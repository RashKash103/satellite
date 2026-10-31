package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// heldJob: hello's job, except that the job "long" runs until state/go-long exists.
const heldJob = `#!/usr/bin/env bash
if [ "$1" = long ]; then
  echo $$ >> "$SAT_BASE/state/pid-long"
  while [ ! -e "$SAT_BASE/state/go-long" ]; do sleep 0.2; done
fi
echo "done $1" > "$SAT_RESULT"
`

// replaceBinary puts a fresh copy of the node's binary over ~/.sat/bin/sat (a new file renamed into
// place, as an update does).
func (n *node) replaceBinary() {
	n.t.Helper()
	exe := filepath.Join(n.home, ".sat", "bin", "sat")
	b, err := os.ReadFile(exe)
	if err != nil {
		n.t.Fatal(err)
	}
	tmp := exe + ".new"
	os.WriteFile(tmp, b, 0o755)
	if err := os.Rename(tmp, exe); err != nil {
		n.t.Fatal(err)
	}
}

func (h *hub) leaseTime(id string) time.Time {
	fi, err := os.Stat(filepath.Join(h.conf, "leases", "hello", id))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

func agentPID(n *node) int {
	var s struct{ PID int }
	json.Unmarshal([]byte(read(n.state("agent.json"))), &s)
	return s.PID
}

// adoptsLong: with "long" running on a, the binary is replaced by replace(): the agent re-executes
// at once (the same PID), adopts the job and keeps renewing its lease, an idle lane takes another
// job meanwhile, and the job's result is reported once.
func adoptsLong(t *testing.T, h *hub, n *node, a *running, replace func()) {
	waitFor(t, "the long job to start", 20*time.Second, func() bool { return exists(n.state("pid-long")) })
	var jobPID int
	fmt.Sscan(read(n.state("pid-long")), &jobPID)
	pid := a.cmd.Process.Pid
	replace()
	waitFor(t, "the job to be adopted", 20*time.Second, func() bool { return strings.Contains(a.out.String(), "adopted long") })
	select {
	case err := <-a.done:
		t.Fatalf("the agent exited (%v)", err)
	default:
	}
	if agentPID(n) != pid {
		t.Errorf("agent.json's pid %d, the agent's %d", agentPID(n), pid)
	}
	waitFor(t, "the restart note", 10*time.Second, func() bool {
		return strings.Contains(read(n.state("agent.json")), `adopted 1 running job"`)
	})
	if out, _ := n.sat("hello", "status"); !strings.Contains(out, "agent: ") || !strings.Contains(out, "adopted 1 running job") {
		t.Errorf("status:\n%s", out)
	}
	if syscall.Kill(jobPID, 0) != nil {
		t.Fatal("the job did not survive the restart")
	}
	adopted := time.Now()
	waitFor(t, "a renewal by the new agent", 15*time.Second, func() bool { return h.leaseTime("long").After(adopted) })
	h.queue("short", "7")
	waitFor(t, "the idle lane to take a job meanwhile", 30*time.Second, func() bool { return written(h.hello + "/done/short") })
	if syscall.Kill(jobPID, 0) != nil || exists(h.hello+"/done/long") {
		t.Fatal("the long job ended early")
	}
	os.WriteFile(n.state("go-long"), nil, 0o644)
	waitFor(t, "the long job's result", 30*time.Second, func() bool { return written(h.hello + "/done/long") })
	waitFor(t, "the lane to be free", 10*time.Second, func() bool { return !exists(n.state("slot1.job")) && !exists(n.state("slot2.job")) })
	time.Sleep(time.Second)
	if d := read(h.hello + "/done/long"); !strings.Contains(d, "done long (by nodeA)") || strings.Count(d, "(by ") != 1 {
		t.Errorf("done/long: %q", d)
	}
	if c := strings.Count(a.out.String(), "long passed"); c != 1 {
		t.Errorf("long passed %d times:\n%s", c, a.out.String())
	}
	if c := strings.Count(read(n.state("pid-long")), "\n"); c != 1 {
		t.Errorf("the long job ran %d times", c)
	}
	if l := h.leases(); len(l) != 0 {
		t.Errorf("leases left: %v", l)
	}
}

func TestReplacedBinaryAdoptsRunningJob(t *testing.T) {
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": heldJob}), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=2\n")
	h.queue("long", "1")
	a := n.agent()
	adoptsLong(t, h, n, a, n.replaceBinary)
	if !strings.Contains(a.out.String(), "the binary was replaced: restarting now; the 1 running job(s) keep running") {
		t.Errorf("agent log:\n%s", a.out.String())
	}
}

