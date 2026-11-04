package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
	"github.com/RashKash103/satellite/internal/view"
)

type styles struct {
	ok, warn, bad, muted, accent, bold, head, sel, selDim, border, borderOn lipgloss.Style
	bar                                                                     lipgloss.Style
}

func tstyle(s ui.Styles) styles {
	return styles{ok: s.OK, warn: s.Warn, bad: s.Bad, muted: s.Muted, accent: s.Accent, bold: s.Bold, head: s.Heading,
		sel:    lipgloss.NewStyle().Reverse(true).Bold(true),
		selDim: s.Accent.Bold(true),
		border: s.Muted, borderOn: s.Accent.Bold(true),
		bar: lipgloss.NewStyle().Foreground(s.Bar)}
}

// rect: a pane's place on screen, for the mouse.
type rect struct{ x, y, w, h int }

func (r rect) has(x, y int) bool { return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h }

type geometry struct {
	panes [panes]rect
	mainH int
}

// box draws a pane: a rounded border with its number and title in the top edge and a note in
// the bottom one; content lines are cut or padded to fit.
func box(t styles, num int, title, note string, lines []string, w, h int, on bool) []string {
	b := t.border
	if on {
		b = t.borderOn
	}
	w, h = max(w, 8), max(h, 3)
	in := w - 2
	head := "─ "
	if num > 0 {
		head += fmt.Sprintf("%d ", num)
	}
	tt := ui.Truncate(title, in-len(head)-2)
	top := b.Render("╭"+head) + tt + b.Render(" "+strings.Repeat("─", max(in-ui.Width(head)-ui.Width(tt)-1, 0))+"╮")
	out := []string{top}
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		l = ui.Pad(ui.Truncate(" "+l, in), in)
		out = append(out, b.Render("│")+l+b.Render("│"))
	}
	nt := ""
	if note != "" {
		nt = " " + ui.Truncate(note, in-4) + " "
	}
	out = append(out, b.Render("╰"+strings.Repeat("─", max(in-ui.Width(nt)-1, 0)))+t.muted.Render(nt)+b.Render("─╯"))
	return out
}

func (m *model) dot(t styles, s string) string {
	switch s {
	case "active":
		return t.ok.Render("●")
	case "failed":
		return t.bad.Render("●")
	case "activating", "deactivating":
		return t.warn.Render("●")
	}
	return t.muted.Render("○")
}

func word(s string) string {
	switch s {
	case "active":
		return "running"
	case "inactive", "":
		return "stopped"
	}
	return s
}

func (m *model) projectRows(t styles, w int) []string {
	var r []string
	for i, p := range m.shown() {
		st := word(p.active)
		switch p.active {
		case "active":
			st = t.ok.Render(st)
		case "failed":
			st = t.bad.Render(st)
		default:
			st = t.muted.Render(st)
		}
		tags := ""
		if p.drain {
			tags += " " + t.warn.Render("draining")
		}
		if p.enabled == "enabled" {
			tags += " " + t.muted.Render("auto")
		}
		if pl := p.plan; pl != nil && !pl.DrainAt.IsZero() && !p.drain {
			tags += " " + t.accent.Render("drain "+pl.DrainAt.Format("15:04"))
		}
		if p.st != nil && p.st.Live() && !p.st.HubOK {
			tags += " " + t.bad.Render("hub!")
		}
		if p.st != nil && p.st.Live() && len(p.st.Pending) > 0 {
			tags += " " + t.warn.Render("restart!") // config waiting for a restart: details say which
		}
		name := p.p.Name
		alert := lipgloss.NewStyle()
		switch agent.Worst(p.alerts) {
		case agent.Error:
			alert = t.bad.Bold(true)
		case agent.Warn:
			alert = t.warn.Bold(true)
		}
		badge := ""
		if n := len(p.alerts); n > 0 && agent.Worst(p.alerts) != agent.Info {
			badge = " " + alert.Render(fmt.Sprintf("!%d", n))
		}
		if i == m.sel[paneProjects] {
			name = m.selName(t, name, paneProjects)
		} else {
			name = alert.Render(name)
		}
		name += badge
		r = append(r, ui.Truncate(m.dot(t, p.active)+" "+name+"  "+st+tags, w))
	}
	if len(m.ps) == 0 {
		r = append(r, t.muted.Render("none yet: a adds one"))
	} else if len(r) == 0 {
		r = append(r, t.muted.Render("none match /"+m.filter[paneProjects]))
	}
	return r
}

