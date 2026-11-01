// Package tui is `sat` with no arguments: the node in panes. On the left, the projects, the
// selected project's lanes and its details; on the right, a lane's log, the service log or the
// output of the last action. It reads what each agent writes (state/agent.json, the lane logs)
// and asks systemctl for the services; it opens no connection of its own.
//
// Every action runs this same binary (`sat PROJECT start`, `sat setup ...`), so the live view
// and the command line can't behave differently. Short ones stream their output into the right
// pane; the ones that install, remove or replace anything hand the terminal to the command,
// which shows what it will do and asks, exactly as on the command line.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/plan"
	"github.com/RashKash103/satellite/internal/selfupdate"
	"github.com/RashKash103/satellite/internal/ui"
	"github.com/RashKash103/satellite/internal/view"
)

// Dur: 45s, 3m12s, 2h05m (ui.Dur).
func Dur(d time.Duration) string { return ui.Dur(d) }

// Options: what the command line tells the live view.
type Options struct {
	Legacy func() bool // a node set up by the bash scripts is here (sat migrate)
}

type pane int

const (
	paneProjects pane = iota
	paneLanes
	paneDetails
	paneMain
	panes
)

var paneNames = [panes]string{"Projects", "Lanes", "Details", ""}

type tab int

type proj struct {
	p               *conf.Project
	st              *agent.State
	active, enabled string
	drain           bool
	slots           int
	disk            agent.Disk
	diskAt          time.Time
	views           []view.View
	vsig            string        // the views directory as last read (view.Sig)
	alerts          []agent.Alert // the agent's, a failed service's and the views'
}

// job: an action streaming its output into the Output tab.
type job struct {
	id      int
	title   string
	args    []string
	lines   []string
	running bool
	err     error
	start   time.Time
	ch      chan tea.Msg
}

// action: what a key did, for the record (and the tests): "stream" runs sat in the
// background into the Output tab; "terminal" hands the terminal to sat, which asks itself;
// "edit" opens an editor.
type action struct {
	mode string
	args []string
}

type confirm struct {
	prompt string
	yes    func() tea.Cmd
}

type model struct {
	version, exe string
	opt          Options
	ps           []*proj
	sel          [panes]int // the row selected in each pane (projects: index into shown())
	focus        pane
	tab          tab
	scroll       int // lines up from the end of the right pane (0: follow)
	filter       [panes]string
	input        *textinput.Model
	inputFor     string // "filter", "env"
	help         bool
	helpTop      int
	confirm      *confirm
	form         *setupForm
	job          *job
	jobs         int
	last         action
	msg          string
	msgBad       bool
	w, h         int
	st           ui.Styles
	update       string
	legacy       bool
	spin         spinner.Model
	svc          []string // the selected project's service log
	geo          geometry
	tabKey       string            // the current tab, by key: it stays put as the tab list changes
	tabProj      string            // the project the tab belongs to
	tabFor       map[string]string // the tab last chosen for each project
	vr           *viewRun          // the view the right pane shows
	runs         int
	hist         map[string]*view.Hist   // metric values, per project/view
	vAlerts      map[string][]view.Block // the alert blocks of each view's last document
	node         *nodeStats
	dtop         int // Details: lines scrolled down
	lay          layout
}

type tick time.Time

func Run(version, exe string, opt Options) int {
	m := newModel(version, exe, opt)
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "sat:", err)
		return 1
	}
	return 0
}

func newModel(version, exe string, opt Options) *model {
	m := &model{version: version, exe: exe, opt: opt, st: ui.Palette(true),
		spin:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		tabFor: map[string]string{}, hist: map[string]*view.Hist{}, vAlerts: map[string][]view.Block{}, node: newNodeStats(),
		tabKey: tabKeys[tabLog]}
	m.refresh()
	m.syncTabs()
	return m
}

func every() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tick(t) }) }

func (m *model) Init() tea.Cmd { return tea.Batch(every(), tea.RequestBackgroundColor) }

func sysctl(args ...string) string {
	out, _ := exec.Command(plan.Systemctl(), append([]string{"--user"}, args...)...).Output()
	return strings.TrimSpace(string(out))
}

func journalctl() string {
	if j := os.Getenv("SAT_JOURNALCTL"); j != "" {
		return j
	}
	return "journalctl"
}

