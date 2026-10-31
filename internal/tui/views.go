package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/view"
)

// The right pane's tabs: these four, then the selected project's views, then (while the selected
// lane has a job) its lane views.
const (
	tabLog tab = iota
	tabService
	tabOutput
	tabAlerts
	fixedTabs
)

var (
	tabNames = [fixedTabs]string{"Lane log", "Service log", "Output", "Alerts"}
	tabKeys  = [fixedTabs]string{"log", "service", "output", "alerts"}
)

type tabEntry struct {
	key, name string
	v         *view.View
}

func (m *model) tabList() []tabEntry {
	r := make([]tabEntry, 0, fixedTabs+4)
	for i := tab(0); i < fixedTabs; i++ {
		r = append(r, tabEntry{key: tabKeys[i], name: tabNames[i]})
	}
	p := m.cur()
	if p == nil {
		return r
	}
	for i := range p.views {
		if v := &p.views[i]; v.Scope == view.ScopeProject {
			r = append(r, tabEntry{"view:" + v.Name, v.Title, v})
		}
	}
	if m.busyLane() != nil {
		for i := range p.views {
			if v := &p.views[i]; v.Scope == view.ScopeLane {
				r = append(r, tabEntry{"view:" + v.Name, v.Title, v})
			}
		}
	}
	return r
}

func (m *model) curTab() tabEntry {
	l := m.tabList()
	if int(m.tab) < len(l) {
		return l[m.tab]
	}
	return l[0]
}

// busyLane: the selected lane, when it has a job.
func (m *model) busyLane() *agent.Lane {
	p, s := m.cur(), m.lane()
	if p == nil || s == 0 || !p.st.Live() || s > len(p.st.Lanes) || p.st.Lanes[s-1].ID == "" {
		return nil
	}
	return &p.st.Lanes[s-1]
}

// setTab: tab t (wrapping), remembered for the selected project.
func (m *model) setTab(t tab) {
	l := m.tabList()
	n := tab(len(l))
	m.tab = ((t % n) + n) % n
	m.tabKey, m.scroll = l[m.tab].key, 0
	if p := m.cur(); p != nil {
		m.tabFor[p.p.Name] = m.tabKey
	}
	if m.tab == tabService {
		m.loadService()
	}
}

// nextView: the next view tab after the current one.
func (m *model) nextView() {
	l := m.tabList()
	for i := 1; i <= len(l); i++ {
		j := (int(m.tab) + i) % len(l)
		if l[j].v != nil {
			m.setTab(tab(j))
			return
		}
	}
	m.say("no views for this project or lane (sat/views/ in the bundle: docs/projects.md)", false)
}

// syncTabs keeps the tab on the same entry as the list changes: a project's remembered tab when
// the selection moves to it, the lane log when a lane view's job ends.
func (m *model) syncTabs() {
	name := ""
	if p := m.cur(); p != nil {
		name = p.p.Name
	}
	if name != m.tabProj {
		m.tabProj = name
		if k, ok := m.tabFor[name]; ok {
			m.tabKey = k
		}
		m.scroll = 0
	}
	l := m.tabList()
	idx := -1
	for i, e := range l {
		if e.key == m.tabKey {
			idx = i
		}
	}
	if idx < 0 {
		idx = 0
		if !strings.HasPrefix(m.tabKey, "view:") {
			for i, k := range tabKeys {
				if k == m.tabKey {
					idx = i
				}
			}
		}
		m.tabKey = l[idx].key
	}
	if tab(idx) != m.tab && tab(idx) == tabService {
		m.loadService()
	}
	m.tab = tab(idx)
}

// viewRun: the view shown in the right pane, running.
type viewRun struct {
	id      int
	key     string
	p       *conf.Project
	v       view.View
	lane    *view.Lane
	ctx     context.Context
	cancel  context.CancelFunc
	ch      chan tea.Msg
	running bool
	runs    int       // runs finished
	at      time.Time // the last result
	lines   []string  // a text view's output
	doc     *view.Doc // a json view's last document
	bad     string    // the document could not be read
	status  string    // how the last run ended, when that is worth saying
	hist    *view.Hist
}

