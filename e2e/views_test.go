package e2e

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A job that waits for go-ID, then end-ID, saying where it is.
const steppedJob = `#!/usr/bin/env bash
id=$1
echo "$id: step one"
echo "30% step one" >> "$SAT_PROGRESS"
while [ ! -e "$SAT_BASE/state/go-$id" ]; do sleep 0.1; done
echo "$id: step two"
while [ ! -e "$SAT_BASE/state/end-$id" ]; do sleep 0.1; done
echo "stepped $id" > "$SAT_RESULT"
`

// The hello example's views, run by sat against the real agent and the bash hub behind the gate.
func TestViewsThroughTheAgent(t *testing.T) {
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": steppedJob}), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=1\n")
	h.queue("j1", "1")
	h.queue("j2", "1")
	n.agent()
	waitFor(t, "j1 to start", 30*time.Second, func() bool {
		out, _ := n.sat("hello", "status")
		return strings.Contains(out, "lane 1: j1") && strings.Contains(out, "30% step one")
	})
	out, err := n.sat("hello", "views")
	for _, w := range []string{"board   Board   project  snapshot  json", "output  Output  lane     stream    text", "queue   Queue   project  snapshot  text"} {
		if err != nil || !strings.Contains(out, w) {
			t.Errorf("views lacks %q: %v\n%s", w, err, out)
		}
	}
	// The project views ask the hub through sat-call, as a job does.
	out, err = n.sat("hello", "view", "queue")
	if err != nil || !strings.Contains(out, "queue    1\nrunning  1\ndone     0\nfailed   0\n") || !strings.Contains(out, "this node: 0 passed, 0 failed") {
		t.Errorf("queue: %v\n%s", err, out)
	}
	out, err = n.sat("hello", "view", "board")
	if err != nil || !strings.Contains(out, "finished 0% 0 of 2 jobs") || !strings.Contains(out, "queued   1\nrunning  1\npassed   0") || !strings.Contains(out, "queued 1 jobs") {
		t.Errorf("board: %v\n%s", err, out)
	}
	// The lane view: once, then followed.
	out, err = n.sat("hello", "view", "output")
	if err != nil || !strings.Contains(out, "job j1 on lane 1\n") || !strings.Contains(out, "j1: step one") {
		t.Errorf("output once: %v\n%s", err, out)
	}
	c := exec.Command(filepath.Join(n.home, ".sat", "bin", "sat"), "hello", "view", "output", "-w")
	c.Env = n.env()
	pipe, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 100)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	saw := func(want string) {
		t.Helper()
		end := time.After(20 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("the view ended before %q", want)
				}
				if strings.Contains(l, want) {
					return
				}
			case <-end:
				t.Fatalf("no %q from the followed view", want)
			}
		}
	}
	saw("j1: step one")
	os.WriteFile(n.state("go-j1"), nil, 0o644)
	saw("j1: step two") // it follows the job as it goes
	c.Process.Signal(syscall.SIGINT)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ctrl+c: %v", err)
		}
	case <-time.After(10 * time.Second):
		c.Process.Kill()
		t.Fatal("the followed view did not stop on ctrl+c")
	}
	os.WriteFile(n.state("end-j1"), nil, 0o644)
	waitFor(t, "j1 done", 30*time.Second, func() bool { return exists(h.hello + "/done/j1") })
	waitFor(t, "the output view of j2", 30*time.Second, func() bool {
		out, _ := n.sat("hello", "view", "output", "--lane", "1")
		return strings.Contains(out, "job j2 on lane 1")
	})
	os.WriteFile(n.state("go-j2"), nil, 0o644)
	os.WriteFile(n.state("end-j2"), nil, 0o644)
	waitFor(t, "j2 done", 30*time.Second, func() bool { return exists(h.hello + "/done/j2") })
	waitFor(t, "the lane to be idle", 30*time.Second, func() bool {
		out, _ := n.sat("hello", "view", "output")
		return strings.Contains(out, "no lane has a job now")
	})
}

// The agent raises an alert, notifies it once (NOTIFY=on), lists it, and clears it.
func TestAlertsThroughTheAgent(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "MIN_FREE_PCT=100\n") // no room: the disk holds work back
	bin := filepath.Join(n.home, "bin")
	os.MkdirAll(bin, 0o755)
	log := filepath.Join(n.home, "notified")
	os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\necho \"$*\" >> "+log+"\n"), 0o755)
	os.WriteFile(filepath.Join(n.home, ".sat", "config"), []byte("NOTIFY=on\n"), 0o644)
	n.extraEnv = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent"}
	h.queue("j1", "10")
	n.agent()
	waitFor(t, "the alert", 20*time.Second, func() bool { out, _ := n.sat("hello", "alerts"); return strings.Contains(out, "not taking work:") })
	out, _ := n.sat("hello", "alerts")
	if !strings.Contains(out, "PROJECT  LEVEL  SINCE  ALERT\nhello    warn") {
		t.Errorf("alerts:\n%s", out)
	}
	if out, _ := n.sat("hello", "status"); !strings.Contains(out, "alert: warn: not taking work:") {
		t.Errorf("status:\n%s", out)
	}
	time.Sleep(2 * time.Second) // several more rounds of the state
	if b := read(log); strings.Count(b, "\n") != 1 || !strings.Contains(b, "-a satellite -u normal sat hello not taking work:") {
		t.Errorf("notified %q", b)
	}
	// Room again: it clears.
	f, _ := os.OpenFile(filepath.Join(n.base(), "config"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("MIN_FREE_PCT=0\n")
	f.Close()
	waitFor(t, "the alert to clear", 30*time.Second, func() bool { out, _ := n.sat("hello", "alerts"); return out == "no alerts\n" })
	waitFor(t, "j1 done", 30*time.Second, func() bool { return exists(h.hello + "/done/j1") })
}
