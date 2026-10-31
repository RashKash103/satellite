package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
)

func jobProject(t *testing.T) *conf.Project {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "config"), []byte("X=1\n"), 0o644)
	return &conf.Project{Name: "p", Base: d, State: d}
}

// The wrapper records the job's exit status, and exits with it; a job's own set -e in env changes
// nothing.
func TestJobCommandRecordsTheExit(t *testing.T) {
	p := jobProject(t)
	os.WriteFile(filepath.Join(p.Base, "env"), []byte("set -e\n"), 0o644)
	for _, rc := range []int{0, 1, 3} {
		exit := filepath.Join(p.State, "exit")
		c := JobCommand([]string{"SAT_BASE=" + p.Base, "PATH=" + os.Getenv("PATH")}, exit, "bash", "-c", "exit "+strconv.Itoa(rc))
		err := c.Run()
		if c.ProcessState.ExitCode() != rc || exitStatus(exit) != rc {
			t.Errorf("rc %d: wait %v, file %d", rc, err, exitStatus(exit))
		}
		if _, err := os.Stat(exit + ".new"); err == nil {
			t.Error("left exit.new")
		}
	}
}

// A job that ended while no agent waited for it: still this process's child (a zombie), wait4
// gives its status.
func TestWaitJobChild(t *testing.T) {
	p := jobProject(t)
	exit := filepath.Join(p.State, "exit")
	c := JobCommand([]string{"SAT_BASE=" + p.Base, "PATH=" + os.Getenv("PATH")}, exit, "bash", "-c", "exit 1")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pi, err := readProc(c.Process.Pid)
	if err != nil || pi.pgid != c.Process.Pid || pi.start == 0 {
		t.Fatalf("readProc: %+v %v", pi, err)
	}
	j := &Job{PID: c.Process.Pid, PGID: c.Process.Pid, Start: pi.start, Exit: exit}
	for exitStatus(exit) == noExit {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if !j.is(true) || j.is(false) {
		t.Fatal("an exited child not yet waited for is a zombie of ours")
	}
	select {
	case rc := <-waitJob(j):
		if rc != 1 {
			t.Errorf("rc %d", rc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no status")
	}
	if j.is(true) {
		t.Error("still there after wait4")
	}
}

// A job that isn't this process's child (a grandchild here, reparented once its parent exits):
// watched through /proc, its status read from the file.
func TestWaitJobNotChild(t *testing.T) {
	p := jobProject(t)
	exit := filepath.Join(p.State, "exit")
	out, err := exec.Command("bash", "-c", `bash -c 'sleep 0.5; echo 1 > "$0"' "$1" >/dev/null 2>&1 & echo $!`, "x", exit).Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	pi, err := readProc(pid)
	if err != nil {
		t.Skip("the process ended before it was looked at")
	}
	j := &Job{PID: pid, PGID: pi.pgid, Start: pi.start, Exit: exit}
	select {
	case rc := <-waitJob(j):
		if rc != 1 {
			t.Errorf("rc %d", rc)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no status")
	}
	// another process with a recorded PID is not the job
	j2 := &Job{PID: os.Getpid(), PGID: syscall.Getpgrp(), Start: 1}
	if j2.is(true) {
		t.Error("a different start time passed")
	}
}

func TestFindJobs(t *testing.T) {
	p := jobProject(t)
	a := &Agent{P: p}
	f1, f2 := a.files(1), a.files(2)
	os.WriteFile(f1.claim, []byte("j1\n"), 0o644)
	os.WriteFile(f1.job, []byte(`{"id": "j1", "pid": 1, "pgid": 1, "start_ticks": 1}`), 0o644)
	os.WriteFile(f2.job, []byte(`{"id": "j2", "pid": 1, "pgid": 1, "start_ticks": 1}`), 0o644) // no claim: handed back already
	m := a.findJobs()
	if len(m) != 1 || m[1] == nil || m[1].ID != "j1" || m[1].alive {
		t.Fatalf("%+v", m)
	}
	if _, err := os.Stat(f2.job); err == nil {
		t.Error("the stale job file was kept")
	}
}

func TestRestartNote(t *testing.T) {
	for _, c := range []struct {
		h    *handoff
		n    int
		want string
	}{
		{nil, 0, ""},
		{nil, 2, "restarted, adopted 2 running jobs"},
		{&handoff{From: "v1.0.0"}, 1, "updated from v1.0.0 to v1.1.0, adopted 1 running job"},
		{&handoff{From: "v1.1.0"}, 0, "restarted for a replaced binary, adopted 0 running jobs"},
	} {
		if got := restartNote(c.h, "v1.1.0", c.n); got != c.want {
			t.Errorf("%+v %d: %q", c.h, c.n, got)
		}
	}
}
