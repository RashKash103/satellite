package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
)

// A drain schedule: drain the project at a local time, once or every day. The agent enforces it
// (checkSchedule, each tick and before each claim), so it holds with the live view closed. It is
// kept in state/schedule.json, apart from the drain flag (state/drain), which start, stop and the
// unit's stop clear: a daily schedule outlives the drains it starts.
//
// Anchor "start": stop taking jobs at the time; the agent stops once the running ones finish, as
// `sat drain`. Anchor "end": be stopped by the time: stop taking jobs once a typical job (Estimate)
// would no longer finish by it, at time - estimate.
//
// An occurrence counts only if it falls after the schedule was saved and after the last one that
// fired; it fires from its drain point until Grace past its time. A node asleep or off through that
// whole window skips it (logged): a daily schedule then waits for the next day rather than drain
// hours late, and a one-time schedule is cleared.

const (
	AnchorStart = "start"
	AnchorEnd   = "end"
	// Grace: how late an occurrence may still fire (a node that was asleep at the time).
	Grace = 15 * time.Minute
)

type Schedule struct {
	Anchor string    `json:"anchor"`         // start or end
	At     string    `json:"at"`             // HH:MM, local time
	Daily  bool      `json:"daily"`          // every day; else once
	Date   string    `json:"date,omitempty"` // YYYY-MM-DD: a one-time schedule's day
	Set    time.Time `json:"set"`            // when it was saved
	Fired  time.Time `json:"fired,omitzero"` // the time of the last occurrence a drain began for
}

func SchedulePath(p *conf.Project) string { return filepath.Join(p.State, "schedule.json") }

// ReadSchedule: the project's schedule; nil (and no error) when it has none.
func ReadSchedule(p *conf.Project) (*Schedule, error) {
	b, err := os.ReadFile(SchedulePath(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Schedule
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %v", SchedulePath(p), err)
	}
	if err := s.valid(); err != nil {
		return nil, fmt.Errorf("%s: %v", SchedulePath(p), err)
	}
	return &s, nil
}

func WriteSchedule(p *conf.Project, s *Schedule) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return conf.WriteAtomic(SchedulePath(p), append(b, '\n'), 0o644)
}

// RemoveSchedule: no schedule; false if there was none.
func RemoveSchedule(p *conf.Project) (bool, error) {
	err := os.Remove(SchedulePath(p))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ParseClock: HH:MM (or H:MM), 00:00 to 23:59.
func ParseClock(s string) (int, int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("a time is HH:MM, 24-hour: %q", s)
	}
	return t.Hour(), t.Minute(), nil
}

func parseDate(s string, loc *time.Location) (time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("a date is YYYY-MM-DD: %q", s)
	}
	return d, nil
}

func (s *Schedule) valid() error {
	if s.Anchor != AnchorStart && s.Anchor != AnchorEnd {
		return fmt.Errorf("the anchor is start or end, not %q", s.Anchor)
	}
	if _, _, err := ParseClock(s.At); err != nil {
		return err
	}
	if !s.Daily {
		if _, err := parseDate(s.Date, time.Local); err != nil {
			return err
		}
	}
	return nil
}

// NewSchedule: a schedule saved at now. A one-time one with no date is for the next time the
// clock reads at; one whose time has passed is refused.
func NewSchedule(anchor, at string, daily bool, date string, now time.Time) (*Schedule, error) {
	s := &Schedule{Anchor: anchor, At: at, Daily: daily, Set: now}
	h, mi, err := ParseClock(at)
	if err != nil {
		return nil, err
	}
	s.At = fmt.Sprintf("%02d:%02d", h, mi) // 9:05 is 09:05
	if anchor != AnchorStart && anchor != AnchorEnd {
		return nil, fmt.Errorf("start (drain from the time) or end (be stopped by it), not %q", anchor)
	}
	if daily {
		if date != "" {
			return nil, errors.New("a daily schedule takes no date")
		}
		return s, nil
	}
	if date == "" {
		t := clock(now, h, mi)
		if !t.After(now) {
			t = clock(now.AddDate(0, 0, 1), h, mi)
		}
		date = t.Format("2006-01-02")
	}
	d, err := parseDate(date, now.Location())
	if err != nil {
		return nil, err
	}
	s.Date = d.Format("2006-01-02")
	if !clock(d, h, mi).After(now) {
		return nil, fmt.Errorf("%s %s has passed", s.Date, s.At)
	}
	return s, nil
}

