package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

// Alert: a condition someone should know about, raised by the agent while it holds and cleared
// when it ends. ID names the condition (one alert per ID); Since is when it was first seen.
type Alert struct {
	ID       string    `json:"id"`
	Level    string    `json:"level"` // info, warn, error
	Since    time.Time `json:"since"`
	Text     string    `json:"text"`
	Notified bool      `json:"notified,omitempty"` // a desktop notification went out for it
}

const (
	Info  = "info"
	Warn  = "warn"
	Error = "error"
)

// HubDownAfter: the hub is unreachable for this long before it is an alert.
const HubDownAfter = 5 * time.Minute

// Restarts: this many starts within RestartWindow is an alert.
const (
	Restarts      = 3
	RestartWindow = 15 * time.Minute
)

// Facts: what the alerts are decided from.
type Facts struct {
	Now      time.Time
	Lanes    []Lane
	Activity map[int]time.Time // per lane: the last change to its log, progress or other state/slotN.* file
	StallMin int               // STALL_MIN (0: off)
	WarnMin  int               // JOB_WARN_MIN (0: off)
	HubDown  time.Time         // when the hub stopped answering (zero: it answers)
	HubErr   string
	Protocol string // why not claiming, when the hub's protocol is not ours ("": fine)
	DiskHeld string // why the disk holds work back ("": it doesn't)
	Missing  string // requirements missing ("": none)
	Starts   int    // starts within RestartWindow
	Update   string // a newer release
}

// Evaluate: the alerts that hold now. One already in prev keeps its Since (and Notified).
func Evaluate(f Facts, prev []Alert) []Alert {
	var now []Alert
	add := func(id, level, text string) { now = append(now, Alert{ID: id, Level: level, Since: f.Now, Text: text}) }
	for _, l := range f.Lanes {
		if l.ID == "" {
			continue
		}
		last := l.Started
		if a := f.Activity[l.Slot]; a.After(last) {
			last = a
		}
		if f.StallMin > 0 && !last.IsZero() && f.Now.Sub(last) >= time.Duration(f.StallMin)*time.Minute {
			add(fmt.Sprintf("stall:%d", l.Slot), Warn, fmt.Sprintf("lane %d stalled: %s has shown nothing new for %s", l.Slot, l.ID, ui.Dur(f.Now.Sub(last))))
		}
		if f.WarnMin > 0 && !l.Started.IsZero() && f.Now.Sub(l.Started) >= time.Duration(f.WarnMin)*time.Minute {
			add(fmt.Sprintf("long:%d", l.Slot), Warn, fmt.Sprintf("lane %d: %s has run for %s (JOB_WARN_MIN is %d)", l.Slot, l.ID, ui.Dur(f.Now.Sub(l.Started)), f.WarnMin))
		}
	}
	if !f.HubDown.IsZero() && f.Now.Sub(f.HubDown) >= HubDownAfter {
		t := "the hub has not answered for " + ui.Dur(f.Now.Sub(f.HubDown))
		if f.HubErr != "" {
			t += ": " + f.HubErr
		}
		add("hub", Error, t)
	}
	if f.Protocol != "" {
		add("protocol", Error, f.Protocol)
	}
	if f.DiskHeld != "" {
		add("disk", Warn, "not taking work: "+f.DiskHeld)
	}
	if f.Missing != "" {
		add("requirements", Error, f.Missing)
	}
	if f.Starts >= Restarts {
		add("restarts", Error, fmt.Sprintf("the service started %d times in %s", f.Starts, ui.Dur(RestartWindow)))
	}
	if f.Update != "" {
		add("update", Info, "sat "+f.Update+" is available: sat self-update")
	}
	old := map[string]Alert{}
	for _, a := range prev {
		old[a.ID] = a
	}
	for i := range now {
		if o, ok := old[now[i].ID]; ok {
			now[i].Since, now[i].Notified = o.Since, o.Notified
		}
	}
	return now
}

// Active: the alerts to show: all of a running agent's; of one that is not running, only those
// that say why it stopped.
func (s *State) Active() []Alert {
	if s == nil {
		return nil
	}
	if s.Live() {
		return s.Alerts
	}
	var r []Alert
	for _, a := range s.Alerts {
		if a.ID == "requirements" || a.ID == "restarts" {
			r = append(r, a)
		}
	}
	return r
}

