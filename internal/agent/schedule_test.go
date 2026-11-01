package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the DST tests' zone, wherever they run

	"github.com/RashKash103/satellite/internal/conf"
)

func zone(t *testing.T, name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParseClock(t *testing.T) {
	for _, c := range []struct {
		in   string
		h, m int
		ok   bool
	}{{"18:40", 18, 40, true}, {"00:00", 0, 0, true}, {"23:59", 23, 59, true}, {"9:05", 9, 5, true},
		{"24:00", 0, 0, false}, {"12:60", 0, 0, false}, {"1840", 0, 0, false}, {"", 0, 0, false}, {"6pm", 0, 0, false}} {
		h, m, err := ParseClock(c.in)
		if (err == nil) != c.ok || c.ok && (h != c.h || m != c.m) {
			t.Errorf("%q: %d %d %v", c.in, h, m, err)
		}
	}
}

func TestNewSchedule(t *testing.T) {
	loc := zone(t, "Europe/Berlin")
	now := time.Date(2026, 11, 2, 18, 0, 0, 0, loc)
	// once, no date: the next time the clock reads it
	s, err := NewSchedule(AnchorStart, "9:05", false, "", now)
	if err != nil || s.Date != "2026-11-03" || s.At != "09:05" || s.Daily || !s.Set.Equal(now) {
		t.Errorf("once tomorrow: %+v %v", s, err)
	}
	if s, err := NewSchedule(AnchorEnd, "19:00", false, "", now); err != nil || s.Date != "2026-11-02" {
		t.Errorf("once today: %+v %v", s, err)
	}
	if s, err := NewSchedule(AnchorEnd, "18:00", false, "", now); err != nil || s.Date != "2026-11-03" {
		t.Errorf("now exactly: tomorrow: %+v %v", s, err)
	}
	if s, err := NewSchedule(AnchorStart, "07:00", true, "", now); err != nil || s.Date != "" || !s.Daily {
		t.Errorf("daily: %+v %v", s, err)
	}
	for _, c := range []struct {
		anchor, at, date string
		daily            bool
		why              string
	}{
		{"start", "17:00", "2026-11-02", false, "has passed"},
		{"start", "10:00", "2026-10-01", false, "has passed"},
		{"middle", "10:00", "", false, "start (drain from the time) or end"},
		{"start", "25:00", "", false, "HH:MM"},
		{"start", "10:00", "2026-11-31", false, "YYYY-MM-DD"},
		{"start", "10:00", "tomorrow", false, "YYYY-MM-DD"},
		{"end", "10:00", "2026-11-05", true, "no date"},
	} {
		if _, err := NewSchedule(c.anchor, c.at, c.daily, c.date, now); err == nil || !strings.Contains(err.Error(), c.why) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// Next: the occurrence due or ahead; once vs daily; past times; what fired and when it was saved.
func TestNext(t *testing.T) {
	loc := zone(t, "Europe/Berlin")
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, loc) }
	daily := &Schedule{Anchor: AnchorStart, At: "18:00", Daily: true, Set: at(1, 12, 0)}
	for _, c := range []struct {
		now, want time.Time
	}{
		{at(2, 9, 0), at(2, 18, 0)},
		{at(2, 18, 0), at(2, 18, 0)},  // due
		{at(2, 18, 14), at(2, 18, 0)}, // up to Grace late
		{at(2, 18, 15), at(3, 18, 0)}, // later: skipped, tomorrow's
		{at(2, 23, 0), at(3, 18, 0)},
	} {
		if n, ok := daily.Next(c.now); !ok || !n.Equal(c.want) {
			t.Errorf("daily at %v: %v %v, want %v", c.now, n, ok, c.want)
		}
	}
	if m, ok := daily.Missed(at(2, 23, 0)); !ok || !m.Equal(at(2, 18, 0)) {
		t.Errorf("missed: %v %v", m, ok)
	}
	today := *daily
	today.Set = at(2, 12, 0)
	if _, ok := today.Missed(at(2, 18, 10)); ok {
		t.Error("missed while still in its window")
	}
	// Once fired, today's no longer counts.
	f := *daily
	f.Fired = at(2, 18, 0)
	if n, _ := f.Next(at(2, 18, 5)); !n.Equal(at(3, 18, 0)) {
		t.Errorf("after firing: %v", n)
	}
	if _, ok := f.Missed(at(2, 23, 0)); ok {
		t.Error("a fired occurrence counted as missed")
	}
	// Saved after today's time: today's doesn't count.
	late := *daily
	late.Set = at(2, 18, 5)
	if n, _ := late.Next(at(2, 18, 6)); !n.Equal(at(3, 18, 0)) {
		t.Errorf("saved after the time: %v", n)
	}
	// Once: its day only, then nothing.
	once := &Schedule{Anchor: AnchorEnd, At: "07:30", Date: "2026-11-03", Set: at(1, 12, 0)}
	if n, ok := once.Next(at(2, 9, 0)); !ok || !n.Equal(at(3, 7, 30)) {
		t.Errorf("once: %v %v", n, ok)
	}
	if _, ok := once.Next(at(3, 8, 0)); ok {
		t.Error("once: still due past its window")
	}
	if m, ok := once.Missed(at(3, 8, 0)); !ok || !m.Equal(at(3, 7, 30)) {
		t.Errorf("once missed: %v %v", m, ok)
	}
	// An end-anchored drain may begin the day before its time.
	mid := &Schedule{Anchor: AnchorEnd, At: "00:10", Daily: true, Set: at(1, 12, 0)}
	pl := PlanOf(mid, Estimate{Dur: 30 * time.Minute, From: "p80", N: 9}, at(2, 23, 45))
	if !pl.Next.Equal(at(3, 0, 10)) || !pl.DrainAt.Equal(at(2, 23, 40)) || !pl.Due {
		t.Errorf("across midnight: %+v", pl)
	}
}

// Local wall-clock time across DST changes: a daily time stays at its local hour, and a time the
// clocks skip is taken as the same moment past the gap.
func TestNextDST(t *testing.T) {
	ny := zone(t, "America/New_York")
	s := &Schedule{Anchor: AnchorStart, At: "18:00", Daily: true, Set: time.Date(2026, 3, 1, 0, 0, 0, 0, ny)}
	// 2026-03-08: clocks go forward at 02:00 in New York
	before := time.Date(2026, 3, 7, 19, 0, 0, 0, ny)
	n, _ := s.Next(before)
	if n.Hour() != 18 || n.Day() != 8 || n.Sub(before) != 22*time.Hour {
		t.Errorf("across spring forward: %v (%v from now)", n, n.Sub(before))
	}
	gap := &Schedule{Anchor: AnchorStart, At: "02:30", Daily: true, Set: time.Date(2026, 3, 1, 0, 0, 0, 0, ny)}
	n, _ = gap.Next(time.Date(2026, 3, 8, 1, 0, 0, 0, ny))
	if n.Hour() != 3 || n.Minute() != 30 || n.Day() != 8 {
		t.Errorf("in the gap: %v", n)
	}
	// 2026-11-01: clocks go back at 02:00
	s.Set = time.Date(2026, 10, 1, 0, 0, 0, 0, ny)
	before = time.Date(2026, 10, 31, 19, 0, 0, 0, ny)
	n, _ = s.Next(before)
	if n.Hour() != 18 || n.Day() != 1 || n.Sub(before) != 24*time.Hour {
		t.Errorf("across fall back: %v (%v from now)", n, n.Sub(before))
	}
	if d := day(n, before); d != "tomorrow" {
		t.Errorf("day across a 25-hour day: %q", d)
	}
}

func TestPlanText(t *testing.T) {
	loc := zone(t, "Europe/Berlin")
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, loc)
	est := Estimate{Dur: 20 * time.Minute, From: "p80", N: 23}
	for _, c := range []struct {
		s    Schedule
		want string
	}{
		{Schedule{Anchor: AnchorEnd, At: "19:00", Daily: true}, "drain at 18:40 today (done by 19:00, p80 20m), daily"},
		{Schedule{Anchor: AnchorStart, At: "18:00", Date: "2026-11-03"}, "drain at 18:00 tomorrow"},
		{Schedule{Anchor: AnchorStart, At: "18:00", Date: "2026-11-05"}, "drain at 18:00 Thu 5 Nov"},
		{Schedule{Anchor: AnchorEnd, At: "12:10", Date: "2026-11-02"}, "drain now (done by 12:10, p80 20m)"},
		{Schedule{Anchor: AnchorStart, At: "08:00", Date: "2026-11-02"}, "the drain set for 2026-11-02 08:00 was missed (the node was asleep or off)"},
	} {
		c.s.Set = now.Add(-24 * time.Hour)
		if got := PlanOf(&c.s, est, now).Text(now); got != c.want {
			t.Errorf("%+v: %q, want %q", c.s, got, c.want)
		}
	}
	if d := (&Schedule{Anchor: AnchorEnd, At: "19:00", Daily: true}).Describe(); d != "be stopped by 19:00, daily" {
		t.Error(d)
	}
	if a := (&Schedule{Anchor: AnchorStart, At: "19:00", Date: "2026-11-03"}).Args(); strings.Join(a, " ") != "start 19:00 2026-11-03" {
		t.Error(a)
	}
}

