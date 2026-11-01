package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

// The jobs this node finished for a project, newest last, one JSON object a line in
// state/history.jsonl: what an end-anchored drain schedule learns how long a job takes from.

// Finished: one job this node ended.
type Finished struct {
	ID      string    `json:"id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Outcome string    `json:"outcome"` // passed, failed, back (handed back), lost (its lease)
}

const (
	historyKeep = 200 // lines kept in the file
	estimateN   = 50  // the newest finished jobs an estimate looks at
	estimateMin = 5   // fewer than this: no percentile, the longest of them
	// EstimateAge: jobs older than this are left out of an estimate.
	EstimateAge = 14 * 24 * time.Hour
	// JobEstDefault: what a job is taken to last before this node has finished one (JOB_EST_MIN).
	JobEstDefault = 30 * time.Minute
)

func HistoryPath(p *conf.Project) string { return filepath.Join(p.State, "history.jsonl") }

// ReadHistory: the jobs recorded, oldest first (lines that don't parse are skipped).
func ReadHistory(p *conf.Project) []Finished {
	b, err := os.ReadFile(HistoryPath(p))
	if err != nil {
		return nil
	}
	var r []Finished
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var f Finished
		if json.Unmarshal(sc.Bytes(), &f) == nil && !f.End.IsZero() {
			r = append(r, f)
		}
	}
	return r
}

// record appends f to the history, keeping the newest historyKeep.
func (a *Agent) record(f Finished) {
	a.histMu.Lock()
	defer a.histMu.Unlock()
	hs := append(ReadHistory(a.P), f)
	hs = hs[max(len(hs)-historyKeep, 0):]
	var b bytes.Buffer
	for _, h := range hs {
		l, _ := json.Marshal(h)
		b.Write(append(l, '\n'))
	}
	if err := conf.WriteAtomic(HistoryPath(a.P), b.Bytes(), 0o644); err != nil {
		a.Logf("job history: %v", err)
	}
}

// Estimate: how long a job of this project takes on this node, and how that was decided.
type Estimate struct {
	Dur  time.Duration `json:"dur"`
	From string        `json:"from"` // p80, max (too few jobs for a percentile), default (none: JOB_EST_MIN)
	N    int           `json:"n"`    // the jobs it is from
}

// String: "p80 20m", "longest of 3: 12m", "default 30m".
func (e Estimate) String() string {
	switch e.From {
	case "p80":
		return "p80 " + Mins(e.Dur)
	case "max":
		return fmt.Sprintf("longest of %d: %s", e.N, Mins(e.Dur))
	}
	return "default " + Mins(e.Dur)
}

// Mins: a duration to the minute, as a schedule cares: 45s, 20m, 1h05m.
func Mins(d time.Duration) string {
	if d < time.Minute {
		return ui.Dur(d.Round(time.Second))
	}
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return fmt.Sprintf("%dh%02dm", d/time.Hour, d%time.Hour/time.Minute)
}

// Long: the estimate in words, for a person deciding a schedule.
func (e Estimate) Long() string {
	switch e.From {
	case "p80":
		return fmt.Sprintf("a job here takes %s or less 4 times in 5 (p80 of the last %d)", Mins(e.Dur), e.N)
	case "max":
		return fmt.Sprintf("a job here took at most %s (only %d finished: fewer than %d for a p80)", Mins(e.Dur), e.N, estimateMin)
	}
	return fmt.Sprintf("no job has finished here in 14 days: taken as %s (JOB_EST_MIN)", Mins(e.Dur))
}

// EstimateOf: from the jobs that ran to the end (passed or failed) among the newest estimateN,
// within EstimateAge of now: their 80th percentile (nearest rank); with fewer than estimateMin, the
// longest; with none, def.
func EstimateOf(hs []Finished, now time.Time, def time.Duration) Estimate {
	var ds []time.Duration
	for i := len(hs) - 1; i >= 0 && len(ds) < estimateN; i-- {
		h := hs[i]
		if h.Outcome != "passed" && h.Outcome != "failed" || now.Sub(h.End) > EstimateAge || h.End.Before(h.Start) {
			continue
		}
		ds = append(ds, h.End.Sub(h.Start))
	}
	switch {
	case len(ds) == 0:
		return Estimate{Dur: def, From: "default"}
	case len(ds) < estimateMin:
		return Estimate{Dur: slices.Max(ds), From: "max", N: len(ds)}
	}
	slices.Sort(ds)
	i := (len(ds)*80+99)/100 - 1 // ceil(0.8 n), from 1
	return Estimate{Dur: ds[i], From: "p80", N: len(ds)}
}

// JobEstDefault for a project: JOB_EST_MIN from its config, or ~/.sat/config, else 30.
func jobEstDefault(p *conf.Project) time.Duration {
	m, err := conf.Parse(filepath.Join(p.Base, "config"), nil)
	if err != nil {
		m = p.Conf
	}
	v := m["JOB_EST_MIN"]
	if v == "" {
		v = conf.Node()["JOB_EST_MIN"]
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
		return time.Duration(n) * time.Minute
	}
	return JobEstDefault
}

// JobEstimate: the estimate for p's jobs now.
func JobEstimate(p *conf.Project, now time.Time) Estimate {
	return EstimateOf(ReadHistory(p), now, jobEstDefault(p))
}