// ServiceAlert: the alert for a service systemd reports failed (the agent can't say so itself).
func ServiceAlert(p *conf.Project, active string) *Alert {
	if active != "failed" {
		return nil
	}
	return &Alert{ID: "service", Level: Error, Text: p.Unit() + " failed: sat " + p.Name + " status, sat " + p.Name + " logs"}
}

// Rank orders levels: error first.
func Rank(level string) int {
	switch level {
	case Error:
		return 0
	case Warn:
		return 1
	}
	return 2
}

// Worst: the highest level among as ("" for none).
func Worst(as []Alert) string {
	w := ""
	for _, a := range as {
		if w == "" || Rank(a.Level) < Rank(w) {
			w = a.Level
		}
	}
	return w
}

// Sorted: errors first, then the oldest.
func Sorted(as []Alert) []Alert {
	r := append([]Alert(nil), as...)
	sort.SliceStable(r, func(i, j int) bool {
		if Rank(r[i].Level) != Rank(r[j].Level) {
			return Rank(r[i].Level) < Rank(r[j].Level)
		}
		return r[i].Since.Before(r[j].Since)
	})
	return r
}

// activity: per busy lane, the newest change to its files in state/ (slotN.log, slotN.progress,
// or anything else a job keeps there as slotN.*, such as a transcript).
func activity(p *conf.Project, lanes []Lane) map[int]time.Time {
	m := map[int]time.Time{}
	for _, l := range lanes {
		if l.ID == "" {
			continue
		}
		fs, _ := filepath.Glob(filepath.Join(p.State, fmt.Sprintf("slot%d.*", l.Slot)))
		for _, f := range fs {
			b := filepath.Base(f)
			if strings.HasSuffix(b, ".claim") || strings.HasSuffix(b, ".result") {
				continue
			}
			if fi, err := os.Stat(f); err == nil && fi.ModTime().After(m[l.Slot]) {
				m[l.Slot] = fi.ModTime()
			}
		}
	}
	return m
}

// countStarts records a start in state/starts and says how many fell within RestartWindow.
func countStarts(p *conf.Project, now time.Time) int {
	path := filepath.Join(p.State, "starts")
	b, _ := os.ReadFile(path)
	var keep []string
	n := 0
	for _, l := range strings.Fields(string(b)) {
		if s, err := strconv.ParseInt(l, 10, 64); err == nil && now.Sub(time.Unix(s, 0)) < RestartWindow {
			keep = append(keep, l)
			n++
		}
	}
	keep = append(keep, strconv.FormatInt(now.Unix(), 10))
	conf.WriteAtomic(path, []byte(strings.Join(keep, "\n")+"\n"), 0o644)
	return n + 1
}

func minutes(m map[string]string, k string) int {
	n, err := strconv.Atoi(strings.TrimSpace(m[k]))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// Notifier sends desktop notifications with notify-send, when NOTIFY=on in ~/.sat/config.
type Notifier struct {
	Send func(level, title, text string) error // nil: notify-send
}

// bus: the session bus address for notify-send, as a user service may lack it.
func bus() string {
	if a := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" {
		return a
	}
	if s := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "bus"); exists(s) {
		return "unix:path=" + s
	}
	return ""
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// NotifySend: one notification through notify-send; an error when there is none, or no bus.
func NotifySend(level, title, text string) error {
	ns, err := exec.LookPath("notify-send")
	if err != nil {
		return err
	}
	b := bus()
	if b == "" {
		return fmt.Errorf("no D-Bus session bus to notify on")
	}
	urg := map[string]string{Error: "critical", Warn: "normal", Info: "low"}[level]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ns, "-a", "satellite", "-u", urg, title, text)
	cmd.Env = append(os.Environ(), "DBUS_SESSION_BUS_ADDRESS="+b)
	return cmd.Run()
}

// notify sends one notification per alert not yet notified, and marks them. Off unless NOTIFY=on.
func (n *Notifier) notify(project string, as []Alert, on bool, log func(string, ...any)) {
	if !on {
		return
	}
	send := n.Send
	if send == nil {
		send = NotifySend
	}
	for i := range as {
		if as[i].Notified {
			continue
		}
		as[i].Notified = true
		go func(a Alert) { // never hold the agent up
			if err := send(a.Level, "sat "+project, a.Text); err != nil && log != nil {
				log("desktop notification: %v", err)
			}
		}(as[i])
	}
}
