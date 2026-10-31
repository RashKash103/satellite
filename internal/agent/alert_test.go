package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
)

func ids(as []Alert) string {
	var s []string
	for _, a := range as {
		s = append(s, a.ID)
	}
	return strings.Join(s, " ")
}

// Each condition raises its alert while it holds and clears when it ends; Since stays put.
func TestAlertsRaiseAndClear(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	lane := Lane{Slot: 1, ID: "j1", Started: t0}
	base := Facts{Lanes: []Lane{lane, {Slot: 2}}, StallMin: 30}
	for _, c := range []struct {
		id   string
		on   func(f *Facts, at time.Time)
		off  func(f *Facts)
		lvl  string
		text string
	}{
		{"stall:1", func(f *Facts, at time.Time) { f.Now = at.Add(31 * time.Minute) }, func(f *Facts) { f.Activity = map[int]time.Time{1: f.Now.Add(-time.Minute)} }, Warn, "lane 1 stalled: j1 has shown nothing new for 31m00s"},
		{"long:1", func(f *Facts, at time.Time) { f.WarnMin, f.StallMin, f.Now = 60, 0, at.Add(61*time.Minute) }, func(f *Facts) { f.Lanes = nil }, Warn, "lane 1: j1 has run for 1h01m (JOB_WARN_MIN is 60)"},
		{"hub", func(f *Facts, at time.Time) { f.HubDown, f.HubErr, f.Now = at, "no route", at.Add(6*time.Minute) }, func(f *Facts) { f.HubDown = time.Time{} }, Error, "the hub has not answered for 6m00s: no route"},
		{"protocol", func(f *Facts, _ time.Time) { f.Protocol = "not claiming: the hub speaks protocol 2" }, func(f *Facts) { f.Protocol = "" }, Error, "not claiming: the hub speaks protocol 2"},
		{"disk", func(f *Facts, _ time.Time) { f.DiskHeld = "3 GB free (keeps 20 GB free)" }, func(f *Facts) { f.DiskHeld = "" }, Warn, "not taking work: 3 GB free (keeps 20 GB free)"},
		{"requirements", func(f *Facts, _ time.Time) { f.Missing = "missing requirements" }, func(f *Facts) { f.Missing = "" }, Error, "missing requirements"},
		{"restarts", func(f *Facts, _ time.Time) { f.Starts = 3 }, func(f *Facts) { f.Starts = 1 }, Error, "the service started 3 times in 15m00s"},
		{"update", func(f *Facts, _ time.Time) { f.Update = "v9.0.0" }, func(f *Facts) { f.Update = "" }, Info, "sat v9.0.0 is available: sat self-update"},
	} {
		f := base
		f.Now = t0.Add(time.Minute)
		if a := Evaluate(f, nil); len(a) != 0 {
			t.Fatalf("%s: alerts before: %v", c.id, ids(a))
		}
		c.on(&f, t0)
		a := Evaluate(f, nil)
		if len(a) != 1 || a[0].ID != c.id || a[0].Level != c.lvl || a[0].Text != c.text || !a[0].Since.Equal(f.Now) {
			t.Errorf("%s: %+v", c.id, a)
			continue
		}
		f.Now = f.Now.Add(time.Minute)
		a2 := Evaluate(f, a)
		if len(a2) != 1 || !a2[0].Since.Equal(a[0].Since) {
			t.Errorf("%s: Since moved: %+v", c.id, a2)
		}
		c.off(&f)
		if a3 := Evaluate(f, a2); len(a3) != 0 {
			t.Errorf("%s: did not clear: %v", c.id, ids(a3))
		}
	}
	// Short of the thresholds, nothing.
	f := base
	f.Now, f.HubDown = t0.Add(29*time.Minute), t0.Add(25*time.Minute)
	if a := Evaluate(f, nil); len(a) != 0 {
		t.Errorf("early: %v", ids(a))
	}
	f.StallMin = 0
	f.Now = t0.Add(48 * time.Hour)
	f.HubDown = time.Time{}
	if a := Evaluate(f, nil); len(a) != 0 {
		t.Errorf("STALL_MIN=0: %v", ids(a))
	}
}

