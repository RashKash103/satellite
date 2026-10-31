// Package tui is `sat` with no arguments: a live view of this node. It reads what each agent
// writes (state/agent.json, the lane logs) and asks systemctl for the services; it opens no
// connection of its own. Start, drain and stop run `sat PROJECT start|drain|stop`.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/plan"
	"github.com/RashKash103/satellite/internal/selfupdate"
)

// Dur: 45s, 3m12s, 2h05m.
func Dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

type keys struct{ Up, Down, Start, Drain, Stop, Logs, Quit key.Binding }

func (k keys) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Start, k.Drain, k.Stop, k.Logs, k.Quit}
}
func (k keys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

var km = keys{
	Up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/↓", "lane")),
	Down:  key.NewBinding(key.WithKeys("down", "j")),
	Start: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "start")),
	Drain: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "drain")),
	Stop:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
	Logs:  key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs")),
	Quit:  key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
}

var (
	bold  = lipgloss.NewStyle().Bold(true)
	dim   = lipgloss.NewStyle().Faint(true)
	good  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	bad   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	warn  = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	selSt = lipgloss.NewStyle().Reverse(true)
	rule  = lipgloss.NewStyle().Faint(true)
)

type proj struct {
	p               *conf.Project
	st              *agent.State
	active, enabled string
	drain           bool
	slots           int
	disk            agent.Disk
	diskAt          time.Time
}

type model struct {
	version, exe string
	ps           []*proj
	sel          int // lane, counted across projects
	logs         bool
	confirm      string // project waiting for a y to stop
	msg          string
	w, h         int
	update       string
	help         help.Model
	bar          progress.Model
}

type tick time.Time
type done struct{ out string }

func Run(version, exe string) int {
	m := &model{version: version, exe: exe, logs: true, help: help.New(), bar: progress.New(progress.WithWidth(16), progress.WithoutPercentage())}
	m.refresh()
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func every() tea.Cmd { return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tick(t) }) }

func (m *model) Init() tea.Cmd { return every() }

func sysctl(args ...string) string {
	out, _ := exec.Command(plan.Systemctl(), append([]string{"--user"}, args...)...).Output()
	return strings.TrimSpace(string(out))
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
			x.disk, x.diskAt = o.disk, o.diskAt
		}
		x.st, _ = agent.ReadState(p)
		x.active, x.enabled = sysctl("is-active", p.Unit()), sysctl("is-enabled", p.Unit())
		_, err = os.Stat(filepath.Join(p.State, "drain"))
		x.drain = err == nil
		fmt.Sscan(p.Get("SLOTS", "1"), &x.slots)
		x.slots = max(x.slots, 1)
		if x.st.Live() {
			x.slots = len(x.st.Lanes)
		}
		if time.Since(x.diskAt) > 30*time.Second {
			x.disk, x.diskAt = agent.Measure(p, true), time.Now() // the size is cached for 30 min
		}
		m.ps = append(m.ps, x)
	}
	m.update = selfupdate.Cached(m.version)
	m.sel = min(m.sel, max(m.lanes()-1, 0))
}

func (m *model) lanes() int {
	n := 0
	for _, p := range m.ps {
		n += p.slots
	}
	return n
}

// at: the project and lane (from 1) of the selection.
func (m *model) at() (*proj, int) {
	i := m.sel
	for _, p := range m.ps {
		if i < p.slots {
			return p, i + 1
		}
		i -= p.slots
	}
	return nil, 0
}

func (m *model) do(p *proj, cmd string) tea.Cmd {
	return func() tea.Msg {
		out, err := exec.Command(m.exe, p.p.Name, cmd).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil && s == "" {
			s = err.Error()
		}
		return done{s}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tick:
		m.refresh()
		return m, every()
	case done:
		m.msg = msg.out
		m.refresh()
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.help.SetWidth(msg.Width)
	case tea.KeyPressMsg:
		p, _ := m.at()
		if m.confirm != "" {
			if msg.String() == "y" && p != nil {
				m.msg = "stopping " + m.confirm + "..."
				m.confirm = ""
				return m, m.do(p, "stop")
			}
			m.confirm, m.msg = "", "not stopped"
			return m, nil
		}
		switch {
		case key.Matches(msg, km.Quit):
			return m, tea.Quit
		case key.Matches(msg, km.Up):
			m.sel = max(m.sel-1, 0)
		case key.Matches(msg, km.Down):
			m.sel = min(m.sel+1, max(m.lanes()-1, 0))
		case key.Matches(msg, km.Logs):
			m.logs = !m.logs
		case p == nil:
		case key.Matches(msg, km.Start):
			m.msg = "starting " + p.p.Name + "..."
			return m, m.do(p, "start")
		case key.Matches(msg, km.Drain):
			m.msg = "draining " + p.p.Name + "..."
			return m, m.do(p, "drain")
		case key.Matches(msg, km.Stop):
			m.confirm = p.p.Name
			m.msg = fmt.Sprintf("Stop %s now? Its running jobs go back to the hub. [y/N]", p.p.Name)
		}
	}
	return m, nil
}