// clock: day's date at h:mi in day's location. A time the clocks skip (a DST change) is
// normalised past the gap (02:30 on a spring-forward night is 03:30).
func clock(day time.Time, h, mi int) time.Time {
	y, mo, d := day.Date()
	t := time.Date(y, mo, d, h, mi, 0, 0, day.Location())
	wall := func(y int, mo time.Month, d, h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, time.UTC) }
	if wall(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute()).Before(wall(y, mo, d, h, mi)) {
		// in a gap, time.Date may answer with the offset from after it, an hour early: take the
		// same distance past the change instead
		_, end := t.ZoneBounds()
		_, before := t.Zone()
		_, after := end.Zone()
		t = t.Add(time.Duration(after-before) * time.Second)
	}
	return t
}

// occurrences: the schedule's times around now (in now's location): its day, or for a daily one
// yesterday, today and the next two days.
func (s *Schedule) occurrences(now time.Time) []time.Time {
	h, mi, err := ParseClock(s.At)
	if err != nil {
		return nil
	}
	if !s.Daily {
		d, err := parseDate(s.Date, now.Location())
		if err != nil {
			return nil
		}
		return []time.Time{clock(d, h, mi)}
	}
	var r []time.Time
	for i := -1; i <= 2; i++ {
		r = append(r, clock(now.AddDate(0, 0, i), h, mi))
	}
	return r
}

// after: occurrences count only after the schedule was saved and after the last that fired.
func (s *Schedule) after() time.Time {
	if s.Fired.After(s.Set) {
		return s.Fired
	}
	return s.Set
}

// Next: the occurrence due or still ahead at now (its time, not its drain point), if any: the
// first after s.after() that now is not Grace past.
func (s *Schedule) Next(now time.Time) (time.Time, bool) {
	for _, t := range s.occurrences(now) {
		if t.After(s.after()) && now.Before(t.Add(Grace)) {
			return t, true
		}
	}
	return time.Time{}, false
}

// Missed: the latest occurrence skipped by now (now was past its time and Grace with no drain
// begun for it), if any within the last day.
func (s *Schedule) Missed(now time.Time) (time.Time, bool) {
	var m time.Time
	for _, t := range s.occurrences(now) {
		if t.After(s.after()) && !now.Before(t.Add(Grace)) && now.Sub(t) < 24*time.Hour+Grace {
			m = t
		}
	}
	return m, !m.IsZero()
}

// Plan: what a schedule means now: the next occurrence and when its drain begins.
type Plan struct {
	Schedule Schedule  `json:"schedule"`
	Next     time.Time `json:"next,omitzero"`     // the occurrence: drain from (start), stopped by (end)
	DrainAt  time.Time `json:"drain_at,omitzero"` // when it stops taking jobs
	Est      Estimate  `json:"estimate"`          // what an end-anchored drain allows for a job
	Due      bool      `json:"due"`               // the drain begins now
	Missed   time.Time `json:"missed,omitzero"`   // an occurrence skipped (asleep or off through it)
}

// PlanOf: s at now, with est the time a job takes.
func PlanOf(s *Schedule, est Estimate, now time.Time) *Plan {
	pl := &Plan{Schedule: *s, Est: est}
	pl.Missed, _ = s.Missed(now)
	t, ok := s.Next(now)
	if !ok {
		return pl
	}
	pl.Next, pl.DrainAt = t, t
	if s.Anchor == AnchorEnd {
		pl.DrainAt = t.Add(-est.Dur)
	}
	pl.Due = !now.Before(pl.DrainAt)
	return pl
}

// PlanFor: p's schedule at now (nil when it has none).
func PlanFor(p *conf.Project, now time.Time) (*Plan, error) {
	s, err := ReadSchedule(p)
	if s == nil || err != nil {
		return nil, err
	}
	return PlanOf(s, JobEstimate(p, now), now), nil
}

// day: "today", "tomorrow", "yesterday" or "Mon 3 Nov".
func day(t, now time.Time) string {
	date := func(t time.Time) time.Time { y, m, d := t.Date(); return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) } // whole days, DST or not
	switch date(t.In(now.Location())).Sub(date(now)) / (24 * time.Hour) {
	case 0:
		return "today"
	case 1:
		return "tomorrow"
	case -1:
		return "yesterday"
	}
	return t.Format("Mon 2 Jan")
}