func (m *model) refresh() {
	old := map[string]*proj{}
	for _, p := range m.ps {
		old[p.p.Name] = p
	}
	m.ps = nil
	for _, n := range conf.List() {
		p, err := conf.Load(n)
		if err != nil {
			continue
		}
		x := &proj{p: p}
		if o := old[n]; o != nil {
			x.disk, x.diskAt, x.views, x.vsig = o.disk, o.diskAt, o.views, o.vsig
		}
		if sig := view.Sig(p); sig != x.vsig || x.views == nil {
			x.views, x.vsig = view.List(p), sig // a new bundle: its views are read again
		}
		x.st, _ = agent.ReadState(p)
		x.active, x.enabled = sysctl("is-active", p.Unit()), sysctl("is-enabled", p.Unit())
		_, err = os.Stat(filepath.Join(p.State, "drain"))
		x.drain = err == nil && x.active == "active" // a stopped project is not draining
		fmt.Sscan(p.Get("SLOTS", "1"), &x.slots)
		x.slots = max(x.slots, 1)
		if x.st.Live() {
			x.slots = len(x.st.Lanes)
		}
		if time.Since(x.diskAt) > 30*time.Second {
			x.disk, x.diskAt = agent.Measure(p, true), time.Now() // the size is cached for 30 min
		}
		x.alerts = agent.Sorted(append(m.engineAlerts(x), m.viewAlerts(n)...))
		m.ps = append(m.ps, x)
	}
	m.node.sample(time.Now())
	m.update = selfupdate.Cached(m.version)
	if m.opt.Legacy != nil {
		m.legacy = m.opt.Legacy()
	}
	m.clamp()
	if m.tab == tabService {
		m.loadService()
	}
}

// engineAlerts: what the project's agent raised, and a failed service.
func (m *model) engineAlerts(x *proj) []agent.Alert {
	var as []agent.Alert
	if a := agent.ServiceAlert(x.p, x.active); a != nil {
		as = append(as, *a)
	}
	return append(as, x.st.Active()...)
}

// shown: the projects that pass the filter.
func (m *model) shown() []*proj {
	f := strings.ToLower(m.filter[paneProjects])
	var r []*proj
	for _, p := range m.ps {
		if f == "" || strings.Contains(strings.ToLower(p.p.Name), f) {
			r = append(r, p)
		}
	}
	return r
}

func (m *model) cur() *proj {
	s := m.shown()
	if m.sel[paneProjects] < len(s) {
		return s[m.sel[paneProjects]]
	}
	return nil
}

// lanes: the selected project's lane numbers that pass the filter.
func (m *model) lanes() []int {
	p := m.cur()
	if p == nil {
		return nil
	}
	f := strings.ToLower(m.filter[paneLanes])
	var r []int
	for s := 1; s <= p.slots; s++ {
		if f == "" || strings.Contains(strings.ToLower(ui.Strip(m.laneText(p, s, 200))), f) {
			r = append(r, s)
		}
	}
	return r
}

func (m *model) lane() int {
	l := m.lanes()
	if m.sel[paneLanes] < len(l) {
		return l[m.sel[paneLanes]]
	}
	return 0
}

func (m *model) clamp() {
	m.sel[paneProjects] = min(m.sel[paneProjects], max(len(m.shown())-1, 0))
	m.sel[paneLanes] = min(m.sel[paneLanes], max(len(m.lanes())-1, 0))
}

func (m *model) say(s string, bad bool) { m.msg, m.msgBad = s, bad }

func (m *model) busy() bool {
	if m.job != nil && m.job.running {
		m.say("wait for "+m.job.title+" to finish", true)
		return true
	}
	return false
}

