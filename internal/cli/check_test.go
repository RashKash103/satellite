package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RashKash103/satellite/internal/conf"
)

// capture runs f with os.Stdout going to a pipe and returns what it printed.
func capture(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

// testNode: a temp HOME with project "demo" whose sat/requirements wants a tool on PATH, and a
// systemctl whose user manager has only the system PATH (as a service gets: no shell profile).
func testNode(t *testing.T) (home string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, "fakebin")
	os.MkdirAll(bin, 0o755)
	sc := filepath.Join(bin, "systemctl")
	os.WriteFile(sc, []byte("#!/bin/sh\ncase \"$*\" in *show-environment*) printf 'PATH=/usr/bin:/bin\\nLANG=C.UTF-8\\nX=$'\"'\"'a\\\\tb'\"'\"'\\n' ;; *is-active*) echo inactive; exit 3 ;; *is-enabled*) echo disabled ;; esac\n"), 0o755)
	t.Setenv("SAT_SYSTEMCTL", sc)
	t.Setenv("SAT_JOURNALCTL", "true")
	// The tool is on this shell's PATH, as nvm or cargo would put it, but not the service's.
	shell := filepath.Join(home, "shellbin")
	os.MkdirAll(shell, 0o755)
	os.WriteFile(filepath.Join(shell, "demotool"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(shell, "ssh"), []byte("#!/bin/sh\necho 'no route to the test hub' >&2\nexit 255\n"), 0o755)
	t.Setenv("PATH", shell+":"+os.Getenv("PATH"))
	base := conf.Dir("demo")
	os.MkdirAll(filepath.Join(base, "bundle", "sat"), 0o755)
	os.MkdirAll(filepath.Join(base, "state"), 0o755)
	os.WriteFile(filepath.Join(base, "config"), []byte("HUB=sat-hub-192.0.2.1\nSAT_NAME=n1\n"), 0o644)
	os.WriteFile(filepath.Join(base, "bundle", "sat", "requirements"),
		[]byte("#!/bin/sh\ncommand -v demotool >/dev/null || echo 'note demotool is not on PATH'\n[ \"$X\" = \"$(printf 'a\\tb')\" ] || echo \"note X is $X\"\n"), 0o755)
	return home
}

func TestCheckUsesTheServiceEnvironment(t *testing.T) {
	home := testNode(t)
	var rc int
	out := capture(t, func() { rc = project("demo", "check", nil) })
	if rc != 1 || !strings.Contains(out, "demotool is not on PATH") || !strings.Contains(out, "the environment the service runs with") {
		t.Fatalf("rc %d, the shell's PATH counted:\n%s", rc, out)
	}
	if strings.Contains(out, "X is") {
		t.Errorf("a $'...' value was not unescaped:\n%s", out)
	}
	out = capture(t, func() { rc = start(mustLoad(t, "demo")) })
	if rc != 1 || !strings.Contains(out, "demotool") {
		t.Fatalf("start's check passed in the shell's environment: %d\n%s", rc, out)
	}
	out = capture(t, func() { status(mustLoad(t, "demo")) })
	if !strings.Contains(out, "requirements: 1 missing") {
		t.Errorf("status:\n%s", out)
	}
	// The project's env file is where such PATH additions belong; the check then sees it.
	os.WriteFile(filepath.Join(conf.Dir("demo"), "env"), []byte("PATH="+filepath.Join(home, "shellbin")+":$PATH\n"), 0o644)
	out = capture(t, func() { rc = project("demo", "check", nil) })
	if rc != 0 || !strings.Contains(out, "has everything it needs") {
		t.Fatalf("with the env file: %d\n%s", rc, out)
	}
}

func TestCheckWithoutUserManager(t *testing.T) {
	testNode(t)
	t.Setenv("SAT_SYSTEMCTL", "false")
	var rc int
	out := capture(t, func() { rc = project("demo", "check", nil) })
	if !strings.Contains(out, "this shell's environment") {
		t.Fatalf("%d\n%s", rc, out)
	}
}

func mustLoad(t *testing.T, n string) *conf.Project {
	p, err := conf.Load(n)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