func diskLine(d agent.Disk) string {
	if d.OK(1) {
		return dim.Render("disk " + d.Why())
	}
	return warn.Render("disk " + d.Why() + ": not taking work")
}

func (m *model) service(p *proj) string {
	s := p.active
	switch s {
	case "active":
		s = good.Render(s)
	case "failed":
		s = bad.Render(s)
	default:
		s = dim.Render(s)
	}
	s += dim.Render(" · autostart " + p.enabled)
	if p.drain {
		s += " · " + warn.Render("draining")
	}
	return s
}

func (m *model) hubLine(p *proj) string {
	h := p.p.Conf["HUB"]
	st := p.st
	if !st.Live() {
		if st != nil && st.Note != "" {
			return "hub " + h + "  " + warn.Render(st.Note)
		}
		return "hub " + h + "  " + dim.Render("(not running: the agent checks the hub)")
	}
	var s string
	switch {
	case !st.HubOK:
		s = bad.Render("unreachable") + dim.Render(" · last contact "+ago(st.LastContact))
	case !st.ProtocolOK:
		s = warn.Render(st.Note)
	default:
		s = good.Render("reachable") + dim.Render(" · protocol "+st.Protocol+" · last contact "+ago(st.LastContact))
	}
	return "hub " + h + "  " + s
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return Dur(time.Since(t)) + " ago"
}

func (m *model) laneLine(p *proj, slot int, selected bool) string {
	var l agent.Lane
	if p.st.Live() && slot <= len(p.st.Lanes) {
		l = p.st.Lanes[slot-1]
	}
	s := fmt.Sprintf("lane %d  ", slot)
	switch {
	case !p.st.Live():
		s += dim.Render("-")
	case l.ID == "" && l.Waiting == "hub":
		s += warn.Render("waiting for the hub")
	case l.ID == "" && l.Waiting == "disk":
		s += warn.Render("waiting for disk space")
	case l.ID == "" && l.Waiting == "ready":
		s += dim.Render("held back by sat/ready")
	case l.ID == "" && p.drain:
		s += dim.Render("idle (draining)")
	case l.ID == "":
		s += dim.Render("idle: waiting for work")
	default:
		s += bold.Render(l.ID) + "  " + Dur(time.Since(l.Started)) + dim.Render("  renewed "+ago(l.Renewed))
		if l.Percent >= 0 {
			s += "  " + m.bar.ViewAs(float64(l.Percent)/100) + fmt.Sprintf(" %3d%%", l.Percent)
		}
		if l.Progress != "" {
			s += "  " + l.Progress
		}
	}
	if selected {
		return selSt.Render(">") + " " + s
	}
	return "  " + s
}

func (m *model) View() tea.View {
	var b strings.Builder
	b.WriteString(bold.Render("satellite") + dim.Render(" "+m.version+" · node "+conf.ShortHost()))
	if m.update != "" {
		b.WriteString("  " + warn.Render(m.update+" available: sat self-update"))
	}
	b.WriteString("\n\n")
	if len(m.ps) == 0 {
		b.WriteString("No projects on this node yet: sat setup HUB_IP PROJECT (docs/node.md).\n")
	}
	selP, selSlot := m.at()
	for _, p := range m.ps {
		fmt.Fprintf(&b, "%s  %s\n", bold.Render(p.p.Name), m.service(p))
		b.WriteString("  " + m.hubLine(p) + "\n")
		b.WriteString("  " + diskLine(p.disk) + "\n")
		for s := 1; s <= p.slots; s++ {
			b.WriteString("  " + m.laneLine(p, s, p == selP && s == selSlot) + "\n")
		}
		b.WriteString("\n")
	}
	if m.logs && selP != nil {
		lf := filepath.Join(selP.p.State, fmt.Sprintf("slot%d.log", selSlot))
		n := 12
		if m.h > 0 {
			n = max(m.h-strings.Count(b.String(), "\n")-5, 3)
		}
		w := m.w
		if w <= 0 {
			w = 100
		}
		b.WriteString(rule.Render(fmt.Sprintf("── %s lane %d: %s ", selP.p.Name, selSlot, filepath.Base(lf))+strings.Repeat("─", max(w-30-len(selP.p.Name), 0))) + "\n")
		tl := agent.Tail(lf, n)
		if len(tl) == 0 {
			b.WriteString(dim.Render("(no output yet)") + "\n")
		}
		for _, l := range tl {
			if len(l) > w && w > 1 {
				l = l[:w-1] + "…"
			}
			b.WriteString(l + "\n")
		}
		b.WriteString("\n")
	}
	if m.msg != "" {
		b.WriteString(m.msg + "\n")
	}
	b.WriteString(m.help.View(km))
	v := tea.NewView(b.String())
	v.AltScreen = true
	v.WindowTitle = "satellite"
	return v
}