// stream: run sat with args in the background, its output into the Output tab.
func (m *model) stream(title string, args ...string) tea.Cmd {
	if m.busy() {
		return nil
	}
	m.last = action{"stream", args}
	m.jobs++
	j := &job{id: m.jobs, title: title, args: args, running: true, start: time.Now(), ch: make(chan tea.Msg, 64)}
	m.job, m.scroll = j, 0
	m.setTab(tabOutput)
	m.say(title+"...", false)
	cmd := exec.Command(m.exe, args...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1") // plain lines for the pane
	go runJob(cmd, j.id, j.ch)
	return tea.Batch(wait(j.ch), m.spin.Tick)
}

type jobLine struct {
	id   int
	line string
}

type jobDone struct {
	id  int
	err error
}

// runJob runs cmd with no terminal (so nothing it would ask runs) and sends its lines.
func runJob(cmd *exec.Cmd, id int, ch chan tea.Msg) {
	pr, pw, err := os.Pipe()
	if err != nil {
		ch <- jobDone{id, err}
		return
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		ch <- jobDone{id, err}
		return
	}
	pw.Close()
	buf := make([]byte, 4096)
	var part string
	for {
		n, rerr := pr.Read(buf)
		part += string(buf[:n])
		for {
			i := strings.IndexAny(part, "\n\r")
			if i < 0 {
				break
			}
			ch <- jobLine{id, part[:i]}
			part = part[i+1:]
		}
		if rerr != nil {
			break
		}
	}
	if part != "" {
		ch <- jobLine{id, part}
	}
	pr.Close()
	ch <- jobDone{id, cmd.Wait()}
}

func wait(ch chan tea.Msg) tea.Cmd { return func() tea.Msg { return <-ch } }

// terminalCmd: `sat args` in the terminal, then a pause to read what it said.
func (m *model) terminalCmd(args ...string) *exec.Cmd {
	return exec.Command("bash", append([]string{"-c", `"$0" "$@"; rc=$?; if [ $rc = 0 ]; then r=done; else r="exit $rc"; fi; printf '\n[%s] Enter: back to the live view ' "$r"; read -r _; exit $rc`, m.exe}, args...)...)
}

// terminal: hand the terminal to `sat args`; it shows what it will do and asks, as on the
// command line. Enter brings the live view back.
func (m *model) terminal(title string, args ...string) tea.Cmd {
	if m.busy() {
		return nil
	}
	m.last = action{"terminal", args}
	return tea.ExecProcess(m.terminalCmd(args...), func(err error) tea.Msg { return termDone{title, err} })
}

type termDone struct {
	title string
	err   error
}

// editor: $VISUAL, $EDITOR, then nano, then vi.
func editor(file string) *exec.Cmd {
	for _, e := range []string{os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if f := strings.Fields(e); len(f) > 0 {
			return exec.Command(f[0], append(f[1:], file)...)
		}
	}
	for _, e := range []string{"nano", "vi"} {
		if p, err := exec.LookPath(e); err == nil {
			return exec.Command(p, file)
		}
	}
	return exec.Command("vi", file)
}

type edited struct {
	name, what string
	err        error
}

func (m *model) edit(p *proj, what string) tea.Cmd {
	f := filepath.Join(p.p.Base, what)
	m.last = action{"edit", []string{f}}
	return tea.ExecProcess(editor(f), func(err error) tea.Msg { return edited{p.p.Name, what, err} })
}

func (m *model) loadService() {
	p := m.cur()
	m.svc = nil
	if p == nil {
		return
	}
	out, _ := exec.Command(journalctl(), "--user", "-u", p.p.Unit(), "-n", "300", "--no-pager", "-o", "cat").Output()
	if s := strings.TrimRight(string(out), "\n"); s != "" {
		m.svc = strings.Split(s, "\n")
	}
}

func (m *model) startInput(kind, placeholder, value string) tea.Cmd {
	in := textinput.New()
	in.Prompt = map[string]string{"filter": "/", "env": "env "}[kind]
	in.Placeholder = placeholder
	in.SetValue(value)
	in.SetWidth(max(m.w-30, 20))
	m.input, m.inputFor = &in, kind
	return in.Focus()
}

// Update handles msg, then keeps the tab and the view it runs in step with the selection.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var c tea.Cmd
	if vc, ok := m.viewMsg(msg); ok {
		c = vc
	} else {
		_, c = m.handle(msg)
	}
	m.syncTabs()
	if rc := m.syncRun(); rc != nil {
		return m, tea.Batch(c, rc)
	}
	return m, c
}

