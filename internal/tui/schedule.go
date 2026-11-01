package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/agent"
)

var anchorOpts = []string{"start at", "done by"} // agent.AnchorStart, agent.AnchorEnd

// scheduleDialog: set or remove the project's drain schedule (sat PROJECT schedule ...). The agent
// keeps it; the dialog only runs the command.
func (m *model) scheduleDialog(p *proj) *modal {
	n := p.p.Name
	now := time.Now()
	cur, _ := agent.ReadSchedule(p.p)
	est := agent.JobEstimate(p.p, now)
	anchor, at, daily, date := 0, "", 0, ""
	if cur != nil {
		if cur.Anchor == agent.AnchorEnd {
			anchor = 1
		}
		at, date = cur.At, cur.Date
		if cur.Daily {
			daily = 1
		}
	}
	d := &modal{kind: "schedule", title: "Drain schedule · " + n, width: 60}
	d.fields = []*field{
		choiceField("Drain", anchorOpts, anchor),
		textField("Time", "HH:MM, 24-hour", at),
		choiceField("Repeat", []string{"once", "daily"}, daily),
		textField("Date", "YYYY-MM-DD (empty: the next one)", date),
	}
	d.fields[3].hide = func() bool { return d.fields[2].at == 1 }
	d.body = []string{
		"start at: stop taking jobs at the time, and stop once the running ones finish (as d). " +
			"done by: be stopped by the time: stop taking jobs once a typical job would no longer finish by it.",
		m.st.Muted.Render(est.Long() + "."),
	}
	if cur != nil {
		if pl, err := agent.PlanFor(p.p, now); err == nil && pl != nil {
			d.body = append(d.body, "Now: "+cur.Describe()+": "+pl.Text(now)+".")
		}
	}
	d.preview = func(d *modal) (string, bool) {
		s, err := scheduleOf(d, time.Now())
		if err != nil {
			if d.fields[1].value() == "" {
				return "", true
			}
			return err.Error(), false
		}
		return "→ " + agent.PlanOf(s, est, time.Now()).Text(time.Now()), true
	}
	d.buttons = []button{
		{label: "Save", do: func(d *modal) (tea.Cmd, string) {
			s, err := scheduleOf(d, time.Now())
			if err != nil {
				return nil, err.Error()
			}
			a := append([]string{n, "schedule"}, s.Args()...)
			if !s.Daily && d.fields[3].value() == "" {
				a = a[:len(a)-1] // the next one, decided when it is saved
			}
			return m.stream("schedule "+n, a...), ""
		}},
		{label: "Remove schedule", off: func() bool { return cur == nil }, do: func(*modal) (tea.Cmd, string) {
			return m.stream("schedule off "+n, n, "schedule", "off"), ""
		}},
		{label: "Cancel", cancel: true},
	}
	if at == "" {
		d.focus = 1 // the time: the anchor and the repeat have a choice already
	}
	return d
}

// scheduleOf: the schedule the dialog's fields say, or why not.
func scheduleOf(d *modal, now time.Time) (*agent.Schedule, error) {
	anchor := agent.AnchorStart
	if d.fields[0].at == 1 {
		anchor = agent.AnchorEnd
	}
	daily := d.fields[2].at == 1
	date := d.fields[3].value()
	if daily {
		date = ""
	}
	return agent.NewSchedule(anchor, d.fields[1].value(), daily, date, now)
}