func (m *model) selName(t styles, s string, p pane) string {
	if m.focus == p {
		return t.sel.Render(s)
	}
	return t.selDim.Render(s)
}

// laneText: one lane, in at most w columns.
func (m *model) laneText(p *proj, slot, w int) string {
	t := tstyle(m.st)
	var l agent.Lane
	if p.st.Live() && slot <= len(p.st.Lanes) {
		l = p.st.Lanes[slot-1]
	}
	switch {
	case !p.st.Live():
		return t.muted.Render("not running")
	case l.ID == "" && l.Waiting == "hub":
		return t.warn.Render("waiting for the hub")
	case l.ID == "" && l.Waiting == "disk":
		return t.warn.Render("waiting for disk space")
	case l.ID == "" && l.Waiting == "ready":
		return t.muted.Render("held back by sat/ready")
	case l.ID == "" && p.drain:
		return t.muted.Render("idle (draining)")
	case l.ID == "":
		return t.muted.Render("idle: waiting for work")
	}
	s := t.bold.Render(l.ID) + " " + Dur(time.Since(l.Started))
	if l.Draining {
		s += " " + t.warn.Render("draining") // the details say it closes after this job
	}
	if l.Percent >= 0 {
		if n := w - ui.Width(s) - 7; n >= 6 {
			s += " " + bar(t, l.Percent, min(n, 14))
		}
		s += fmt.Sprintf(" %3d%%", l.Percent)
	}
	if room := w - ui.Width(s) - 1; l.Progress != "" && room >= 4 {
		s += " " + t.muted.Render(ui.Truncate(l.Progress, room))
	}
	return s
}

func bar(t styles, pct, n int) string {
	f := min(max(pct, 0), 100) * n / 100
	return t.bar.Render(strings.Repeat("█", f)) + t.muted.Render(strings.Repeat("░", n-f))
}

func (m *model) laneRows(t styles, w int) []string {
	p := m.cur()
	if p == nil {
		return []string{t.muted.Render("no project selected")}
	}
	var r []string
	for i, s := range m.lanes() {
		n := fmt.Sprintf("%d", s)
		if i == m.sel[paneLanes] {
			n = m.selName(t, n, paneLanes)
		}
		r = append(r, ui.Truncate(n+" "+m.laneText(p, s, w-2), w))
	}
	if len(r) == 0 {
		r = append(r, t.muted.Render("none match /"+m.filter[paneLanes]))
	}
	return r
}

