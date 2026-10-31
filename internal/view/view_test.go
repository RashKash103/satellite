package view

import (
	"context"
	"errors"
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

func TestHeaderDefaults(t *testing.T) {
	v, err := Header("q", []string{"#!/bin/sh", "echo hi"})
	if err != nil || v.Title != "q" || v.Scope != ScopeProject || v.Mode != ModeSnapshot || v.Format != FormatText || v.Every != 5*time.Second || v.Timeout != 10*time.Second {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestHeaderKeys(t *testing.T) {
	v, err := Header("q", []string{"#!/bin/sh", "# sat-view: title=The queue ; scope=lane;mode=stream; every=2.5; timeout=1; format=json; colour=yes;"})
	if err != nil || v.Title != "The queue" || v.Scope != ScopeLane || v.Mode != ModeStream || v.Format != FormatJSON || v.Every != 2500*time.Millisecond || v.Timeout != time.Second {
		t.Fatalf("%+v %v", v, err)
	}
	// Only the first 10 lines are read, and only the first header line.
	ls := make([]string, 10)
	ls = append(ls, "# sat-view: scope=lane")
	if v, _ := Header("q", ls); v.Scope != ScopeProject {
		t.Error("read past line 10")
	}
	if v, _ := Header("q", []string{"#sat-view: title=a", "# sat-view: title=b"}); v.Title != "a" {
		t.Errorf("title %q", v.Title)
	}
}

func TestHeaderBadValues(t *testing.T) {
	for _, h := range []string{"scope=node", "mode=poll", "format=xml", "every=0", "every=abc", "timeout=-1", "title=", "scope", "every=100000"} {
		if _, err := Header("q", []string{"# sat-view: " + h}); err == nil {
			t.Errorf("%s: no error", h)
		}
	}
}

// project: a temp HOME with project p and views written to its bundle.
func project(t *testing.T, views map[string]string) *conf.Project {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	base := conf.Dir("p")
	os.MkdirAll(filepath.Join(base, "state"), 0o755)
	os.MkdirAll(filepath.Join(base, "bundle", "sat", "views"), 0o755)
	os.WriteFile(filepath.Join(base, "config"), []byte("HUB=sat-hub-192.0.2.1\nSAT_NAME=n1\nFROM_CONFIG=c\n"), 0o644)
	os.WriteFile(filepath.Join(base, "env"), []byte("export FROM_ENV=\"$FROM_CONFIG-e\"\n"), 0o644)
	for n, body := range views {
		os.WriteFile(filepath.Join(base, "bundle", "sat", "views", n), []byte(body), 0o755)
	}
	p, err := conf.Load("p")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestList(t *testing.T) {
	p := project(t, map[string]string{"b": "#!/bin/sh\n", "a": "#!/bin/sh\n# sat-view: mode=sideways\n", "bad name": "#!/bin/sh\n"})
	os.WriteFile(filepath.Join(Dir(p), "notexec"), []byte("#!/bin/sh\n"), 0o644)
	os.WriteFile(filepath.Join(Dir(p), ".hidden"), []byte("#!/bin/sh\n"), 0o755)
	vs := List(p)
	if len(vs) != 2 || vs[0].Name != "a" || vs[1].Name != "b" || vs[0].Err == nil || vs[1].Err != nil {
		t.Fatalf("%+v", vs)
	}
	s := Sig(p)
	os.WriteFile(filepath.Join(Dir(p), "c"), []byte("#!/bin/sh\n"), 0o755)
	if Sig(p) == s {
		t.Error("the signature missed a new view")
	}
}

func run(t *testing.T, p *conf.Project, name string, o Opts) (string, error) {
	t.Helper()
	v, ok := Find(p, name)
	if !ok {
		t.Fatal("no view", name)
	}
	var out strings.Builder
	err := Run(context.Background(), p, v, o, &out, &out)
	return out.String(), err
}

func TestEnvironment(t *testing.T) {
	p := project(t, map[string]string{
		"env":  "#!/usr/bin/env bash\necho \"mode=$SAT_VIEW_MODE width=$SAT_VIEW_WIDTH color=${SAT_VIEW_COLOR-unset} once=${SAT_VIEW_ONCE-unset} view=$SAT_VIEW cfg=$FROM_CONFIG env=$FROM_ENV project=$SAT_PROJECT path=${PATH%%:*} slot=${SAT_SLOT-unset} job=${SAT_JOB-unset}\"\n",
		"lane": "#!/usr/bin/env bash\n# sat-view: scope=lane; mode=stream\necho \"slot=$SAT_SLOT job=$SAT_JOB claim=$SAT_CLAIM log=$SAT_LOG progress=$SAT_PROGRESS started=$SAT_STARTED once=${SAT_VIEW_ONCE-unset}\"\n",
	})
	t.Setenv("SAT_VIEW_COLOR", "1") // inherited ones never leak through
	t.Setenv("SAT_JOB", "stale")
	out, err := run(t, p, "env", Opts{Width: 50})
	want := "mode=once width=50 color=unset once=unset view=env cfg=c env=c-e project=p path=" + conf.Bin() + " slot=unset job=unset\n"
	if err != nil || out != want {
		t.Fatalf("%q %v\nwant %q", out, err, want)
	}
	if out, _ := run(t, p, "env", Opts{Live: true, Color: true}); !strings.Contains(out, "mode=live width=80 color=1 once=unset") {
		t.Error(out)
	}
	st := time.Unix(1700000000, 0)
	l := &Lane{Slot: 2, Job: "j9", Claim: "j9 abc", Started: st, Log: "/x/slot2.log", Progress: "/x/slot2.progress"}
	if out, _ := run(t, p, "lane", Opts{Lane: l}); out != "slot=2 job=j9 claim=j9 abc log=/x/slot2.log progress=/x/slot2.progress started=1700000000 once=1\n" {
		t.Errorf("%q", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	v, _ := Find(p, "lane")
	var b strings.Builder
	Run(ctx, p, v, Opts{Live: true, Lane: l}, &b, &b)
	if !strings.Contains(b.String(), "once=unset") {
		t.Errorf("live stream: %q", b.String())
	}
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func TestTimeoutKillsTheGroup(t *testing.T) {
	p := project(t, map[string]string{"slow": "#!/usr/bin/env bash\n# sat-view: timeout=0.3\necho started\nsleep 30 & echo $! > \"$SAT_BASE/state/child\"\nsleep 30\n"})
	t0 := time.Now()
	out, err := run(t, p, "slow", Opts{})
	var te TimeoutError
	if !errors.As(err, &te) || !strings.Contains(out, "started") || time.Since(t0) > 5*time.Second {
		t.Fatalf("%q %v after %s", out, err, time.Since(t0))
	}
	if Describe(err) != "timed out after 0.3s and was stopped" {
		t.Error(Describe(err))
	}
	b, _ := os.ReadFile(filepath.Join(p.State, "child"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	time.Sleep(100 * time.Millisecond)
	if pid == 0 || alive(pid) {
		t.Errorf("the view's child %d outlived it", pid)
	}
}

func TestOnceAndLive(t *testing.T) {
	p := project(t, map[string]string{
		"snap":   "#!/bin/sh\n# sat-view: timeout=5\necho \"$SAT_VIEW_MODE\"\n",
		"stream": "#!/usr/bin/env bash\n# sat-view: mode=stream; timeout=5\necho first\n[ -n \"$SAT_VIEW_ONCE\" ] && exit 0\necho $$ > \"$SAT_BASE/state/pid\"\nwhile :; do echo tick; sleep 0.1; done\n",
	})
	if out, err := run(t, p, "snap", Opts{}); out != "once\n" || err != nil {
		t.Error(out, err)
	}
	if out, err := run(t, p, "snap", Opts{Live: true}); out != "live\n" || err != nil {
		t.Error(out, err)
	}
	if out, err := run(t, p, "stream", Opts{}); out != "first\n" || err != nil {
		t.Errorf("once: %q %v", out, err)
	}
	// Live, a stream runs past its timeout until it is stopped.
	ctx, cancel := context.WithCancel(context.Background())
	v, _ := Find(p, "stream")
	lines := make(chan string, 100)
	w := &Lines{F: func(l string) { lines <- l }}
	done := make(chan error, 1)
	go func() { done <- Run(ctx, p, v, Opts{Live: true}, w, w) }()
	n := 0
	for l := range lines {
		if l == "tick" {
			n++
		}
		if n == 3 {
			break
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Error(err)
	}
	b, _ := os.ReadFile(filepath.Join(p.State, "pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if pid == 0 || alive(pid) {
		t.Error("the stream outlived its view")
	}
}

func TestExitAndBadHeader(t *testing.T) {
	p := project(t, map[string]string{"fail": "#!/bin/sh\necho oops; exit 3\n", "bad": "#!/bin/sh\n# sat-view: scope=x\n"})
	out, err := run(t, p, "fail", Opts{})
	var x *exec.ExitError
	if out != "oops\n" || !errors.As(err, &x) || Describe(err) != "exited 3" {
		t.Error(out, err)
	}
	if _, err := run(t, p, "bad", Opts{}); err == nil || !strings.Contains(err.Error(), "scope=x") {
		t.Error(err)
	}
}

func TestClean(t *testing.T) {
	in := "\x1b[1;31mred\x1b[0m\tx\x1b[2J\x1b[H\x1b]0;title\x07 \x1b[?25lend\x00"
	if got := Clean(in, false); got != "red     x end" {
		t.Errorf("plain: %q", got)
	}
	if got := Clean(in, true); got != "\x1b[1;31mred\x1b[0m     x end" {
		t.Errorf("colour: %q", got)
	}
	if got := Clean("10%\r20%\r30% done\r", false); got != "30% done" {
		t.Errorf("cr: %q", got)
	}
	var got []string
	w := &Lines{F: func(l string) { got = append(got, l) }}
	w.Write([]byte("a\nb"))
	w.Write([]byte("c\n\x1b[32md"))
	w.Flush()
	if strings.Join(got, "|") != "a|bc|d" {
		t.Errorf("%q", got)
	}
}
