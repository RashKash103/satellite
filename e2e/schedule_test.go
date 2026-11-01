package e2e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
)

// writeHistory: n finished jobs of d each, as the agent records them.
func writeHistory(t *testing.T, n *node, count int, d time.Duration) {
	var b strings.Builder
	end := time.Now().Add(-time.Hour)
	for range count {
		l, _ := json.Marshal(agent.Finished{ID: "old", Start: end.Add(-d), End: end, Outcome: "passed"})
		b.Write(append(l, '\n'))
	}
	if err := os.WriteFile(n.state("history.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A start-anchored daily schedule: at its time the agent stops claiming, lets the running job
// finish and stops; the schedule stays (marked as fired) and `sat start` keeps it.
func TestScheduledDrainStopsTheAgent(t *testing.T) {
	job := "#!/usr/bin/env bash\ntouch \"$SAT_BASE/state/ran-$1\"\nsleep 2\necho \"done $1\" > \"$SAT_RESULT\"\n"
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": job}), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=1\n")
	h.queue("j1", "1")
	h.queue("j2", "1")
	a := n.agent()
	waitFor(t, "the first job to start", 20*time.Second, func() bool { return exists(n.state("ran-j1")) || exists(n.state("ran-j2")) })
	first, second := "j1", "j2"
	if exists(n.state("ran-j2")) {
		first, second = "j2", "j1"
	}
	// due now: this minute, saved an hour ago (sat schedule itself only takes times ahead)
	now := time.Now()
	s := &agent.Schedule{Anchor: agent.AnchorStart, At: now.Format("15:04"), Daily: true, Set: now.Add(-time.Hour)}
	b, _ := json.Marshal(s)
	os.WriteFile(n.state("schedule.json"), b, 0o644)
	select {
	case err := <-a.done:
		if err != nil {
			t.Fatalf("the agent exited with %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the scheduled drain did not stop the agent")
	}
	out := a.out.String()
	for _, w := range []string{"scheduled drain: no new jobs from now; stopping once the running ones finish", first + " passed", "drained: stopping"} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
	if !written(h.hello+"/done/"+first) || !exists(h.hello+"/queue/"+second) || exists(n.state("ran-"+second)) {
		t.Error("the running job did not finish, or another was claimed")
	}
	if exists(n.state("drain")) {
		t.Error("the drain flag was left behind")
	}
	var kept agent.Schedule
	if err := json.Unmarshal([]byte(read(n.state("schedule.json"))), &kept); err != nil || !kept.Daily || kept.Fired.IsZero() {
		t.Fatalf("the daily schedule after its drain: %+v %v", kept, err)
	}
	if out, _ := n.sat("hello", "status"); !strings.Contains(out, "schedule: start draining at "+s.At+", daily: drain at "+s.At+" tomorrow, daily") {
		t.Errorf("status:\n%s", out)
	}
	if out, err := n.sat("hello", "start"); err != nil || !exists(n.state("schedule.json")) {
		t.Errorf("start removed the schedule: %v %s", err, out)
	}
	if !strings.Contains(read(n.state("history.jsonl")), `"outcome":"passed"`) {
		t.Error("the finished job was not recorded")
	}
}

// An end-anchored schedule: while a typical job (the p80 of the node's history) still fits before
// the time, jobs are claimed; once it doesn't, the agent stops claiming and stops, and the
// one-time schedule clears itself.
func TestEndScheduleStopsClaimingWhenAJobWouldNotFit(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=1\n")
	writeHistory(t, n, 6, time.Minute)
	end := time.Now().Add(time.Hour)
	if out, err := n.sat("hello", "schedule", "end", end.Format("15:04"), end.Format("2006-01-02")); err != nil ||
		!strings.Contains(out, "be stopped by "+end.Format("15:04")) || !strings.Contains(out, "p80 1m") {
		t.Fatalf("%v %s", err, out)
	}
	h.queue("j1", "100")
	a := n.agent()
	waitFor(t, "j1 to pass", 30*time.Second, func() bool { return written(h.hello + "/done/j1") })
	waitFor(t, "the plan in agent.json", 10*time.Second, func() bool {
		return strings.Contains(read(n.state("agent.json")), `"drain_at"`) && strings.Contains(read(n.state("agent.json")), `"from": "p80"`)
	})
	if out, _ := n.sat("hello", "status"); !strings.Contains(out, "schedule: be stopped by "+end.Format("15:04")) {
		t.Errorf("status:\n%s", out)
	}
	// Jobs now take two hours: one would no longer be done by then.
	os.WriteFile(n.state("hold"), nil, 0o644)
	writeHistory(t, n, 6, 2*time.Hour)
	h.queue("j2", "100")
	os.Remove(n.state("hold"))
	select {
	case err := <-a.done:
		if err != nil {
			t.Fatalf("the agent exited with %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the end-anchored schedule did not stop the agent")
	}
	if !exists(h.hello + "/queue/j2") {
		t.Error("claimed a job that would not be done in time")
	}
	out := a.out.String()
	for _, w := range []string{"scheduled drain: no new jobs from now, to be stopped by " + end.Format("15:04") + " (a job takes p80 2h00m)", "drained: stopping"} {
		if !strings.Contains(out, w) {
			t.Errorf("log lacks %q:\n%s", w, out)
		}
	}
	if exists(n.state("schedule.json")) {
		t.Error("the one-time schedule did not clear itself")
	}
}
