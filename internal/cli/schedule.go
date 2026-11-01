package cli

import (
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

const scheduleUsage = "usage: sat [PROJECT] schedule [start|end HH:MM [daily|YYYY-MM-DD] | off]"

// scheduleCmd: sat PROJECT schedule: show, set or remove the project's drain schedule. The agent
// enforces it (agent.Schedule).
func scheduleCmd(p *conf.Project, a []string) int {
	t := ui.Out()
	now := time.Now()
	switch {
	case len(a) == 0:
		pl, err := agent.PlanFor(p, now)
		if err != nil {
			return fail(err.Error())
		}
		if pl == nil {
			t.Info("no drain scheduled for %s", p.Name)
			return 0
		}
		t.Println(t.Mark(ui.MarkInfo) + p.Name + ": " + pl.Schedule.Describe() + t.Muted(": "+pl.Text(now)))
		if pl.Schedule.Anchor == agent.AnchorEnd {
			t.Println(t.Muted("  " + pl.Est.Long()))
		}
		return 0
	case len(a) == 1 && (a[0] == "off" || a[0] == "none" || a[0] == "remove"):
		had, err := agent.RemoveSchedule(p)
		switch {
		case err != nil:
			return fail(err.Error())
		case had:
			t.Done("no drain scheduled for %s any more", t.Bold(p.Name))
		default:
			t.Info("no drain was scheduled for %s", p.Name)
		}
		return 0
	case len(a) < 2 || len(a) > 3:
		return fail(scheduleUsage)
	}
	daily, date := false, ""
	if len(a) == 3 {
		switch a[2] {
		case "daily":
			daily = true
		case "once":
		default:
			date = a[2]
		}
	}
	s, err := agent.NewSchedule(a[0], a[1], daily, date, now)
	if err != nil {
		return failHint(err.Error(), strings.TrimPrefix(scheduleUsage, "usage: "))
	}
	if err := agent.WriteSchedule(p, s); err != nil {
		return fail(err.Error())
	}
	pl := agent.PlanOf(s, agent.JobEstimate(p, now), now)
	t.Done("%s: %s", t.Bold(p.Name), s.Describe()+t.Muted(": "+pl.Text(now)))
	if s.Anchor == agent.AnchorEnd {
		t.Println(t.Muted("  " + pl.Est.Long()))
	}
	if !active(p.Unit()) {
		t.Info("%s isn't running: the agent keeps the schedule while it runs (sat %s start)", p.Name, p.Name)
	}
	return 0
}
