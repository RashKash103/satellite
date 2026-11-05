package tui

import (
	tea "charm.land/bubbletea/v2"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
)

// editState changes hello's agent.json as a live agent would have written it.
func editState(t *testing.T, f func(*agent.State)) {
	t.Helper()
	p := filepath.Join(conf.Dir("hello"), "state", "agent.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var st agent.State
	json.Unmarshal(b, &st)
	f(&st)
	st.Updated = time.Now()
	b, _ = json.Marshal(st)
	os.WriteFile(p, b, 0o644)
}

// SLOTS went down: a draining lane is marked, a closed one is gone, the details count them.
func TestLiveSlots(t *testing.T) {
	m := node(t)
	editState(t, func(st *agent.State) {
		for len(st.Lanes) < 3 {
			st.Lanes = append(st.Lanes, agent.Lane{Slot: len(st.Lanes) + 1, Percent: -1})
		}
		st.Lanes[0].Draining = true
		st.Lanes[2].Off = true
		st.Slots = 1
	})
	m.refresh()
	v := screen(m)
	if !strings.Contains(v, "1 job42 3m00s draining") {
		t.Errorf("the draining lane is not marked:\n%s", v)
	}
	if strings.Contains(v, "\n│ 3 ") || len(m.lanes()) != 2 {
		t.Errorf("the closed lane is listed (lanes %v):\n%s", m.lanes(), v)
	}
	if !strings.Contains(v, "lanes   2 (1 draining: closes after") {
		t.Errorf("no lane count in the details:\n%s", v)
	}
	if strings.Contains(v, "restart!") {
		t.Error("a restart marker with nothing pending")
	}
}

// A key that applies at the next start: marked in the projects list, named in the details.
func TestPendingRestart(t *testing.T) {
	m := node(t)
	editState(t, func(st *agent.State) { st.Pending = []string{"HUB", "TRANSPORT"} })
	m.refresh()
	v := screen(m)
	row := ""
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "hello") && strings.Contains(l, "running") {
			row = l
		}
	}
	if !strings.Contains(row, "restart!") {
		t.Errorf("no marker in the projects list (%q):\n%s", row, v)
	}
	if !strings.Contains(m.View().Content, m.st.Warn.Render("restart!")) {
		t.Error("the marker is not in the warning colour")
	}
	if !strings.Contains(v, "HUB, TRANSPORT changed") || !strings.Contains(v, "sat hello stop && sat hello") {
		t.Errorf("the details don't say what is pending or how to apply it:\n%s", v)
	}
	editState(t, func(st *agent.State) { st.Pending = nil })
	m.refresh()
	if strings.Contains(screen(m), "restart!") {
		t.Error("the marker outlived the pending change")
	}
}

// The lanes box fits the open lanes: lowering SLOTS live (a lane closed) makes it smaller, as a
// project started with fewer lanes would have it.
func TestLanesBoxShrinks(t *testing.T) {
	rows := func(v string) int {
		ls := strings.Split(v, "\n")
		a, b := -1, -1
		for i, l := range ls {
			if a < 0 && strings.Contains(l, "Lanes") {
				a = i
			} else if a >= 0 && strings.Contains(l, "Details") {
				b = i
				break
			}
		}
		if a < 0 || b < 0 {
			t.Fatalf("no lanes or details box:\n%s", v)
		}
		return b - a
	}
	m := node(t)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 50}) // tall: the lanes box is sized to its lanes
	editState(t, func(st *agent.State) {
		for len(st.Lanes) < 3 {
			st.Lanes = append(st.Lanes, agent.Lane{Slot: len(st.Lanes) + 1, Percent: -1})
		}
		st.Slots = 3
	})
	m.refresh()
	three := rows(screen(m))
	editState(t, func(st *agent.State) { st.Lanes[2].Off = true; st.Slots = 2 })
	m.refresh()
	if two := rows(screen(m)); two != three-1 {
		t.Errorf("the lanes box took %d rows with 3 lanes open and %d with 2 (want one fewer)", three, two)
	}
}