type (
	viewSnap struct {
		id          int
		out, errOut []byte
		err         error
	}
	viewLine struct {
		id   int
		line string
	}
	viewEnd struct {
		id  int
		err error
	}
	viewTick struct{ id int }
)

func waitCtx(ctx context.Context, ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		select {
		case m := <-ch:
			return m
		case <-ctx.Done():
			return nil
		}
	}
}

func (m *model) runKey(p *proj, v *view.View, l *agent.Lane) string {
	k := p.p.Name + "/" + v.Name + "/" + p.vsig
	if v.Scope == view.ScopeLane && l != nil {
		k += "/" + strconv.Itoa(l.Slot) + "/" + l.ID
	}
	return k
}

// syncRun starts the view the current tab shows, and stops one it no longer shows.
func (m *model) syncRun() tea.Cmd {
	e := m.curTab()
	p := m.cur()
	want := ""
	var l *agent.Lane
	if e.v != nil && p != nil {
		if e.v.Scope == view.ScopeLane {
			l = m.busyLane()
		}
		want = m.runKey(p, e.v, l)
	}
	if m.vr != nil && m.vr.key == want {
		return nil
	}
	m.stopRun()
	if want == "" {
		return nil
	}
	return m.startRun(p, *e.v, l, want)
}

func (m *model) stopRun() {
	if m.vr != nil {
		m.vr.cancel()
		m.vr = nil
	}
}

func (m *model) startRun(p *proj, v view.View, l *agent.Lane, key string) tea.Cmd {
	m.runs++
	ctx, cancel := context.WithCancel(context.Background())
	hk := p.p.Name + "/" + v.Name
	if m.hist[hk] == nil {
		m.hist[hk] = view.NewHist()
	}
	r := &viewRun{id: m.runs, key: key, p: p.p, v: v, ctx: ctx, cancel: cancel, ch: make(chan tea.Msg, 256), hist: m.hist[hk]}
	if l != nil {
		r.lane = view.LaneOf(p.p, *l)
	}
	m.vr = r
	if v.Err != nil {
		r.status = "bad header in sat/views/" + v.Name + ": " + v.Err.Error()
		return nil
	}
	return m.launch(r)
}

func (m *model) viewOpts(r *viewRun) view.Opts {
	return view.Opts{Live: true, Width: max(m.mainWidth(), 20), Color: os.Getenv("NO_COLOR") == "", Lane: r.lane}
}

// launch starts one run; it never blocks: the output comes back as messages.
func (m *model) launch(r *viewRun) tea.Cmd {
	r.running = true
	o := m.viewOpts(r)
	send := func(msg tea.Msg) {
		select {
		case r.ch <- msg:
		case <-r.ctx.Done():
		}
	}
	if r.v.Mode == view.ModeSnapshot {
		go func() {
			var out, errb bytes.Buffer
			err := view.Run(r.ctx, r.p, r.v, o, &out, &errb)
			send(viewSnap{r.id, out.Bytes(), errb.Bytes(), err})
		}()
		return waitCtx(r.ctx, r.ch)
	}
	r.lines, r.status = nil, ""
	go func() {
		w := &view.Lines{Color: o.Color, F: func(l string) { send(viewLine{r.id, l}) }}
		err := view.Run(r.ctx, r.p, r.v, o, w, w)
		w.Flush()
		send(viewEnd{r.id, err})
	}()
	return waitCtx(r.ctx, r.ch)
}

// restartRun: r runs the view again now.
func (m *model) restartRun() tea.Cmd {
	if m.vr == nil {
		return nil
	}
	k := m.vr.key
	m.stopRun()
	p, e := m.cur(), m.curTab()
	if p == nil || e.v == nil {
		return nil
	}
	return m.startRun(p, *e.v, m.busyLane(), k)
}

