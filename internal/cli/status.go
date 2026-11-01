package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/hub"
	"github.com/RashKash103/satellite/internal/selfupdate"
	"github.com/RashKash103/satellite/internal/ui"
)

func ago(t time.Time) string { return ui.Ago(t, time.Now()) }

// statusInfo: what `sat status` shows for one project, gathered once and rendered plain (the
// lines sat always printed) or styled.
type statusInfo struct {
	Name, State, Enabled string
	Drain                bool
	Hub, HubVersion      string
	HubErr               error
	NotClaiming          bool // reachable, but its protocol is not ours
	Live                 bool // the agent's state is current
	Note                 string
	Restart              string // the agent re-executed and adopted running jobs, within the hour
	RestartAt            time.Time
	Lanes                []agent.Lane
	Claims               [][2]string // lane, job: from the claim files when the agent isn't live
	Missing              int         // requirements missing (only looked at when not active)
	Alerts               []agent.Alert
	Disk                 agent.Disk
	Log                  []string
}

const (
	hubOK = iota
	hubNotClaiming
	hubDown
)

func (s *statusInfo) hubState() int {
	switch {
	case s.HubErr == nil:
		return hubOK
	case s.NotClaiming:
		return hubNotClaiming
	}
	return hubDown
}

func gather(t *ui.T, p *conf.Project) *statusInfo {
	u := p.Unit()
	s := &statusInfo{Name: p.Name, State: scOut("is-active", u), Enabled: scOut("is-enabled", u)}
	if s.State == "" {
		s.State = "unknown"
	}
	_, err := os.Stat(filepath.Join(p.State, "drain"))
	s.Drain = err == nil && s.State == "active" // a stopped project is not draining
	c := hub.For(p)
	s.Hub = c.Name()
	spin(t, "asking the hub "+s.Hub, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		s.HubVersion, s.HubErr = hub.CheckProtocol(ctx, c)
		if s.HubErr == nil {
			_, _, s.HubErr = hub.Out(ctx, c, p.Name, "status")
		}
		cancel()
		c.Close()
	})
	var pe hub.ProtocolError
	s.NotClaiming = errors.As(s.HubErr, &pe)
	s.Alerts = alertsOf(p, s.State)
	if st, err := agent.ReadState(p); err == nil && st.Live() {
		s.Live, s.Note, s.Lanes = true, st.Note, st.Lanes
		if st.Restart != "" && time.Since(st.RestartAt) < time.Hour {
			s.Restart, s.RestartAt = st.Restart, st.RestartAt
		}
	} else {
		fs, _ := filepath.Glob(filepath.Join(p.State, "slot*.claim"))
		for _, f := range fs {
			if b, _ := os.ReadFile(f); len(b) > 0 {
				s.Claims = append(s.Claims, [2]string{strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "slot"), ".claim"), strings.TrimSpace(string(b))})
			}
		}
	}
	if s.State != "active" {
		s.Missing = len(planOf(p))
	}
	s.Disk = agent.Measure(p, true)
	out, _ := exec.Command(journalctl(), "--user", "-u", u, "-n", "8", "--no-pager", "-o", "cat").Output()
	if l := strings.TrimRight(string(out), "\n"); l != "" {
		s.Log = strings.Split(l, "\n")
	}
	return s
}

// spin runs f behind a spinner on a terminal.
func spin(t *ui.T, msg string, f func()) { ui.Spin(t, msg, f) }

// renderPlain: the lines sat status has always printed.
func (s *statusInfo) renderPlain(t *ui.T, now time.Time) {
	dr := ""
	if s.Drain {
		dr = ", draining"
	}
	t.Printf("%s: %s (autostart: %s)%s\n", s.Name, s.State, s.Enabled, dr)
	switch s.hubState() {
	case hubOK:
		t.Printf("hub %s: reachable, protocol %s\n", s.Hub, s.HubVersion)
	case hubNotClaiming:
		t.Printf("hub %s: reachable, but not claiming: %v\n", s.Hub, s.HubErr)
	default:
		t.Printf("hub %s: UNREACHABLE (%v)\n", s.Hub, s.HubErr)
	}
	if s.Live {
		if s.Note != "" {
			t.Printf("agent: %s\n", s.Note)
		}
		if s.Restart != "" {
			t.Printf("agent: %s, %s\n", s.Restart, ago(s.RestartAt))
		}
		for _, l := range s.Lanes {
			if l.ID == "" {
				continue
			}
			pr := ""
			if l.Percent >= 0 {
				pr = fmt.Sprintf(", %d%%", l.Percent)
			}
			if l.Progress != "" {
				pr += " " + l.Progress
			}
			t.Printf("lane %d: %s for %s, renewed %s%s\n", l.Slot, l.ID, ui.Dur(now.Sub(l.Started)), ui.Ago(l.Renewed, now), pr)
		}
	}
	for _, c := range s.Claims {
		t.Printf("lane %s: %s\n", c[0], c[1])
	}
	if s.Missing > 0 {
		t.Printf("requirements: %d missing, so the service won't start (sat %s check)\n", s.Missing, s.Name)
	}
	for _, a := range s.Alerts {
		t.Printf("alert: %s: %s (since %s)\n", a.Level, a.Text, sinceWord(a.Since, now))
	}
	hold := ""
	if !s.Disk.OK(1) {
		hold = "  (not taking work: low on room)"
	}
	t.Printf("disk: %s%s\n", s.Disk.Why(), hold)
	for _, l := range s.Log {
		t.Println(l)
	}
}

