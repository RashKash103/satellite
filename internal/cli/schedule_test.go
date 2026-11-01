package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

func TestScheduleCommand(t *testing.T) {
	testNode(t)
	p, _ := conf.Load("demo")
	run := func(a ...string) (string, int) {
		var rc int
		out := captureAll(func() { rc = Main("dev", append([]string{"sat", "demo", "schedule"}, a...)) })
		return out, rc
	}
	if out, rc := run(); rc != 0 || !strings.Contains(out, "no drain scheduled for demo") {
		t.Fatalf("%d %q", rc, out)
	}
	// three hours from now (the time of day the test runs at doesn't matter), drained 30 min before
	at := time.Now().Add(3 * time.Hour).Format("15:04")
	ct, _ := time.Parse("15:04", at)
	from := ct.Add(-30 * time.Minute).Format("15:04")
	out, rc := run("end", at, "daily")
	if rc != 0 || !strings.Contains(out, "demo: be stopped by "+at+", daily: drain at "+from) || !strings.Contains(out, "(done by "+at+", default 30m), daily") ||
		!strings.Contains(out, "taken as 30m (JOB_EST_MIN)") || !strings.Contains(out, "demo isn't running") {
		t.Fatalf("%d %q", rc, out)
	}
	s, err := agent.ReadSchedule(p)
	if err != nil || s == nil || s.Anchor != "end" || s.At != at || !s.Daily {
		t.Fatalf("%+v %v", s, err)
	}
	if out, _ := run(); !strings.Contains(out, "be stopped by "+at+", daily") {
		t.Errorf("show: %q", out)
	}
	// status shows it
	if out := capture(t, func() { Main("dev", []string{"sat", "demo", "status"}) }); !strings.Contains(out, "schedule: be stopped by "+at+", daily: drain at "+from) {
		t.Errorf("status: %q", out)
	}
	// once, the next 06:15
	out, rc = run("start", "6:15")
	s, _ = agent.ReadSchedule(p)
	if rc != 0 || s == nil || s.Daily || s.At != "06:15" || s.Date == "" || !strings.Contains(out, "start draining at 06:15 on "+s.Date) {
		t.Fatalf("%d %q %+v", rc, out, s)
	}
	for _, c := range [][]string{{"start"}, {"middle", "10:00"}, {"start", "10:61"}, {"start", "10:00", "2001-01-01"}, {"start", "10:00", "daily", "x"}} {
		if out, rc := run(c...); rc != 2 {
			t.Errorf("%v: %d %q", c, rc, out)
		}
	}
	if out, rc := run("off"); rc != 0 || !strings.Contains(out, "no drain scheduled for demo any more") || exists(agent.SchedulePath(p)) {
		t.Errorf("off: %d %q", rc, out)
	}
	if out, _ := run("off"); !strings.Contains(out, "no drain was scheduled") {
		t.Errorf("off again: %q", out)
	}
	// start keeps it: the schedule is not the drain flag
	run("start", "10:00", "daily")
	os.WriteFile(p.State+"/drain", nil, 0o644)
	start(p)
	if !exists(agent.SchedulePath(p)) {
		t.Error("start removed the schedule")
	}
}

func TestStatusSchedule(t *testing.T) {
	s := sample()
	loc := time.UTC
	n := time.Date(2026, 10, 1, 12, 0, 0, 0, loc)
	s.Plan = agent.PlanOf(&agent.Schedule{Anchor: "end", At: "19:00", Daily: true, Set: n.Add(-time.Hour)}, agent.Estimate{Dur: 20 * time.Minute, From: "p80", N: 9}, n)
	var b strings.Builder
	s.render(ui.Plain(&b), n)
	if !strings.Contains(b.String(), "schedule: be stopped by 19:00, daily: drain at 18:40 today (done by 19:00, p80 20m), daily\n") {
		t.Errorf("%s", b.String())
	}
	b.Reset()
	s.render(ui.Styled(&b, true, 120), n)
	if !strings.Contains(ui.Strip(b.String()), "drain   be stopped by 19:00, daily: drain at 18:40 today") {
		t.Errorf("%s", ui.Strip(b.String()))
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