func (r *viewRun) setDoc(b []byte) {
	d, err := view.Parse(b)
	if err != nil {
		r.bad = err.Error()
		return
	}
	r.doc, r.bad = d, ""
	r.hist.Record(d)
}

// viewMsg handles a run's messages; ok is false for one that isn't a view message.
func (m *model) viewMsg(msg tea.Msg) (tea.Cmd, bool) {
	r := m.vr
	switch msg := msg.(type) {
	case viewSnap:
		if r == nil || msg.id != r.id {
			return nil, true
		}
		r.running, r.at = false, time.Now()
		r.runs++
		r.status = ""
		if msg.err != nil {
			r.status = view.Describe(msg.err)
		}
		if r.v.Format == view.FormatJSON {
			if msg.err == nil || len(bytes.TrimSpace(msg.out)) > 0 {
				r.setDoc(msg.out)
			}
			r.lines = cleanLines(msg.errOut)
		} else {
			r.lines = append(cleanLines(msg.out), cleanLines(msg.errOut)...)
		}
		m.noteAlerts(r)
		id := r.id
		return tea.Tick(r.v.Every, func(time.Time) tea.Msg { return viewTick{id} }), true
	case viewTick:
		if r == nil || msg.id != r.id || r.running {
			return nil, true
		}
		return m.launch(r), true
	case viewLine:
		if r == nil || msg.id != r.id {
			return nil, true
		}
		r.at = time.Now()
		if r.v.Format == view.FormatJSON {
			if strings.TrimSpace(msg.line) != "" {
				r.setDoc([]byte(msg.line))
				m.noteAlerts(r)
			}
		} else {
			r.lines = append(r.lines, msg.line)
			if len(r.lines) > 5000 {
				r.lines = r.lines[1000:]
			}
		}
		return waitCtx(r.ctx, r.ch), true
	case viewEnd:
		if r == nil || msg.id != r.id {
			return nil, true
		}
		r.running = false
		r.runs++
		r.status = "the view ended"
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			r.status += ": " + view.Describe(msg.err)
		}
		r.status += " (r runs it again)"
		return nil, true
	}
	return nil, false
}

func cleanLines(b []byte) []string {
	var ls []string
	w := &view.Lines{Color: os.Getenv("NO_COLOR") == "", F: func(l string) { ls = append(ls, l) }}
	w.Write(b)
	w.Flush()
	return ls
}

// noteAlerts keeps the alert blocks of a view's last document, for the Projects pane.
func (m *model) noteAlerts(r *viewRun) {
	k := r.p.Name + "/" + r.v.Name
	if a := r.doc.Alerts(); len(a) > 0 && r.bad == "" {
		m.vAlerts[k] = a
	} else {
		delete(m.vAlerts, k)
	}
}

// viewAlerts: a project's alerts from its views, as agent alerts.
func (m *model) viewAlerts(name string) []agent.Alert {
	var as []agent.Alert
	for k, bs := range m.vAlerts {
		if pn, vn, _ := strings.Cut(k, "/"); pn == name {
			for _, b := range bs {
				as = append(as, agent.Alert{ID: "view:" + vn, Level: b.Level, Text: vn + ": " + b.Text})
			}
		}
	}
	return as
}

// runNote: the right pane's note for a view tab.
func (r *viewRun) note() string {
	s := r.v.Title
	switch {
	case r.v.Err != nil:
		return s + " · bad header"
	case r.v.Mode == view.ModeStream && r.running:
		s += " · following"
	case r.v.Mode == view.ModeSnapshot:
		s += " · every " + view.Secs(r.v.Every)
		if r.running && r.runs == 0 {
			s += " · running"
		} else if !r.at.IsZero() {
			s += " · " + Dur(time.Since(r.at)) + " ago"
		}
	}
	if r.lane != nil {
		s += fmt.Sprintf(" · lane %d %s", r.lane.Slot, r.lane.Job)
	}
	return s
}
