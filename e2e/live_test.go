package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type liveLane struct {
	Slot     int    `json:"slot"`
	ID       string `json:"id"`
	Draining bool   `json:"draining"`
	Off      bool   `json:"off"`
}

type liveState struct {
	Slots   int        `json:"slots"`
	Pending []string   `json:"pending"`
	Lanes   []liveLane `json:"lanes"`
}

func (n *node) live() liveState {
	var s liveState
	json.Unmarshal([]byte(read(n.state("agent.json"))), &s)
	return s
}

// lanesText: the lanes as "slot:id[:draining|:off]", for messages and checks.
func (s liveState) lanesText() string {
	var r []string
	for _, l := range s.Lanes {
		x := fmt.Sprintf("%d:%s", l.Slot, l.ID)
		if l.Draining {
			x += ":draining"
		}
		if l.Off {
			x += ":off"
		}
		r = append(r, x)
	}
	return strings.Join(r, " ")
}

// SLOTS changes while the agent runs: raising it opens a lane that takes a job; lowering it while
// both lanes are busy drains exactly one, which closes after its job while the other keeps claiming;
// a key that shapes the connection to the hub waits for the next start.
func TestLiveConfig(t *testing.T) {
	// Each job runs until the test lets it go (state/go-ID), and says which lane ran it.
	job := "#!/usr/bin/env bash\necho \"$SAT_SLOT\" > \"$SAT_BASE/state/lane-$1\"\nuntil [ -e \"$SAT_BASE/state/go-$1\" ]; do sleep 0.1; done\necho \"$1 done\" > \"$SAT_RESULT\"\n"
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": job}), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=1\n")
	n.extraEnv = append(n.extraEnv, "FAKE_ACTIVE=1")
	for _, j := range []string{"j1", "j2", "j3", "j4"} {
		h.queue(j, "5")
	}
	a := n.agent()
	free := func(id string) { os.WriteFile(n.state("go-"+id), nil, 0o644) }
	busy := func(s liveState) []string {
		var r []string
		for _, l := range s.Lanes {
			if l.ID != "" {
				r = append(r, l.ID)
			}
		}
		return r
	}
	waitFor(t, "the first job", 20*time.Second, func() bool { return len(h.leases()) == 1 })
	time.Sleep(time.Second)
	if l := h.leases(); len(l) != 1 {
		t.Fatalf("SLOTS=1 but %d leases: %v", len(l), l)
	}

	// Up: a second lane opens and takes a job.
	if out, err := n.sat("hello", "config", "SLOTS=2"); err != nil || !strings.Contains(out, "applies it within seconds") {
		t.Fatalf("config SLOTS=2: %v\n%s", err, out)
	}
	waitFor(t, "a second lane with a job", 20*time.Second, func() bool { return len(h.leases()) == 2 && len(busy(n.live())) == 2 })
	if s := n.live(); s.Slots != 2 || len(s.Lanes) != 2 {
		t.Fatalf("after raising: slots %d, lanes %s", s.Slots, s.lanesText())
	}

	// Down while both lanes are busy: exactly one drains, and claims nothing more.
	n.sat("hello", "config", "SLOTS=1")
	waitFor(t, "one lane draining", 10*time.Second, func() bool {
		s := n.live()
		d := 0
		for _, l := range s.Lanes {
			if l.Draining {
				d++
			}
		}
		return s.Slots == 1 && d == 1
	})
	s := n.live()
	var drain, keep liveLane
	for _, l := range s.Lanes {
		if l.Draining {
			drain = l
		} else {
			keep = l
		}
	}
	if drain.ID == "" || keep.ID == "" || drain.Slot != 2 {
		t.Fatalf("lanes %s: want lane 2 draining, lane 1 busy", s.lanesText())
	}
	free(drain.ID)
	waitFor(t, "the draining lane to close", 20*time.Second, func() bool { l := n.live().Lanes; return len(l) == 2 && l[drain.Slot-1].Off })
	time.Sleep(time.Second) // a closed lane takes nothing
	if s := n.live(); s.Lanes[drain.Slot-1].ID != "" || len(h.leases()) != 1 {
		t.Fatalf("the closed lane took a job: %s, leases %v", s.lanesText(), h.leases())
	}

	// The other lane keeps claiming, one job at a time.
	free(keep.ID)
	waitFor(t, "the open lane to take the next job", 20*time.Second, func() bool {
		b := busy(n.live())
		return len(b) == 1 && b[0] != keep.ID && b[0] != drain.ID
	})
	next := busy(n.live())[0]
	waitFor(t, next+" to start", 10*time.Second, func() bool { return strings.TrimSpace(read(n.state("lane-"+next))) != "" })
	if got := strings.TrimSpace(read(n.state("lane-" + next))); got != fmt.Sprint(keep.Slot) {
		t.Errorf("%s ran on lane %s, want lane %d", next, got, keep.Slot)
	}
	if l := h.leases(); len(l) != 1 {
		t.Errorf("leases after closing a lane: %v", l)
	}

	// A key the connection depends on waits for a restart.
	if out, _ := n.sat("hello", "config", "TRANSPORT=exec"); !strings.Contains(out, "starts again") {
		t.Errorf("config TRANSPORT: %s", out)
	}
	waitFor(t, "TRANSPORT pending", 10*time.Second, func() bool { return slices.Contains(n.live().Pending, "TRANSPORT") })
	if !strings.Contains(a.out.String(), "TRANSPORT (applies when the service starts again") {
		t.Errorf("not logged:\n%s", a.out.String())
	}
	if !strings.Contains(a.out.String(), "SLOTS 2 -> 1: lane 2 finishes its job, then closes") {
		t.Errorf("the drain is not logged:\n%s", a.out.String())
	}
	for _, j := range []string{"j1", "j2", "j3", "j4"} {
		free(j)
	}
	waitFor(t, "every job done", 30*time.Second, func() bool {
		for _, j := range []string{"j1", "j2", "j3", "j4"} {
			if !exists(h.hello + "/done/" + j) {
				return false
			}
		}
		return true
	})
}