func TestEstimate(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	job := func(ago time.Duration, d time.Duration, out string) Finished {
		end := now.Add(-ago)
		return Finished{ID: "j", Start: end.Add(-d), End: end, Outcome: out}
	}
	if e := EstimateOf(nil, now, 30*time.Minute); e.From != "default" || e.Dur != 30*time.Minute || e.String() != "default 30m" {
		t.Errorf("none: %+v %s", e, e)
	}
	few := []Finished{job(time.Hour, 5*time.Minute, "passed"), job(time.Hour, 12*time.Minute, "failed"), job(time.Hour, 90*time.Minute, "back"), job(time.Hour, 80*time.Minute, "lost")}
	if e := EstimateOf(few, now, time.Minute); e.From != "max" || e.Dur != 12*time.Minute || e.N != 2 || e.String() != "longest of 2: 12m" {
		t.Errorf("few (handed back and lost left out): %+v", e)
	}
	// ten jobs of 1..10 minutes: the p80 (nearest rank) is the 8th
	var hs []Finished
	for i := 1; i <= 10; i++ {
		hs = append(hs, job(time.Hour, time.Duration(i)*time.Minute, "passed"))
	}
	if e := EstimateOf(hs, now, time.Minute); e.From != "p80" || e.Dur != 8*time.Minute || e.N != 10 {
		t.Errorf("p80: %+v", e)
	}
	// five: ceil(4) = the 4th
	if e := EstimateOf(hs[:5], now, time.Minute); e.From != "p80" || e.Dur != 4*time.Minute {
		t.Errorf("p80 of five: %+v", e)
	}
	// only the newest 50, and none older than 14 days
	var many []Finished
	for range 60 {
		many = append(many, job(20*24*time.Hour, 99*time.Minute, "passed")) // too old
	}
	for range 60 {
		many = append(many, job(48*time.Hour, 100*time.Minute, "passed"))
	}
	for range 50 {
		many = append(many, job(time.Hour, 3*time.Minute, "passed"))
	}
	if e := EstimateOf(many, now, time.Minute); e.Dur != 3*time.Minute || e.N != 50 {
		t.Errorf("the newest 50: %+v", e)
	}
	if e := EstimateOf(many[:60], now, 7*time.Minute); e.From != "default" {
		t.Errorf("all too old: %+v", e)
	}
	if Mins(65*time.Minute) != "1h05m" || Mins(45*time.Second) != "45s" || Mins(20*time.Minute+20*time.Second) != "20m" {
		t.Error(Mins(65*time.Minute), Mins(45*time.Second))
	}
}