func (m *model) detailRows(t styles, w int) []string {
	p := m.cur()
	if p == nil {
		return []string{t.muted.Render("a: add a project"), t.muted.Render("?: every key")}
	}
	kv := func(k, v string) string { return ui.Truncate(t.muted.Render(ui.Pad(k, 8))+v, w) }
	auto := "off"
	if p.enabled == "enabled" {
		auto = t.ok.Render("on")
	}
	r := []string{kv("service", word(p.active)+t.muted.Render(" · auto ")+auto)}
	h := p.p.Conf["HUB"]
	st := p.st
	now := time.Now()
	r = append(r, kv("hub", h))
	switch {
	case !st.Live() && st != nil && st.Note != "":
		r = append(r, kv("", t.warn.Render(st.Note)))
	case !st.Live():
		r = append(r, kv("", t.muted.Render("checked while the service runs")))
	case !st.HubOK:
		r = append(r, kv("", t.bad.Render("unreachable")+t.muted.Render(" · contact "+ui.Ago(st.LastContact, now))))
	case !st.ProtocolOK:
		r = append(r, kv("", t.warn.Render(st.Note)))
	default:
		r = append(r, kv("", t.ok.Render("reachable")+t.muted.Render(" · protocol "+st.Protocol+" · "+ui.Ago(st.LastContact, now))))
	}
	if st.Live() && st.Restart != "" && now.Sub(st.RestartAt) < time.Hour {
		r = append(r, kv("agent", st.Restart+t.muted.Render(" · "+ui.Ago(st.RestartAt, now))))
	}
	d := p.disk
	ds := fmt.Sprintf("%d GB free", d.Avail>>30) + t.muted.Render(fmt.Sprintf(" · keeps %d GB", d.Need>>30))
	if !d.OK(1) {
		ds = t.warn.Render(fmt.Sprintf("%d GB free: holding, low on room", d.Avail>>30))
	}
	if st.Live() {
		open, draining := 0, 0
		for _, l := range st.Lanes {
			if !l.Off {
				open++
			}
			if l.Draining {
				draining++
			}
		}
		ls := fmt.Sprintf("%d", open)
		if draining > 0 {
			ls += t.warn.Render(fmt.Sprintf(" (%d draining: closes after its job)", draining))
		}
		if st.Slots > 0 && st.Slots != open-draining {
			ls += t.muted.Render(fmt.Sprintf(" · SLOTS %d", st.Slots))
		}
		if draining > 0 || (st.Slots > 0 && st.Slots != open-draining) {
			r = append(r, kv("lanes", ls)) // only when it says more than the lanes pane
		}
		if len(st.Pending) > 0 {
			r = append(r, kv("config", t.warn.Render(strings.Join(st.Pending, ", ")+" changed: restart to apply")))
			r = append(r, kv("", t.muted.Render("sat "+p.p.Name+" stop && sat "+p.p.Name+" start")))
		}
	}
	r = append(r, kv("disk", ds))
	if d.Used > 0 {
		r = append(r, kv("", t.muted.Render("project "+ui.Bytes(d.Used))))
	}
	switch {
	case p.planErr != nil:
		r = append(r, kv("drain", t.warn.Render(p.planErr.Error())))
	case p.plan != nil:
		r = append(r, kv("drain", p.plan.Text(now)+t.muted.Render(" · D changes it")))
	}
	r = append(r, kv("files", t.muted.Render(tilde(p.p.Base))))
	if len(p.alerts) > 0 {
		r = append(r, kv("alerts", alertWord(t, p.alerts)+t.muted.Render(" · ! lists them")))
	}
	return r
}

func alertWord(t styles, as []agent.Alert) string {
	s := ui.Plural(len(as), "alert", "alerts")
	switch agent.Worst(as) {
	case agent.Error:
		return t.bad.Render(s)
	case agent.Warn:
		return t.warn.Render(s)
	}
	return t.muted.Render(s)
}

// nodeRows: this machine's CPU, memory and load, and each project's disk room.
func (m *model) nodeRows(t styles, w, ch int) []string {
	n := m.node
	r := []string{t.head.Render("Node") + t.muted.Render(" "+conf.ShortHost())}
	sw := min(max(w-6-12, 6), 60)
	line := func(k string, vs []float64, top float64, val string) {
		for i, l := range view.SparkH(m.st, vs, sw, ch, top) {
			if i == 0 {
				r = append(r, ui.Truncate(t.muted.Render(ui.Pad(k, 6))+l+" "+val, w))
			} else {
				r = append(r, ui.Truncate(ui.Pad("", 6)+l, w))
			}
		}
	}
	if v, ok := last(n.cpu); ok {
		line("cpu", n.cpu, 100, fmt.Sprintf("%3.0f%%", v))
	} else {
		r = append(r, t.muted.Render(ui.Pad("cpu", 6)+"measuring…"))
	}
	if v, ok := last(n.mem); ok {
		line("mem", n.mem, 100, fmt.Sprintf("%3.0f%%", v))
	}
	if v, ok := last(n.load); ok {
		line("load", n.load, float64(n.cpus), view.Num(v)+t.muted.Render(fmt.Sprintf(" of %d", n.cpus)))
	}
	for _, p := range m.ps {
		r = append(r, ui.Truncate(t.muted.Render(ui.Pad(ui.Truncate(p.p.Name, 10), 11))+diskGauge(m, t, p.disk, min(max(w-34, 6), 30)), w))
	}
	return r
}