func (m *model) handle(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tick:
		m.refresh()
		return m, every()
	case tea.BackgroundColorMsg:
		m.st = ui.Palette(msg.IsDark())
		if m.form != nil {
			m.form.st = m.st
		}
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case spinner.TickMsg:
		if m.job != nil && m.job.running {
			var c tea.Cmd
			m.spin, c = m.spin.Update(msg)
			return m, c
		}
	case jobLine:
		if m.job != nil && msg.id == m.job.id {
			m.job.lines = append(m.job.lines, msg.line)
			if len(m.job.lines) > 5000 {
				m.job.lines = m.job.lines[1000:]
			}
			return m, wait(m.job.ch)
		}
	case jobDone:
		if m.job != nil && msg.id == m.job.id {
			m.job.running, m.job.err = false, msg.err
			if msg.err != nil {
				m.job.lines = append(m.job.lines, "("+msg.err.Error()+")")
				m.say(m.job.title+" failed: "+msg.err.Error(), true)
			} else {
				m.say(m.job.title+": done", false)
			}
			m.refresh()
		}
	case termDone:
		if msg.err != nil {
			m.say(msg.title+": "+msg.err.Error(), true)
		} else {
			m.say(msg.title+": done", false)
		}
		m.refresh()
	case edited:
		switch {
		case msg.err != nil:
			m.say("editor: "+msg.err.Error(), true)
		case msg.what == "env":
			m.say(msg.name+" env saved: it applies from the next job (running ones keep what they started with)", false)
		default:
			m.say(msg.name+" config saved: it applies from the next start (x, then s); disk settings at once", false)
		}
		m.refresh()
	case tea.MouseClickMsg:
		m.click(msg.X, msg.Y)
	case tea.MouseWheelMsg:
		m.wheel(msg.X, msg.Y, msg.Button == tea.MouseWheelUp)
	case tea.KeyPressMsg:
		return m.key(msg)
	}
	if m.form != nil {
		return m, m.form.update(msg)
	}
	if m.input != nil {
		in, c := m.input.Update(msg)
		m.input = &in
		return m, c
	}
	return m, nil
}

func (m *model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if k == "y" || k == "Y" {
			return m, c.yes()
		}
		m.say("nothing was done", false)
		return m, nil
	}
	if m.form != nil {
		return m, m.formKey(msg)
	}
	if m.input != nil {
		return m, m.inputKey(msg)
	}
	if m.help {
		switch k {
		case "?", "esc", "q":
			m.help = false
		case "j", "down":
			m.helpTop++
		case "k", "up":
			m.helpTop = max(m.helpTop-1, 0)
		}
		return m, nil
	}
	switch k {
	case "q", "ctrl+c":
		m.stopRun()
		return m, tea.Quit
	case "?":
		m.help, m.helpTop = true, 0
	case "tab":
		m.focus = m.nextPane(m.focus, 1)
	case "shift+tab":
		m.focus = m.nextPane(m.focus, panes-1)
	case "1", "2", "3", "4":
		if p := pane(k[0] - '1'); p == paneDetails && m.detailsHidden() {
			m.say("Details is hidden: the terminal is too small for it", false)
		} else {
			m.focus = p
		}
	case "esc":
		m.filter[m.focus] = ""
		m.clamp()
	case "/":
		return m, m.startInput("filter", "filter "+strings.ToLower(m.paneTitle(m.focus)), m.filter[m.focus])
	case "r":
		m.refresh()
		m.say("refreshed", false)
		if m.vr != nil {
			return m, m.restartRun()
		}
	case "[":
		m.setTab(m.tab - 1)
	case "]":
		m.setTab(m.tab + 1)
	case "w":
		m.nextView()
	case "!":
		m.setTab(tabAlerts)
	case "L":
		m.setTab(tabService)
	case "o":
		m.setTab(tabOutput)
	case "a":
		m.form = newSetupForm(m.st)
		return m, m.form.focus()
	case "S":
		return m, m.terminal("self-update", "self-update")
	case "M":
		if m.legacy {
			return m, m.terminal("migrate", "migrate")
		}
		m.say("no node set up by the bash scripts here: nothing to migrate", false)
	case "ctrl+u":
		return m, m.terminal("uninstall everything", "uninstall")
	case "up", "k", "down", "j", "g", "G", "home", "end", "pgup", "pgdown":
		m.move(k)
	case "enter":
		switch m.focus {
		case paneProjects:
			m.focus = paneLanes
		case paneLanes, paneDetails:
			m.focus = paneMain
			m.setTab(tabLog)
		}
	default:
		p := m.cur()
		if p == nil {
			if len(k) == 1 && strings.Contains("sxdAcitueEvUl", k) {
				m.say("no project selected: a adds one", true)
			}
			return m, nil
		}
		return m, m.projectKey(p, k)
	}
	return m, nil
}

