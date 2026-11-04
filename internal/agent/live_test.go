package agent

import (
	"fmt"
	"slices"
	"testing"
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
