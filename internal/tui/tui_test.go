package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
)

func TestView(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SAT_SYSTEMCTL", "true")
	base := conf.Dir("hello")
	os.MkdirAll(filepath.Join(base, "state"), 0o755)
	os.WriteFile(filepath.Join(base, "config"), []byte("HUB=sat-hub-192.0.2.1\nSLOTS=2\nMIN_FREE_GB=0\nMIN_FREE_PCT=0\n"), 0o644)
	now := time.Now()
	st := agent.State{Project: "hello", Running: true, Updated: now, HubOK: true, ProtocolOK: true, Protocol: "1", LastContact: now,
		Lanes: []agent.Lane{{Slot: 1, ID: "job42", Started: now.Add(-3 * time.Minute), Renewed: now, Percent: 40, Progress: "linking"}, {Slot: 2, Percent: -1}}}
	b, _ := json.Marshal(st)
	os.WriteFile(filepath.Join(base, "state", "agent.json"), b, 0o644)
	os.WriteFile(filepath.Join(base, "state", "slot1.log"), []byte("compiling a\ncompiling b\n"), 0o644)
	m := &model{version: "dev", exe: "/bin/false", logs: true}
	m.refresh()
	v := m.View().Content
	for _, w := range []string{"hello", "sat-hub-192.0.2.1", "reachable", "protocol 1", "job42", "3m0", "40%", "linking", "idle: waiting for work", "compiling b", "disk "} {
		if !strings.Contains(v, w) {
			t.Errorf("view lacks %q:\n%s", w, v)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'l', Text: "l"})
	if strings.Contains(m.View().Content, "compiling b") {
		t.Error("l did not hide the log")
	}
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if _, s := m.at(); s != 2 {
		t.Errorf("selection %d", s)
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if !strings.Contains(m.View().Content, "Stop hello now?") {
		t.Error("stop did not ask")
	}
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil || m.confirm != "" {
		t.Error("stopped without a yes")
	}
}
