package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// lanes builds lane states from a pattern: B busy, I idle, D busy and retiring (draining), C closed.
func lanes(pat string) []laneState {
	var ls []laneState
	for _, c := range pat {
		switch c {
		case 'B':
			ls = append(ls, laneState{open: true, busy: true})
		case 'I':
			ls = append(ls, laneState{open: true})
		case 'D':
			ls = append(ls, laneState{open: true, retiring: true, busy: true})
		case 'C':
			ls = append(ls, laneState{})
		}
	}
	return ls
}

func TestPlanSlots(t *testing.T) {
	for _, c := range []struct {
		lanes string
		want  int
		plan  string // unretire reopen retire add (lane indexes from 0)
	}{
		{"I", 2, "[] [] [] 1"},
		{"B", 3, "[] [] [] 2"},
		{"BB", 1, "[] [] [1] 0"},     // both busy: the highest drains
		{"BI", 1, "[] [] [1] 0"},     // the idle one goes
		{"IB", 1, "[] [] [0] 0"},     // the idle one goes, though it is lower
		{"IIB", 1, "[] [] [1 0] 0"},  // idle ones, highest first
		{"BIB", 1, "[] [] [1 2] 0"},  // the idle one, then the highest busy
		{"BD", 2, "[1] [] [] 0"},     // raising cancels the drain first
		{"BC", 2, "[] [1] [] 0"},     // then reopens a closed lane
		{"BDC", 4, "[1] [2] [] 1"},   // all three, in that order
		{"BCC", 2, "[] [1] [] 0"},    // the lowest closed lane
		{"BB", 2, "[] [] [] 0"},      // as it is
		{"BD", 1, "[] [] [] 0"},      // already draining down to one
		{"B", 0, "[] [] [] 0"},       // never below one
		{"BBBB", 2, "[] [] [3 2] 0"}, // exactly as many as the decrease
		{"BBDC", 1, "[] [] [1] 0"},   // one open lane past the wanted one
	} {
		p := planSlots(lanes(c.lanes), c.want)
		got := fmt.Sprintf("%v %v %v %d", nz(p.unretire), nz(p.reopen), nz(p.retire), p.add)
		if got != c.plan {
			t.Errorf("planSlots(%s, %d) = %s, want %s", c.lanes, c.want, got, c.plan)
		}
	}
}

func nz(s []int) []int {
	if s == nil {
		return []int{}
	}
	return s
}

func TestConfDiff(t *testing.T) {
	old := map[string]string{"HUB": "hub", "SLOTS": "2", "STALL_MIN": "30", "GONE": "x"}
	cur := map[string]string{"HUB": "hub2", "SLOTS": "3", "STALL_MIN": "30", "TRANSPORT": "exec", "NEW": "y"}
	live, restart := confDiff(old, cur)
	if !slices.Equal(live, []string{"GONE", "NEW", "SLOTS"}) {
		t.Errorf("live = %v", live)
	}
	if !slices.Equal(restart, []string{"HUB", "TRANSPORT"}) {
		t.Errorf("restart = %v", restart)
	}
	if l, r := confDiff(old, old); l != nil || r != nil {
		t.Errorf("no change: %v %v", l, r)
	}
}

// A lane napping (waiting for disk room, say) wakes when a live setting changes, not at the end of
// its nap: MAX_GB raised, it looks again at once.
func TestLaneNapWakesOnConfigChange(t *testing.T) {
	p := testProject(t, "MAX_GB=10\n") // no SLOTS: a bare Agent has no hub to open a lane on
	a := &Agent{P: p}
	ctx := context.Background()
	a.reload(ctx)
	woke := make(chan time.Duration, 1)
	go func() { t0 := time.Now(); a.laneNap(ctx, 0, 30*time.Second); woke <- time.Since(t0) }()
	time.Sleep(300 * time.Millisecond)
	cfg := filepath.Join(p.Base, "config")
	if err := os.WriteFile(cfg, []byte("MAX_GB=20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	os.Chtimes(cfg, later, later) // a new mtime even on a coarse clock
	a.reload(ctx)
	select {
	case d := <-woke:
		if d > 5*time.Second {
			t.Fatalf("woke after %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the nap did not end when MAX_GB changed")
	}
	// An unchanged file wakes nothing.
	go func() { t0 := time.Now(); a.laneNap(ctx, 0, 700*time.Millisecond); woke <- time.Since(t0) }()
	time.Sleep(100 * time.Millisecond)
	a.reload(ctx)
	if d := <-woke; d < 600*time.Millisecond {
		t.Fatalf("an unchanged config ended the nap after %v", d)
	}
}
