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
		if p.st != nil && p.st.Live() && !p.st.HubOK {
			tags += " " + t.bad.Render("hub!")
		}
		name := p.p.Name
		if i == m.sel[paneProjects] {
			name = m.selName(t, name, paneProjects)
		}
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
	if l.Percent >= 0 {
		if n := w - ui.Width(s) - 6; n >= 6 {
			s += " " + bar(t, l.Percent, min(n, 14))
		}
		s += fmt.Sprintf(" %3d%%", l.Percent)
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
	d := p.disk
	ds := fmt.Sprintf("%d GB free", d.Avail>>30) + t.muted.Render(fmt.Sprintf(" · keeps %d GB", d.Need>>30))
	if !d.OK(1) {
		ds = t.warn.Render(fmt.Sprintf("%d GB free: holding, low on room", d.Avail>>30))
	}
	r = append(r, kv("disk", ds))
	if d.Used > 0 {
		r = append(r, kv("", t.muted.Render("project "+ui.Bytes(d.Used))))
	}
	r = append(r, kv("files", t.muted.Render(tilde(p.p.Base))))
	return r
}

func tilde(p string) string {
	if r, err := filepath.Rel(conf.Home(), p); err == nil && !strings.HasPrefix(r, "..") {
		return "~/" + r
	}
	return p
}

// mainLines: the right pane's content, filtered, with its title and note.
func (m *model) mainLines(t styles) (title string, lines []string, note string) {
	var tl []string
	for i := tab(0); i < tabs; i++ {
		n := tabNames[i]
		if i == m.tab {
			n = t.head.Render(n)
		} else {
			n = t.muted.Render(n)
		}
		tl = append(tl, n)
	}
	title = strings.Join(tl, t.muted.Render(" │ "))
	p := m.cur()
	switch m.tab {
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
	if f := strings.ToLower(m.filter[paneMain]); f != "" {
		var keep []string
		for _, l := range lines {
			if strings.Contains(strings.ToLower(ui.Strip(l)), f) {
				keep = append(keep, l)
			}
		}
		lines = keep
		note += " · /" + m.filter[paneMain]
	}
	return title, lines, note
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
	m.scroll = min(m.scroll, max(len(ls)-h, 0))
	end := len(ls) - m.scroll
	return ls[max(end-h, 0):end]
}

func (m *model) footer(t styles) string {
	if m.confirm != nil {
		return t.warn.Bold(true).Render(m.confirm.prompt)
	}
	if m.input != nil {
		return m.input.View() + t.muted.Render("  enter apply · esc cancel")
	}
	k := func(key, what string) string { return t.accent.Render(key) + " " + t.muted.Render(what) }
	var ks []string
	switch {
	case m.form != nil:
		ks = []string{k("tab", "next"), k("space", "toggle"), k("enter", "set up"), k("esc", "cancel")}
	case m.help:
		ks = []string{k("esc", "close")}
	case m.focus == paneMain || m.focus == paneDetails:
		ks = []string{k("j/k", "scroll"), k("g/G", "top/end"), k("[ ]", "tab"), k("/", "filter"), k("tab", "pane"), k("?", "keys"), k("q", "quit")}
	default:
		ks = []string{k("j/k", "move"), k("s", "start"), k("x", "stop"), k("d", "drain"), k("c", "check"), k("t", "test"), k("e", "env"), k("a", "add"), k("/", "filter"), k("tab", "pane")}
	}
	end := k("?", "keys") + "  " + k("q", "quit")
	if m.form != nil || m.help {
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
		{"/", "filter the pane (esc clears)"}, {"[ ]", "the right pane's tabs"}, {"L", "service log"}, {"o", "output of the last action"},
		{"r", "refresh"}, {"a", "add a project (sat setup)"}, {"S", "self-update: notes, checks, asks"},
		{"M", "migrate a bash node (~/sat), when there is one"}, {"ctrl+u", "uninstall everything: lists it, asks"},
		{"?", "this help"}, {"q", "quit"}}},
	{"Projects and Lanes", [][2]string{
		{"j k ↑ ↓", "move"}, {"enter", "into the lanes, then a lane's log"}, {"s", "start"}, {"d", "drain"}, {"x", "stop (asks)"},
		{"A", "autostart on/off"}, {"c", "check what is missing"}, {"i", "install it: every step shown, asks"},
		{"t", "selftest"}, {"u", "update: bundle, check, project setup"}, {"e", "edit env in $EDITOR"}, {"E", "edit config"},
		{"v", "set or unset an env KEY"}, {"l", "the lane's log"}, {"U", "uninstall this project: lists it, asks"}}},
	{"Right pane and Details", [][2]string{
		{"j k ↑ ↓", "scroll"}, {"pgup pgdown", "a page"}, {"g G", "top, end (follow)"}}},
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
		a, b := group([]int{0, 2}, cw), group([]int{1}, cw)
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
		r = group([]int{0, 1, 2}, w)
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
	switch {
	case m.help:
		hl := m.helpLines(t, w-4)
		m.helpTop = min(m.helpTop, max(len(hl)-(bodyH-2), 0))
		note := "esc closes"
		if len(hl) > bodyH-2 {
			note = "j/k scroll · " + note
		}
		body = box(t, 0, t.head.Render("Keys"), note, hl[m.helpTop:], w, bodyH, true)
	default:
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
	out = append(out, ui.Truncate(msg, w), ui.Truncate(m.footer(t), w))
	v := tea.NewView(strings.Join(out, "\n"))
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "satellite"
	return v
}

func (m *model) panes(t styles, w, h int) []string {
	lw := min(max(w*2/5, 30), 48)
	if w < 60 {
		lw = w / 2
	}
	rw := w - lw
	ph := min(max(len(m.ps), 1)+2, max(h/3, 4))
	dh := min(9, max(h-ph-4, 3))
	lh := h - ph - dh
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
	left := box(t, 1, paneNames[paneProjects], pn, scrollTo(m.projectRows(t, lw-4), m.sel[paneProjects], ph-2), lw, ph, m.focus == paneProjects)
	left = append(left, box(t, 2, lt, note(paneLanes), scrollTo(m.laneRows(t, lw-4), m.sel[paneLanes], lh-2), lw, lh, m.focus == paneLanes)...)
	left = append(left, box(t, 3, paneNames[paneDetails], "", m.detailRows(t, lw-4), lw, dh, m.focus == paneDetails)...)
	var right []string
	if m.form != nil {
		right = box(t, 4, t.head.Render("Add a project"), "sat setup", m.form.view(rw-4), rw, h, true)
	} else {
		title, lines, nt := m.mainLines(t)
		for i := range lines {
			lines[i] = ui.Truncate(lines[i], rw-4)
		}
		right = box(t, 4, title, nt, m.tail(lines, h-2), rw, h, m.focus == paneMain)
	}
	m.geo = geometry{mainH: h - 2}
	m.geo.panes[paneProjects] = rect{0, 1, lw, ph}
	m.geo.panes[paneLanes] = rect{0, 1 + ph, lw, lh}
	m.geo.panes[paneDetails] = rect{0, 1 + ph + lh, lw, dh}
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

// scrollTo: the window of rows that keeps the selected one in view.
func scrollTo(rows []string, sel, h int) []string {
	if h <= 0 || len(rows) <= h {
		return rows
	}
	start := min(max(sel-h+1, 0), len(rows)-h)
	return rows[start : start+h]
}

func (m *model) click(x, y int) {
	for p := pane(0); p < panes; p++ {
		r := m.geo.panes[p]
		if !r.has(x, y) {
			continue
		}
		m.focus = p
		row := y - r.y - 1
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