func testProject(t *testing.T, config string) *conf.Project {
	t.Setenv("HOME", t.TempDir())
	base := conf.Dir("demo")
	os.MkdirAll(filepath.Join(base, "state"), 0o755)
	os.WriteFile(filepath.Join(base, "config"), []byte(config), 0o644)
	p, err := conf.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The history keeps the newest historyKeep jobs, and the default comes from JOB_EST_MIN.
func TestHistory(t *testing.T) {
	p := testProject(t, "JOB_EST_MIN=45\n")
	a := &Agent{P: p}
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for i := range historyKeep + 10 {
		a.record(Finished{ID: "j" + string(rune('a'+i%26)), Start: t0, End: t0.Add(time.Duration(i) * time.Second), Outcome: "passed"})
	}
	hs := ReadHistory(p)
	if len(hs) != historyKeep || hs[len(hs)-1].End != t0.Add(time.Duration(historyKeep+9)*time.Second) {
		t.Errorf("%d kept, last %v", len(hs), hs[len(hs)-1])
	}
	os.WriteFile(HistoryPath(p), []byte("not json\n"), 0o644)
	if e := JobEstimate(p, t0); e.From != "default" || e.Dur != 45*time.Minute {
		t.Errorf("JOB_EST_MIN: %+v", e)
	}
}

// The agent's decision, with its clock given: a start-anchored drain at its time, an end-anchored
// one once a typical job would no longer finish by it; a daily schedule kept and marked, a one-time
// one removed; a missed one-time schedule removed without draining.
func TestCheckSchedule(t *testing.T) {
	p := testProject(t, "")
	a := &Agent{P: p}
	loc := time.Local
	day := time.Now().AddDate(0, 0, 3)
	at := func(h, m int) time.Time { return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc) }
	flag := filepath.Join(p.State, "drain")
	// ten jobs of 20 minutes: p80 20m
	for range 10 {
		a.record(Finished{ID: "j", Start: at(8, 0), End: at(8, 20), Outcome: "passed"})
	}

	s, _ := NewSchedule(AnchorEnd, "19:00", true, "", at(9, 0))
	WriteSchedule(p, s)
	a.checkSchedule(at(18, 39))
	if exists(flag) || a.plan == nil || !a.plan.DrainAt.Equal(at(18, 40)) || a.plan.Est.From != "p80" {
		t.Fatalf("too early: %v %+v", exists(flag), a.plan)
	}
	a.checkSchedule(at(18, 40))
	if !exists(flag) {
		t.Fatal("end-anchored: no drain when the p80 no longer fits")
	}
	cur, _ := ReadSchedule(p)
	if cur == nil || !cur.Fired.Equal(at(19, 0)) || a.plan == nil || !a.plan.Next.Equal(at(19, 0).AddDate(0, 0, 1)) {
		t.Fatalf("daily: kept, fired marked, the next is tomorrow's: %+v %+v", cur, a.plan)
	}
	// started again (start clears the flag): no second drain for the same occurrence
	os.Remove(flag)
	a.checkSchedule(at(18, 45))
	if exists(flag) {
		t.Fatal("drained twice for one occurrence")
	}

	// once, start-anchored: drains at its time and is removed
	os.Remove(flag)
	s, _ = NewSchedule(AnchorStart, "20:00", false, at(0, 0).Format("2006-01-02"), at(9, 0))
	WriteSchedule(p, s)
	a.checkSchedule(at(19, 59))
	if exists(flag) {
		t.Fatal("start-anchored: drained early")
	}
	a.checkSchedule(at(20, 0))
	if !exists(flag) || exists(SchedulePath(p)) || a.plan != nil {
		t.Fatalf("once: drained %v, schedule left %v, plan %+v", exists(flag), exists(SchedulePath(p)), a.plan)
	}

	// once, missed (the node slept through it): removed, no drain
	os.Remove(flag)
	s, _ = NewSchedule(AnchorStart, "21:00", false, at(0, 0).Format("2006-01-02"), at(9, 0))
	WriteSchedule(p, s)
	a.checkSchedule(at(22, 0))
	if exists(flag) || exists(SchedulePath(p)) {
		t.Fatalf("missed once: drained %v, schedule left %v", exists(flag), exists(SchedulePath(p)))
	}
	log, _ := os.ReadFile(filepath.Join(p.State, "agent.log"))
	for _, w := range []string{"scheduled drain: no new jobs from now, to be stopped by 19:00 (a job takes p80 20m)", "scheduled drain: no new jobs from now; stopping",
		"skipped the drain scheduled for", "the one-time drain schedule for"} {
		if !strings.Contains(string(log), w) {
			t.Errorf("log lacks %q:\n%s", w, log)
		}
	}

	// a schedule saved meanwhile is not overwritten by the one that fired
	s, _ = NewSchedule(AnchorStart, "10:00", true, "", at(9, 0))
	if a.fired(&Schedule{Set: at(1, 0), Daily: true}, at(10, 0)) {
		t.Error("marked a schedule that is no longer the file's")
	}
	// a broken file: ignored, logged
	os.WriteFile(SchedulePath(p), []byte("{"), 0o644)
	a.checkSchedule(at(10, 0))
	if a.plan != nil || exists(flag) {
		t.Error("a broken schedule acted on")
	}
}