func TestNotifyOncePerAlert(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	n := Notifier{Send: func(level, title, text string) error {
		mu.Lock()
		sent = append(sent, level+" "+title+": "+text)
		mu.Unlock()
		return nil
	}}
	count := func() int { time.Sleep(50 * time.Millisecond); mu.Lock(); defer mu.Unlock(); return len(sent) }
	f := Facts{Now: time.Now(), DiskHeld: "low"}
	a := Evaluate(f, nil)
	n.notify("p", a, false, nil)
	if count() != 0 {
		t.Fatal("notified with NOTIFY off")
	}
	a = Evaluate(f, nil)
	n.notify("p", a, true, nil)
	for range 5 { // it stays: no more
		a = Evaluate(f, a)
		n.notify("p", a, true, nil)
	}
	if count() != 1 || sent[0] != "warn sat p: not taking work: low" {
		t.Fatalf("%q", sent)
	}
	f.Missing = "missing"
	a = Evaluate(f, a)
	n.notify("p", a, true, nil)
	if count() != 2 {
		t.Fatalf("a second alert: %q", sent)
	}
	f.DiskHeld = "" // cleared, then back: a new alert
	a = Evaluate(f, a)
	f.DiskHeld = "low"
	a = Evaluate(f, a)
	n.notify("p", a, true, nil)
	if count() != 3 {
		t.Fatalf("raised again: %q", sent)
	}
}

func TestNotifySend(t *testing.T) {
	d := t.TempDir()
	log := filepath.Join(d, "log")
	os.WriteFile(filepath.Join(d, "notify-send"), []byte("#!/bin/sh\necho \"$DBUS_SESSION_BUS_ADDRESS $*\" >> "+log+"\n"), 0o755)
	t.Setenv("PATH", d+":"+os.Getenv("PATH"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/tmp/fake")
	if err := NotifySend(Error, "sat p", "hub down"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); string(b) != "unix:path=/tmp/fake -a satellite -u critical sat p hub down\n" {
		t.Errorf("%q", b)
	}
	t.Setenv("PATH", "/nonexistent")
	if NotifySend(Warn, "x", "y") == nil {
		t.Error("no notify-send, no error")
	}
}

func TestStartsAndActivity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := conf.Dir("p")
	os.MkdirAll(filepath.Join(base, "state"), 0o755)
	p := &conf.Project{Name: "p", Base: base, State: filepath.Join(base, "state")}
	now := time.Now()
	os.WriteFile(filepath.Join(p.State, "starts"), []byte("1\n"), 0o644) // long ago: not counted
	for i := 1; i <= 3; i++ {
		if n := countStarts(p, now.Add(time.Duration(i)*time.Minute)); n != i {
			t.Errorf("start %d counted %d", i, n)
		}
	}
	if n := countStarts(p, now.Add(time.Hour)); n != 1 {
		t.Errorf("an hour later: %d", n)
	}
	old := now.Add(-time.Hour)
	for _, f := range []string{"slot1.log", "slot1.claim", "slot1.transcript"} {
		os.WriteFile(filepath.Join(p.State, f), nil, 0o644)
		os.Chtimes(filepath.Join(p.State, f), old, old)
	}
	fresh := now.Add(-time.Minute)
	os.Chtimes(filepath.Join(p.State, "slot1.transcript"), fresh, fresh)
	os.Chtimes(filepath.Join(p.State, "slot1.claim"), now, now) // not the job's doing
	a := activity(p, []Lane{{Slot: 1, ID: "j"}, {Slot: 2}})
	if !a[1].Equal(fresh) || len(a) != 1 {
		t.Errorf("%v", a)
	}
}

func TestActive(t *testing.T) {
	s := &State{Running: true, Updated: time.Now(), Alerts: []Alert{{ID: "disk"}, {ID: "requirements"}}}
	if len(s.Active()) != 2 {
		t.Error("live")
	}
	s.Running = false
	if a := s.Active(); len(a) != 1 || a[0].ID != "requirements" {
		t.Errorf("stopped: %v", ids(a))
	}
	as := Sorted([]Alert{{ID: "i", Level: Info}, {ID: "w", Level: Warn}, {ID: "e", Level: Error}})
	if ids(as) != "e w i" || Worst(as) != Error || Worst(nil) != "" {
		t.Error(ids(as))
	}
}