func (m *model) inputKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		if m.inputFor == "filter" {
			m.filter[m.focus] = ""
			m.clamp()
		}
		m.input = nil
	case "enter":
		v := strings.TrimSpace(m.input.Value())
		m.input = nil
		if m.inputFor == "filter" {
			m.filter[m.focus] = v
			m.clamp()
			return nil
		}
		if p := m.cur(); p != nil && v != "" {
			return m.stream("env "+v, p.p.Name, "env", v)
		}
	default:
		in, c := m.input.Update(msg)
		m.input = &in
		if m.inputFor == "filter" {
			m.filter[m.focus] = in.Value()
			if m.focus != paneMain {
				m.sel[m.focus] = 0
			}
			m.scroll = 0
			m.clamp()
		}
		return c
	}
	return nil
}

// projectKey: the actions on the selected project.
func (m *model) projectKey(p *proj, k string) tea.Cmd {
	n := p.p.Name
	switch k {
	case "s":
		return m.stream("start "+n, n, "start")
	case "d":
		return m.stream("drain "+n, n, "drain")
	case "x":
		m.confirm = &confirm{fmt.Sprintf("Stop %s now? Its running jobs go back to the hub. [y/N]", n), func() tea.Cmd { return m.stream("stop "+n, n, "stop") }}
	case "A":
		if p.enabled == "enabled" {
			return m.stream("autostart off "+n, n, "autostart", "off")
		}
		return m.stream("autostart on "+n, n, "autostart", "on")
	case "c":
		return m.stream("check "+n, n, "check")
	case "t":
		return m.stream("selftest "+n, n, "selftest")
	case "u":
		return m.stream("update "+n, n, "update")
	case "i":
		return m.terminal("install for "+n, n, "install")
	case "U":
		return m.terminal("uninstall "+n, "uninstall", n)
	case "e":
		return m.edit(p, "env")
	case "E":
		return m.edit(p, "config")
	case "v":
		return m.startInput("env", "KEY=VALUE to set, -KEY to unset", "")
	case "l":
		m.setTab(tabLog)
		m.focus = paneMain
	}
	return nil
}

// detailsHidden: the last drawing had no room for Details.
func (m *model) detailsHidden() bool { return m.lay.lw > 0 && !m.lay.details }

// nextPane: the pane step panes on from p, past a hidden Details.
func (m *model) nextPane(p pane, step pane) pane {
	p = (p + step) % panes
	if p == paneDetails && m.detailsHidden() {
		p = (p + step) % panes
	}
	return p
}

func (m *model) move(k string) {
	if m.focus == paneDetails {
		page := max(m.lay.dh-3, 1)
		switch k {
		case "up", "k":
			m.dtop--
		case "down", "j":
			m.dtop++
		case "pgup":
			m.dtop -= page
		case "pgdown":
			m.dtop += page
		case "g", "home":
			m.dtop = 0
		case "G", "end":
			m.dtop = 1 << 30
		}
		m.dtop = max(m.dtop, 0) // and at most the end, when drawn
		return
	}
	if m.focus == paneMain {
		page := max(m.geo.mainH-2, 1)
		switch k {
		case "up", "k":
			m.scroll++
		case "down", "j":
			m.scroll--
		case "pgup":
			m.scroll += page
		case "pgdown":
			m.scroll -= page
		case "g", "home":
			m.scroll = 1 << 30
		case "G", "end":
			m.scroll = 0
		}
		m.scroll = max(m.scroll, 0)
		return
	}
	n := len(m.shown())
	if m.focus == paneLanes {
		n = len(m.lanes())
	}
	s := &m.sel[m.focus]
	switch k {
	case "up", "k":
		*s--
	case "down", "j":
		*s++
	case "g", "home", "pgup":
		*s = 0
	case "G", "end", "pgdown":
		*s = n - 1
	}
	*s = min(max(*s, 0), max(n-1, 0))
	if m.focus == paneProjects {
		m.sel[paneLanes], m.scroll = 0, 0
		if m.tab == tabService {
			m.loadService()
		}
	}
	if m.focus == paneLanes {
		m.scroll = 0
	}
}

func (m *model) paneTitle(p pane) string {
	if p == paneMain {
		return m.curTab().name
	}
	return paneNames[p]
}