// serviceWord: active -> running, inactive -> stopped.
func serviceWord(state string) string {
	switch state {
	case "active":
		return "running"
	case "inactive":
		return "stopped"
	}
	return state
}

func autostartWord(enabled string) string {
	switch enabled {
	case "enabled":
		return "on"
	case "disabled", "":
		return "off"
	}
	return enabled
}

// renderStyled: the same facts for a terminal: a heading per project, aligned rows, every lane.
func (s *statusInfo) renderStyled(t *ui.T, now time.Time) {
	head := t.Dot(s.State) + t.Bold(s.Name) + "  " + t.Badge(serviceWord(s.State)) +
		t.Muted(" · autostart ") + t.Badge(autostartWord(s.Enabled))
	if s.Drain {
		head += t.Muted(" · ") + t.Badge("draining")
	}
	t.Println(head)
	var rows [][]string
	switch s.hubState() {
	case hubOK:
		rows = append(rows, []string{t.Muted("hub"), s.Hub + "  " + t.Badge("reachable") + t.Muted(" · protocol "+s.HubVersion)})
	case hubNotClaiming:
		rows = append(rows, []string{t.Muted("hub"), s.Hub + "  " + t.Warn("reachable, but not claiming: "+s.HubErr.Error())})
	default:
		rows = append(rows, []string{t.Muted("hub"), s.Hub + "  " + t.Bad("unreachable") + t.Muted(" ("+s.HubErr.Error()+")")})
	}
	if s.Restart != "" {
		rows = append(rows, []string{t.Muted("agent"), s.Restart + t.Muted(", "+ago(s.RestartAt))})
	}
	if s.Note != "" {
		rows = append(rows, []string{t.Muted("agent"), t.Warn(s.Note)})
	}
	for _, a := range s.Alerts {
		rows = append(rows, []string{t.Muted("alert"), alertText(t, a, now)})
	}
	if s.Missing > 0 {
		rows = append(rows, []string{t.Muted("needs"), t.Warn(ui.Plural(s.Missing, "requirement", "requirements")+" missing, so the service won't start") + t.Muted(": sat "+s.Name+" check")})
	}
	d := s.Disk
	disk := fmt.Sprintf("%d GB free", d.Avail/(1<<30)) + t.Muted(fmt.Sprintf(" · keeps %d GB free", d.Need/(1<<30)))
	if d.Used > 0 {
		pr := " · project " + ui.Bytes(d.Used)
		if d.Cap > 0 {
			pr += " of " + ui.Bytes(d.Cap)
		}
		disk += t.Muted(pr)
	}
	if !d.OK(1) {
		disk += "  " + t.Warn("holding: not taking work, low on room")
	}
	rows = append(rows, []string{t.Muted("disk"), disk})
	lanes := s.laneRows(t, now)
	if len(lanes) == 0 && s.State != "active" {
		lanes = [][]string{{t.Muted("lanes"), t.Muted("none running")}}
	}
	t.Print(ui.Columns("  ", append(rows, lanes...)))
	if len(s.Log) > 0 {
		t.Println("  " + t.Muted("recent log"))
		for _, l := range s.Log {
			t.Println("    " + ui.Truncate(LogLine(t, l), t.Width()-4))
		}
	}
}

func (s *statusInfo) laneRows(t *ui.T, now time.Time) [][]string {
	var rows [][]string
	for _, l := range s.Lanes {
		k := t.Muted(fmt.Sprintf("lane %d", l.Slot))
		switch {
		case l.ID != "":
			v := t.Bold(l.ID) + "  " + ui.Dur(now.Sub(l.Started)) + t.Muted("  renewed "+ui.Ago(l.Renewed, now))
			if l.Percent >= 0 {
				if t.Width() == 0 || t.Width() >= 80 {
					v += "  " + t.Bar(l.Percent, 12)
				}
				v += fmt.Sprintf(" %3d%%", l.Percent)
			}
			if l.Progress != "" {
				v += "  " + l.Progress
			}
			rows = append(rows, []string{k, v})
		case l.Waiting == "hub":
			rows = append(rows, []string{k, t.Warn("waiting for the hub")})
		case l.Waiting == "disk":
			rows = append(rows, []string{k, t.Warn("waiting for disk space")})
		case l.Waiting == "ready":
			rows = append(rows, []string{k, t.Muted("held back by sat/ready")})
		case s.Drain:
			rows = append(rows, []string{k, t.Muted("idle (draining)")})
		default:
			rows = append(rows, []string{k, t.Muted("idle: waiting for work")})
		}
	}
	for _, c := range s.Claims {
		rows = append(rows, []string{t.Muted("lane " + c[0]), t.Bold(c[1])})
	}
	return rows
}