// diskGauge: free space against the disk's size, the floor marked │; held back below it.
func diskGauge(m *model, t styles, d agent.Disk, n int) string {
	if d.Size <= 0 {
		return t.muted.Render("disk not measured")
	}
	free := float64(d.Avail) / float64(d.Size)
	floor := min(int(float64(n)*float64(d.Need)/float64(d.Size)), n-1)
	f := min(int(free*float64(n)), n)
	fill := t.bar
	if !d.OK(1) {
		fill = t.warn
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		switch {
		case i == floor && d.Need > 0:
			b.WriteString(t.warn.Render("│"))
		case i < f:
			b.WriteString(fill.Render("█"))
		default:
			b.WriteString(t.muted.Render("░"))
		}
	}
	return b.String() + fmt.Sprintf(" %d GB free", d.Avail>>30) + t.muted.Render(fmt.Sprintf(" · floor %d", d.Need>>30))
}

func tilde(p string) string {
	if r, err := filepath.Rel(conf.Home(), p); err == nil && !strings.HasPrefix(r, "..") {
		return "~/" + r
	}
	return p
}

// mainLines: the right pane's content, filtered, with its title and note.
func (m *model) mainLines(t styles, w int) (title string, lines []string, note string) {
	list := m.tabList()
	var tl []string
	for i, e := range list {
		n := e.name
		if e.v != nil {
			n = "◆ " + n
		}
		if tab(i) == m.tab {
			n = t.head.Render(n)
		} else {
			n = t.muted.Render(n)
		}
		tl = append(tl, n)
	}
	title = strings.Join(tl, t.muted.Render(" │ "))
	if ui.Width(title) > w-4 { // too many to show: the current one, and where it is
		title = t.head.Render(list[m.tab].name) + t.muted.Render(fmt.Sprintf(" (%d of %d) [ ]", m.tab+1, len(list)))
	}
	p := m.cur()
	if e := m.curTab(); e.v != nil {
		lines, note = m.viewLines(t, w)
		return title, m.filtered(lines, &note), note
	}
	switch m.tab {
	case tabAlerts:
		lines = m.alertLines(t)
		note = "every project's alerts · docs/node.md#alerts"
	case tabLog:
		if p == nil {
			break
		}
		s := m.lane()
		if s == 0 {
			break
		}
		lf := filepath.Join(p.p.State, fmt.Sprintf("slot%d.log", s))
		lines = agent.Tail(lf, 2000)
		note = fmt.Sprintf("%s lane %d · %s", p.p.Name, s, filepath.Base(lf))
		if len(lines) == 0 {
			lines = []string{t.muted.Render("(no output yet)")}
		}
	case tabService:
		if p == nil {
			break
		}
		for _, l := range m.svc {
			lines = append(lines, logLine(t, l))
		}
		note = p.p.Unit()
		if len(lines) == 0 {
			lines = []string{t.muted.Render("(nothing logged yet)")}
		}
	case tabOutput:
		if m.job == nil {
			lines = []string{t.muted.Render("Actions show their output here: c check, t selftest, u update, s start ...")}
			break
		}
		lines = append([]string{t.muted.Render("$ sat " + strings.Join(m.job.args, " "))}, m.job.lines...)
		switch {
		case m.job.running:
			note = m.spin.View() + " " + m.job.title + " · " + Dur(time.Since(m.job.start))
		case m.job.err != nil:
			note = m.job.title + ": " + m.job.err.Error()
		default:
			note = m.job.title + ": done"
		}
	}
	return title, m.filtered(lines, &note), note
}

func (m *model) filtered(lines []string, note *string) []string {
	f := strings.ToLower(m.filter[paneMain])
	if f == "" {
		return lines
	}
	var keep []string
	for _, l := range lines {
		if strings.Contains(strings.ToLower(ui.Strip(l)), f) {
			keep = append(keep, l)
		}
	}
	*note += " · /" + m.filter[paneMain]
	return keep
}