// sat self-update swaps the binary at once, with no drain, and the agent adopts the running job.
func TestSelfUpdateKeepsRunningJobs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": heldJob}), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=2\n")
	bin, _ := os.ReadFile(filepath.Join(n.home, ".sat", "bin", "sat"))
	sum := sha256.Sum256(bin)
	asset := "sat_linux_" + runtime.GOARCH
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/RashKash103/satellite/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name": "v9.9.9", "body": "notes", "assets": [{"name": %q, "browser_download_url": %q}, {"name": "checksums.txt", "browser_download_url": %q}]}`,
			asset, srv.URL+"/bin", srv.URL+"/sums")
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) { w.Write(bin) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	shim := filepath.Join(n.home, "shim")
	os.MkdirAll(shim, 0o755)
	os.WriteFile(filepath.Join(shim, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o755) // not logged in
	n.extraEnv = []string{"SAT_RELEASES_API=" + srv.URL, "FAKE_ACTIVE=1", "PATH=" + shim + ":" + os.Getenv("PATH")}
	h.queue("long", "1")
	a := n.agent()
	adoptsLong(t, h, n, a, func() {
		start := time.Now()
		out, err := n.sat("self-update", "--yes")
		if err != nil || !strings.Contains(out, "sat is now v9.9.9") || strings.Contains(out, "drain") {
			t.Fatalf("self-update: %v\n%s", err, out)
		}
		if !strings.Contains(out, "adopt their running jobs") {
			t.Errorf("self-update:\n%s", out)
		}
		if time.Since(start) > 15*time.Second || exists(n.state("drain")) {
			t.Fatal("self-update waited for the running job")
		}
	})
}

// A job file whose process is gone: with no exit status recorded, the job is handed back; with
// one, it is reported from it.
func TestAdoptEndedJob(t *testing.T) {
	for _, c := range []struct{ name, exit, want string }{{"no exit status", "", "left no exit status"}, {"exit 0", "0\n", "(exit 0)"}} {
		t.Run(c.name, func(t *testing.T) {
			h := newHub(t, hooks(), realGate())
			n := newNode(t, h, "nodeA", "SLOTS=1\n")
			h.queue("j1", "5")
			line, rc := n.call("sat-hub hello claim nodeA 1")
			if rc != 0 || strings.Fields(line)[0] != "j1" {
				t.Fatalf("claim: %d %q", rc, line)
			}
			gone := exec.Command("true")
			gone.Run()
			os.WriteFile(n.state("slot1.claim"), []byte("j1\n"), 0o644)
			j, _ := json.Marshal(map[string]any{"id": "j1", "line": strings.TrimSpace(line), "pid": gone.Process.Pid, "pgid": gone.Process.Pid, "start_ticks": 1,
				"started": time.Now().Add(-time.Minute), "exit": n.state("slot1.exit"), "result": n.state("slot1.result"), "progress": n.state("slot1.progress")})
			os.WriteFile(n.state("slot1.job"), j, 0o644)
			os.WriteFile(n.state("slot1.result"), []byte("sha256 recorded\n"), 0o644)
			if c.exit != "" {
				os.WriteFile(n.state("slot1.exit"), []byte(c.exit), 0o644)
			}
			os.WriteFile(n.state("hold"), nil, 0o644) // hello's sat/ready: no new claims
			a := n.agent()
			waitFor(t, "the ended job", 20*time.Second, func() bool { return strings.Contains(a.out.String(), "j1 ended while the agent restarted") })
			waitFor(t, "the lane to be free", 10*time.Second, func() bool { return !exists(n.state("slot1.claim")) && !exists(n.state("slot1.job")) })
			if !strings.Contains(a.out.String(), c.want) || strings.Contains(a.out.String(), "adopted j1") {
				t.Errorf("agent log:\n%s", a.out.String())
			}
			if c.exit == "" {
				if !exists(h.hello+"/queue/j1") || !strings.Contains(a.out.String(), "j1 handed back") {
					t.Error("not handed back")
				}
			} else if d := read(h.hello + "/done/j1"); !strings.Contains(d, "sha256 recorded (by nodeA)") {
				t.Errorf("done/j1: %q", d)
			}
			if l := h.leases(); len(l) != 0 {
				t.Errorf("leases left: %v", l)
			}
		})
	}
}