// Text: the plan in a few words: "drain at 18:40 today (done by 19:00, p80 20m), daily".
func (pl *Plan) Text(now time.Time) string {
	s := pl.Schedule
	rep := ", daily"
	if !s.Daily {
		rep = ""
	}
	if pl.Next.IsZero() {
		if !s.Daily {
			return fmt.Sprintf("the drain set for %s %s was missed (the node was asleep or off)", s.Date, s.At)
		}
		return "drain daily at " + s.At
	}
	when := pl.DrainAt.Format("15:04") + " " + day(pl.DrainAt, now)
	if pl.Due {
		when = "now"
	}
	if s.Anchor == AnchorEnd {
		return fmt.Sprintf("drain %s (done by %s, %s)%s", at(when), pl.Next.Format("15:04"), pl.Est, rep)
	}
	return "drain " + at(when) + rep
}

func at(when string) string {
	if when == "now" {
		return when
	}
	return "at " + when
}

// Describe: the schedule itself: "start draining at 18:00, daily", "be stopped by 19:00 on 2026-11-02".
func (s *Schedule) Describe() string {
	w := "start draining at " + s.At
	if s.Anchor == AnchorEnd {
		w = "be stopped by " + s.At
	}
	if s.Daily {
		return w + ", daily"
	}
	return w + " on " + s.Date
}

// Args: the schedule as `sat PROJECT schedule` words.
func (s *Schedule) Args() []string {
	a := []string{s.Anchor, s.At}
	if s.Daily {
		return append(a, "daily")
	}
	return append(a, s.Date)
}

// checkSchedule: begin the drain the schedule asks for at now, if it is due (the drain flag, as
// `sat drain` sets it), and keep the plan for agent.json.
func (a *Agent) checkSchedule(now time.Time) {
	a.schedMu.Lock()
	defer a.schedMu.Unlock()
	s, err := ReadSchedule(a.P)
	if err != nil {
		if err.Error() != a.schedErr {
			a.schedErr = err.Error()
			a.Logf("drain schedule: %v (ignored)", err)
		}
		a.plan = nil
		return
	}
	a.schedErr = ""
	if s == nil {
		a.plan = nil
		return
	}
	est := JobEstimate(a.P, now)
	pl := PlanOf(s, est, now)
	if m := pl.Missed; !m.IsZero() && !m.Equal(a.missed) {
		a.missed = m
		a.Logf("skipped the drain scheduled for %s: the node was asleep or off then", m.Format("01-02 15:04"))
	}
	switch {
	case pl.Due:
		if a.draining() {
			a.Logf("scheduled drain (%s): already draining", s.Describe())
		} else {
			os.WriteFile(filepath.Join(a.P.State, "drain"), nil, 0o644)
			if s.Anchor == AnchorEnd {
				a.Logf("scheduled drain: no new jobs from now, to be stopped by %s (a job takes %s); stopping once the running ones finish", pl.Next.Format("15:04"), est)
			} else {
				a.Logf("scheduled drain: no new jobs from now; stopping once the running ones finish")
			}
		}
		if a.fired(s, pl.Next) {
			s.Fired = pl.Next
			pl = PlanOf(s, est, now)
		}
		if !s.Daily {
			pl = nil
		}
	case !s.Daily && pl.Next.IsZero():
		if a.fired(s, time.Time{}) {
			a.Logf("the one-time drain schedule for %s %s passed: removed", s.Date, s.At)
		}
		pl = nil
	}
	a.plan = pl
}

// fired records that the drain for occurrence t began (t zero: that it was missed): a daily
// schedule keeps it, a one-time one is removed. Only if the file still holds s (not one saved
// since); false if not.
func (a *Agent) fired(s *Schedule, t time.Time) bool {
	cur, err := ReadSchedule(a.P)
	if err != nil || cur == nil || !cur.Set.Equal(s.Set) {
		return false
	}
	if !s.Daily {
		RemoveSchedule(a.P)
		return true
	}
	cur.Fired = t
	if err := WriteSchedule(a.P, cur); err != nil {
		a.Logf("drain schedule: %v", err)
		return false
	}
	return true
}