// viewLines: the current view tab: its alerts first, then what it showed, and how its last run
// went if that is worth saying.
func (m *model) viewLines(t styles, w int) ([]string, string) {
	r := m.vr
	if r == nil {
		return []string{t.muted.Render("starting…")}, ""
	}
	rd := view.Render{Width: w, Color: true, S: m.st, Hist: r.hist}
	var ls []string
	if r.status != "" {
		ls = append(ls, rd.Error(r.status)...)
	}
	switch {
	case r.v.Format == view.FormatJSON && r.bad != "":
		ls = append(ls, rd.Error(r.bad)...)
	case r.v.Format == view.FormatJSON && r.doc != nil:
		ls = append(ls, rd.Doc(r.doc)...)
	}
	ls = append(ls, r.lines...)
	if len(ls) == 0 {
		if r.running {
			ls = []string{t.muted.Render("running " + r.v.Name + "…")}
		} else {
			ls = []string{t.muted.Render("(the view showed nothing)")}
		}
	}
	return ls, r.note()
}

// alertLines: every project's alerts, the selected project's first.
func (m *model) alertLines(t styles) []string {
	var ls []string
	ps := append([]*proj(nil), m.ps...)
	if c := m.cur(); c != nil {
		for i, p := range ps {
			if p == c {
				ps = append(append([]*proj{c}, ps[:i]...), ps[i+1:]...)
				break
			}
		}
	}
	now := time.Now()
	for _, p := range ps {
		if len(p.alerts) == 0 {
			continue
		}
		ls = append(ls, t.bold.Render(p.p.Name))
		for _, a := range p.alerts {
			mark, st := ui.MarkInfo, t.muted
			switch a.Level {
			case agent.Error:
				mark, st = ui.MarkBad, t.bad
			case agent.Warn:
				mark, st = ui.MarkWarn, t.warn
			}
			since := ""
			if !a.Since.IsZero() {
				since = Dur(now.Sub(a.Since))
			}
			ls = append(ls, "  "+t.muted.Render(ui.Pad(since, 7))+" "+st.Render(mark+" "+a.Text))
		}
	}
	if len(ls) == 0 {
		ls = []string{t.ok.Render(ui.MarkOK) + " no alerts"}
	}
	return ls
}

func logLine(t styles, l string) string {
	low := strings.ToLower(l)
	switch {
	case containsAny(low, "failed", "cannot", "could not", "unreachable", "lost", "missing", "refus"):
		return t.bad.Render(l)
	case containsAny(low, "handing it back", "handed back", "not taking work", "low on disk", "drain", "restarting", "waiting"):
		return t.warn.Render(l)
	case containsAny(low, " passed", "claimed", "claiming"):
		return t.ok.Render(l)
	}
	return l
}