func (s *statusInfo) render(t *ui.T, now time.Time) {
	if t.Color() {
		s.renderStyled(t, now)
	} else {
		s.renderPlain(t, now)
	}
}

// overview: one row per project, before the details, when there are several (styled only).
func overview(t *ui.T, ss []*statusInfo) {
	var rows [][]string
	for _, s := range ss {
		busy := 0
		for _, l := range s.Lanes {
			if l.ID != "" {
				busy++
			}
		}
		busy += len(s.Claims)
		lanes := fmt.Sprintf("%d of %d busy", busy, max(len(s.Lanes), len(s.Claims)))
		if !s.Live && len(s.Claims) == 0 {
			lanes = "-"
		}
		hub := t.Badge("reachable")
		switch s.hubState() {
		case hubNotClaiming:
			hub = t.Warn("not claiming")
		case hubDown:
			hub = t.Bad("unreachable")
		}
		rows = append(rows, []string{t.Dot(s.State) + t.Bold(s.Name), t.Badge(serviceWord(s.State)), t.Badge(autostartWord(s.Enabled)), hub, lanes})
	}
	t.Print(t.Table("", []string{"  PROJECT", "SERVICE", "AUTOSTART", "HUB", "LANES"}, rows))
	t.Println()
}

func status(p *conf.Project) {
	t := ui.Out()
	gather(t, p).render(t, time.Now())
}

func statusAll() int {
	t := ui.Out()
	ps := conf.List()
	if len(ps) == 0 {
		t.Println("no projects on this node: sat setup HUB_IP PROJECT (docs/node.md)")
		return 0
	}
	var ss []*statusInfo
	for _, n := range ps {
		if p, err := conf.Load(n); err == nil {
			ss = append(ss, gather(t, p))
		}
	}
	if t.Color() && len(ss) > 1 {
		overview(t, ss)
	}
	now := time.Now()
	for i, s := range ss {
		if i > 0 {
			t.Println()
		}
		s.render(t, now)
	}
	updateNote()
	return 0
}

func updateNote() {
	tag := selfupdate.Cached(Version)
	if tag == "" {
		return
	}
	t := ui.Out()
	if t.Color() {
		t.Println("\n" + t.Warn("↑ sat "+tag+" is available") + t.Muted(" (you have "+Version+"): ") + t.Accent("sat self-update"))
		return
	}
	t.Printf("\nsat %s is available (you have %s): sat self-update\n", tag, Version)
}

var (
	logStamp = regexp.MustCompile(`^(\d\d-\d\d \d\d:\d\d:\d\d )(\[\d+\] )?`)
	logBad   = regexp.MustCompile(`\b(failed|cannot|could not|unreachable|lost|refus\w*|error|missing)\b`)
	logWarn  = regexp.MustCompile(`\b(handing it back|handed back|not taking work|low on disk|disk nearly full|drain\w*|restarting|waiting|stopped)\b`)
	logGood  = regexp.MustCompile(`\b(passed|claimed|claiming|starting|updated)\b`)
)

// LogLine colours one agent log line for a terminal: the time muted, the lane in the accent,
// the message by what it says. Plain, it is returned as it is.
func LogLine(t *ui.T, l string) string {
	if !t.Color() {
		return l
	}
	m := logStamp.FindStringSubmatch(l)
	pre, rest := "", l
	if m != nil {
		pre = t.Muted(m[1]) + t.Accent(m[2])
		rest = l[len(m[0]):]
	}
	switch {
	case logBad.MatchString(rest):
		rest = t.Bad(rest)
	case logWarn.MatchString(rest):
		rest = t.Warn(rest)
	case logGood.MatchString(rest):
		rest = t.OK(rest)
	}
	return pre + rest
}

// logs follows the service log; on a terminal the lines are coloured as they come.
func logs(t *ui.T, unit string) {
	j := exec.Command(journalctl(), "--user", "-u", unit, "-f", "-o", "cat")
	if !t.Color() {
		run(j)
		return
	}
	j.Stderr = os.Stderr
	r, err := j.StdoutPipe()
	if err != nil || j.Start() != nil {
		run(exec.Command(journalctl(), "--user", "-u", unit, "-f", "-o", "cat"))
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		t.Println(LogLine(t, sc.Text()))
	}
	j.Wait()
}
