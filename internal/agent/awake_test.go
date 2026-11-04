package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeInhibit stands in for systemd-inhibit: it writes "WHAT WHY" to a file while it holds, and holds
// until its stdin closes, as `systemd-inhibit ... cat` does; the kinds in refuse end at once instead.
func fakeInhibit(t *testing.T, refuse ...string) (mark string) {
	t.Helper()
	mark = filepath.Join(t.TempDir(), "held")
	oldCmd, oldCheck := inhibitCmd, holdCheck
	inhibitCmd = func(what, why string) *exec.Cmd {
		for _, r := range refuse {
			if what == r {
				return exec.Command("sh", "-c", "echo refused >&2; exit 1")
			}
		}
		return exec.Command("sh", "-c", `echo "$1 $2" > "$3"; cat > /dev/null; rm -f "$3"`, "sh", what, why, mark)
	}
	holdCheck = 100 * time.Millisecond
	t.Cleanup(func() { inhibitCmd, holdCheck = oldCmd, oldCheck })
	return mark
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestAwakeHeldWhileBusy(t *testing.T) {
	mark := fakeInhibit(t)
	var k awake
	var log []string
	logf := func(f string, v ...any) { log = append(log, f) }

	k.set(false, true, "idle", logf)
	if k.held() || exists(mark) {
		t.Fatal("held with no job running")
	}
	k.set(true, true, "sat demo: 1 job(s) running", logf)
	eventually(t, "the inhibitor to start", func() bool { return exists(mark) })
	if b, _ := os.ReadFile(mark); string(b) != "sleep:idle sat demo: 1 job(s) running\n" {
		t.Errorf("inhibitor = %q", b)
	}
	k.set(true, true, "sat demo: 2 job(s) running", logf) // still busy: the same inhibitor
	if !k.held() || len(log) != 1 {
		t.Fatalf("held=%v log=%v", k.held(), log)
	}
	k.set(false, true, "", logf)
	eventually(t, "the inhibitor to end", func() bool { return !exists(mark) })
	if k.held() || len(log) != 2 {
		t.Fatalf("held=%v log=%v", k.held(), log)
	}
}

func TestAwakeOff(t *testing.T) {
	mark := fakeInhibit(t)
	var k awake
	k.set(true, false, "busy", func(string, ...any) {})
	time.Sleep(150 * time.Millisecond)
	if k.held() || exists(mark) {
		t.Fatal("KEEP_AWAKE=off still held the machine awake")
	}
	k.set(true, true, "busy", func(string, ...any) {})
	eventually(t, "the inhibitor to start", func() bool { return exists(mark) })
	k.set(true, false, "busy", func(string, ...any) {}) // turned off while busy: let go
	eventually(t, "the inhibitor to end", func() bool { return !exists(mark) })
}

// No login session: logind refuses to block sleep, so the agent blocks idle (allowed to anyone).
func TestAwakeFallsBackToIdle(t *testing.T) {
	mark := fakeInhibit(t, "sleep:idle")
	var k awake
	var log []string
	k.set(true, true, "busy", func(f string, v ...any) { log = append(log, f) })
	eventually(t, "the idle inhibitor", func() bool { b, _ := os.ReadFile(mark); return strings.HasPrefix(string(b), "idle ") })
	if len(log) != 1 || !strings.Contains(log[0], "login session") {
		t.Errorf("log = %v", log)
	}
}

// Every kind refused: said once, not retried on each lane change, retried after the next idle spell.
func TestAwakeRefusedOnce(t *testing.T) {
	fakeInhibit(t, "sleep:idle", "idle")
	var k awake
	var log []string
	logf := func(f string, v ...any) { log = append(log, f) }
	k.set(true, true, "busy", logf)
	k.set(true, true, "busy", logf)
	if k.held() || len(log) != 1 || !strings.Contains(log[0], "refused") {
		t.Fatalf("held=%v log=%v", k.held(), log)
	}
	k.set(false, true, "", logf)
	k.set(true, true, "busy", logf)
	if len(log) != 2 {
		t.Fatalf("not retried after the lanes went idle: log=%v", log)
	}
}
