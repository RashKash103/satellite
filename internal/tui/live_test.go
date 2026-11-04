package tui

import (
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