func containsAny(s string, w ...string) bool {
	for _, x := range w {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// tail: the last h lines of ls, scrolled up by m.scroll.
func (m *model) tail(ls []string, h int) []string {
	h = max(h, 0)
	m.scroll = min(m.scroll, max(len(ls)-h, 0))
	end := len(ls) - m.scroll
	return ls[max(end-h, 0):end]
}

func (m *model) footer(t styles) string {
	if m.input != nil {
		return m.input.View() + t.muted.Render("  enter apply · esc cancel")
	}
	k := func(key, what string) string { return t.accent.Render(key) + " " + t.muted.Render(what) }
	var ks []string
	switch {
	case m.modal != nil:
		ks = m.modal.hints(k)
	case m.focus == paneMain || m.focus == paneDetails:
		ks = []string{k("j/k", "scroll"), k("g/G", "top/end"), k("[ ]", "tab"), k("w", "views"), k("/", "filter"), k("tab", "pane")}
	default:
		ks = []string{k("j/k", "move"), k("s", "start"), k("x", "stop"), k("d", "drain"), k("D", "schedule"), k("c", "check"), k("t", "test"), k("e", "env"), k("a", "add"), k("/", "filter"), k("tab", "pane")}
	}
	end := k("?", "keys") + "  " + k("q", "quit")
	if m.modal != nil {
		return strings.Join(ks, "  ")
	}
	w := m.w
	if w <= 0 {
		w = 100
	}
	out := ""
	for _, x := range ks {
		if ui.Width(out+x+"  "+end) > w {
			break
		}
		out += x + "  "
	}
	return out + end
}

// helpGroups: every key, by where it works.
var helpGroups = []struct {
	title string
	keys  [][2]string
}{
	{"Everywhere", [][2]string{
		{"tab ⇧tab", "next / previous pane"}, {"1 2 3 4", "Projects, Lanes, Details, the right pane"},
		{"/", "filter the pane (esc clears)"}, {"[ ]", "the right pane's tabs: logs, output, alerts, views"},
		{"w", "the next view tab (◆: the project's own views)"}, {"!", "alerts, every project's"},
		{"L", "service log"}, {"o", "output of the last action"},
		{"r", "refresh; runs the shown view again"}, {"a", "add a project (sat setup)"}, {"S", "self-update: notes, checks, asks"},
		{"M", "migrate a bash node (~/sat), when there is one"}, {"ctrl+u", "uninstall everything: lists it, asks"},
		{"?", "this help"}, {"q", "quit"}}},
	{"Projects and Lanes", [][2]string{
		{"j k ↑ ↓", "move"}, {"enter", "into the lanes, then a lane's log"}, {"s", "start"}, {"d", "drain"},
		{"D", "schedule a drain: start at, or be done by, a time"}, {"x", "stop (asks)"},
		{"A", "autostart on/off"}, {"c", "check what is missing"}, {"i", "install it: every step shown, asks"},
		{"t", "selftest"}, {"u", "update: bundle, check, project setup"}, {"e", "edit env in $EDITOR"}, {"E", "edit config"},
		{"v", "set or unset an env KEY"}, {"l", "the lane's log"}, {"U", "uninstall this project: lists it, asks"}}},
	{"Right pane and Details", [][2]string{
		{"j k ↑ ↓", "scroll (the mouse wheel too)"}, {"pgup pgdown", "a page"}, {"g G", "top, end (follow)"}}},
	{"Dialogs", [][2]string{
		{"tab ⇧tab ↑ ↓", "the next / previous field or button"}, {"← →", "between the buttons; change a choice"},
		{"enter", "the focused button (on a field: the next)"}, {"space", "toggle, or the focused button"},
		{"y n", "yes / no, when the dialog asks one"}, {"esc", "cancel"}, {"mouse", "click a button or a field"}}},
}

func (m *model) helpLines(t styles, w int) []string {
	group := func(gs []int, w int) []string {
		var r []string
		for _, i := range gs {
			g := helpGroups[i]
			r = append(r, t.head.Render(g.title))
			for _, k := range g.keys {
				r = append(r, ui.Truncate("  "+t.accent.Render(ui.Pad(k[0], 15))+k[1], w))
			}
			r = append(r, "")
		}
		return r
	}
	var r []string
	if w >= 76 {
		cw := (w - 2) / 2
		a, b := group([]int{0, 2}, cw), group([]int{1, 3}, cw)
		for i := 0; i < max(len(a), len(b)); i++ {
			l, x := "", ""
			if i < len(a) {
				l = a[i]
			}
			if i < len(b) {
				x = b[i]
			}
			r = append(r, ui.Pad(l, cw+2)+x)
		}
	} else {
		r = group([]int{0, 1, 2, 3}, w)
	}
	r = append(r, ui.Truncate(t.muted.Render("Install, uninstall, setup --install, self-update and migrate hand the terminal to sat"), w),
		ui.Truncate(t.muted.Render("itself: it shows exactly what it will do and asks before anything runs."), w))
	return r
}

func (m *model) View() tea.View {
	w, h := m.w, m.h
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}
	t := tstyle(m.st)
	head := t.bold.Render("satellite") + t.muted.Render(" "+m.version+" · node "+conf.ShortHost())
	if m.update != "" {
		head += "  " + t.warn.Render("↑ "+m.update+" available: S")
	}
	if m.legacy {
		head += "  " + t.warn.Render("bash node in ~/sat: M migrates it")
	}
	head = ui.Truncate(head, w)
	bodyH := h - 3
	var body []string
	if w < 24 || bodyH < 4 { // no room for the panes
		body = make([]string, max(bodyH, 0))
		if len(body) > 0 {
			body[0] = t.muted.Render("the terminal is too small")
		}
		m.geo = geometry{}
	} else {
		body = m.panes(t, w, bodyH)
	}
	msg := ""
	if m.msg != "" {
		msg = m.msg
		if m.msgBad {
			msg = t.bad.Render(msg)
		}
	}
	out := append([]string{head}, body...)
	var dl []string // the dialog, drawn first: the footer lists its keys
	if d := m.modal; d != nil && h > 1 {
		dl = d.view(t, w, h-1) // the footer stays: it lists the dialog's keys
	}
	out = append(out, ui.Truncate(msg, w), ui.Truncate(m.footer(t), w))
	if len(out) > h {
		out = out[len(out)-h:]
	}
	for i := range out {
		out[i] = ui.Truncate(out[i], w)
	}
	if d := m.modal; d != nil && len(dl) > 0 {
		sh := len(out) - 1
		d.place(w, sh, ui.Width(dl[0]), len(dl))
		out = append(overlay(t, out[:sh], dl, d.at.x, d.at.y), out[sh:]...)
	}
	v := tea.NewView(strings.Join(out, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "satellite"
	return v
}

// layout: where the panes go in w x h. As the terminal shrinks the node charts go first, then
// Details; the Lanes pane keeps room for a few lanes.
type layout struct {
	lw, rw, ph, lh, dh int
	details, node      bool
	chartH             int        // rows per node chart
	top                [panes]int // the first row shown, per pane
}

func (m *model) layout(t styles, w, h int) layout {
	var l layout
	l.lw = min(max(w*2/5, 30), 48)
	if w < 60 {
		l.lw = w / 2
	}
	l.rw = w - l.lw
	l.ph = min(max(len(m.ps), 1)+2, max(h/3, 4))
	lanes := 1
	if p := m.cur(); p != nil {
		lanes = p.slots
	}
	lmin := min(lanes, 4) + 2
	avail := h - l.ph - lmin
	base := len(m.detailRows(t, l.lw-4)) + 2
	node := len(m.ps) + 4
	switch {
	case avail >= base+node+1 && h >= 30:
		// the lanes get what they need, the charts the rest (taller, up to 4 rows)
		l.details, l.node = true, true
		l.lh = max(lmin, min(lanes+2, h-l.ph-base-node-1))
		l.chartH = min(max(1+(h-l.ph-l.lh-base-node-1)/3, 1), 4)
		l.dh = h - l.ph - l.lh
		return l
	case avail >= 5:
		l.details, l.dh = true, min(base, avail, 9)
	}
	l.lh = h - l.ph - l.dh
	return l
}

// mainWidth: the columns inside the right pane.
func (m *model) mainWidth() int {
	if m.lay.rw > 0 {
		return m.lay.rw - 4
	}
	w := m.w
	if w <= 0 {
		w = 100
	}
	return w - min(max(w*2/5, 30), 48) - 4
}

// more: how far a pane's content runs past its window, as ↑N ↓N.
func more(t styles, base string, top, shown, total int) string {
	var a []string
	if base != "" {
		a = append(a, base)
	}
	if top > 0 {
		a = append(a, fmt.Sprintf("↑%d", top))
	}
	if rest := total - top - shown; rest > 0 {
		a = append(a, fmt.Sprintf("↓%d", rest))
	}
	return strings.Join(a, " ")
}

func (m *model) panes(t styles, w, h int) []string {
	l := m.layout(t, w, h)
	lw, rw, ph, lh, dh := l.lw, l.rw, l.ph, l.lh, l.dh
	if !l.details && m.focus == paneDetails {
		m.focus = paneLanes
	}
	n := m.cur()
	lt := paneNames[paneLanes]
	if n != nil {
		lt += t.muted.Render(" · " + n.p.Name)
	}
	note := func(p pane) string {
		if m.filter[p] != "" {
			return "/" + m.filter[p]
		}
		return ""
	}
	pn := note(paneProjects)
	if pn == "" && len(m.ps) > 0 {
		pn = fmt.Sprintf("%d of %d", min(m.sel[paneProjects]+1, len(m.shown())), len(m.shown()))
	}
	pr := m.projectRows(t, lw-4)
	prs, ptop := scrollTo(pr, m.sel[paneProjects], ph-2)
	l.top[paneProjects] = ptop
	left := box(t, 1, paneNames[paneProjects], more(t, pn, ptop, len(prs), len(pr)), prs, lw, ph, m.focus == paneProjects)
	lr := m.laneRows(t, lw-4)
	lrs, ltop := scrollTo(lr, m.sel[paneLanes], lh-2)
	l.top[paneLanes] = ltop
	left = append(left, box(t, 2, lt, more(t, note(paneLanes), ltop, len(lrs), len(lr)), lrs, lw, lh, m.focus == paneLanes)...)
	if l.details {
		dr := m.detailRows(t, lw-4)
		if l.node {
			dr = append(append(dr, ""), m.nodeRows(t, lw-4, l.chartH)...)
		}
		m.dtop = min(m.dtop, max(len(dr)-(dh-2), 0))
		end := min(m.dtop+dh-2, len(dr))
		l.top[paneDetails] = m.dtop
		left = append(left, box(t, 3, paneNames[paneDetails], more(t, "", m.dtop, end-m.dtop, len(dr)), dr[m.dtop:end], lw, dh, m.focus == paneDetails)...)
	}
	m.lay = l
	title, lines, nt := m.mainLines(t, rw-4)
	for i := range lines {
		lines[i] = ui.Truncate(lines[i], rw-4)
	}
	shown := m.tail(lines, h-2)
	top := len(lines) - m.scroll - len(shown)
	right := box(t, 4, title, more(t, nt, top, len(shown), len(lines)), shown, rw, h, m.focus == paneMain)
	m.geo = geometry{mainH: h - 2}
	m.geo.panes[paneProjects] = rect{0, 1, lw, ph}
	m.geo.panes[paneLanes] = rect{0, 1 + ph, lw, lh}
	if l.details {
		m.geo.panes[paneDetails] = rect{0, 1 + ph + lh, lw, dh}
	}
	m.geo.panes[paneMain] = rect{lw, 1, rw, h}
	out := make([]string, h)
	for i := range out {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = ui.Pad(l, lw) + r
	}
	return out
}

// scrollTo: the window of rows that keeps the selected one in view, and its first row.
func scrollTo(rows []string, sel, h int) ([]string, int) {
	if h <= 0 || len(rows) <= h {
		return rows, 0
	}
	start := min(max(sel-h+1, 0), len(rows)-h)
	return rows[start : start+h], start
}

func (m *model) click(x, y int) {
	for p := pane(0); p < panes; p++ {
		r := m.geo.panes[p]
		if !r.has(x, y) {
			continue
		}
		m.focus = p
		row := y - r.y - 1 + m.lay.top[p]
		switch p {
		case paneProjects:
			if row >= 0 && row < len(m.shown()) {
				m.sel[p] = row
				m.sel[paneLanes] = 0
			}
		case paneLanes:
			if row >= 0 && row < len(m.lanes()) {
				m.sel[p] = row
				m.scroll = 0
			}
		}
		m.clamp()
		return
	}
}

func (m *model) wheel(x, y int, up bool) {
	for p := pane(0); p < panes; p++ {
		if m.geo.panes[p].has(x, y) {
			old := m.focus
			m.focus = p
			if up {
				m.move("up")
			} else {
				m.move("down")
			}
			if p == paneMain {
				m.focus = old
			}
			return
		}
	}
}
