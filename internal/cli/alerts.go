package cli

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

// alertsOf: a project's current alerts: what its agent raised, and a failed service.
func alertsOf(p *conf.Project, active string) []agent.Alert {
	var as []agent.Alert
	if a := agent.ServiceAlert(p, active); a != nil {
		as = append(as, *a)
	}
	if st, err := agent.ReadState(p); err == nil {
		as = append(as, st.Active()...)
	}
	return agent.Sorted(as)
}

func sinceWord(t, now time.Time) string {
	if t.IsZero() {
		return "now"
	}
	return ui.Dur(now.Sub(t))
}

// alertText: one alert for a terminal, marked by its level.
func alertText(t *ui.T, a agent.Alert, now time.Time) string {
	since := t.Muted("  " + sinceWord(a.Since, now))
	switch a.Level {
	case agent.Error:
		return t.Mark(ui.MarkBad) + t.Bad(a.Text) + since
	case agent.Warn:
		return t.Mark(ui.MarkWarn) + t.Warn(a.Text) + since
	}
	return t.Mark(ui.MarkInfo) + a.Text + since
}

// alertRows: every project's alerts, as rows (project, level, since, text).
func alertRows(t *ui.T, names []string, now time.Time) [][]string {
	var rows [][]string
	for _, n := range names {
		p, err := conf.Load(n)
		if err != nil {
			continue
		}
		for _, a := range alertsOf(p, scOut("is-active", p.Unit())) {
			lv := a.Level
			switch {
			case !t.Color():
			case a.Level == agent.Error:
				lv = t.Bad(lv)
			case a.Level == agent.Warn:
				lv = t.Warn(lv)
			default:
				lv = t.Muted(lv)
			}
			rows = append(rows, []string{t.Bold(n), lv, sinceWord(a.Since, now), a.Text})
		}
	}
	return rows
}

// alerts: sat [PROJECT] alerts [-w]: the current alerts, of one project or all; -w keeps them live.
func alerts(names []string, a []string) int {
	watch, a := flag(a, "-w", "--watch")
	if len(a) > 0 {
		return fail("usage: sat [PROJECT] alerts [-w]")
	}
	t := ui.Out()
	lines := func() []string {
		rows := alertRows(t, names, time.Now())
		if len(rows) == 0 {
			return []string{t.Mark(ui.MarkOK) + "no alerts"}
		}
		return splitLines(t.Table("", []string{"PROJECT", "LEVEL", "SINCE", "ALERT"}, rows))
	}
	if !watch {
		for _, l := range lines() {
			t.Println(l)
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s := newScreenTitled("alerts")
	for ctx.Err() == nil {
		s.draw(lines(), "every 2s")
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return 0
}
