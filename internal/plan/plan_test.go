package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAndShow(t *testing.T) {
	s := Parse("apt a b\nrun curl x | sh\nnote do it\nbogus line\n\n")
	if len(s) != 3 || s[0].Rest != "a b" || s[1].Rest != "curl x | sh" {
		t.Fatal(s)
	}
	out := Show(s)
	for _, want := range []string{"sudo apt-get install --no-remove a b", "  curl x | sh", "# by hand: do it"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestSim(t *testing.T) {
	out := "NOTE: x\nInst libfoo (1.2 Debian:stable [amd64])\nInst bar [1.0] (1.1 Debian [amd64])\nConf libfoo\n"
	i, u := Sim(out)
	if len(i) != 1 || i[0] != "libfoo" || len(u) != 1 || u[0] != "bar" {
		t.Fatal(i, u)
	}
}

func TestInstallWithoutTerminalRunsNothing(t *testing.T) {
	d := t.TempDir()
	mark := filepath.Join(d, "ran")
	var b strings.Builder
	called := false
	ok := Install(&b, strings.NewReader("y\n"), false, []Step{{"run", "touch " + mark}, {"note", "by hand"}}, func(string) { called = true })
	if ok || called || !strings.Contains(b.String(), "Not a terminal") {
		t.Fatal(b.String())
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("ran")
	}
}

func TestInstallConfirmed(t *testing.T) {
	d := t.TempDir()
	mark := filepath.Join(d, "ran")
	var b strings.Builder
	var log []string
	if !Install(&b, strings.NewReader("y\n"), true, []Step{{"run", "touch " + mark}}, func(c string) { log = append(log, c) }) {
		t.Fatal(b.String())
	}
	if _, err := os.Stat(mark); err != nil || len(log) != 1 {
		t.Fatal("not run", log)
	}
	if Install(&b, strings.NewReader("n\n"), true, []Step{{"run", "false"}}, func(string) {}) {
		t.Fatal("declined but ok")
	}
}

func TestOf(t *testing.T) {
	t.Setenv("SAT_SYSTEMCTL", "true")
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "sat"), 0o755)
	os.WriteFile(filepath.Join(d, "sat", "requirements"), []byte("#!/bin/sh\necho \"note $SAT_PROJECT\"\n"), 0o755)
	s := Of(d, append(os.Environ(), "SAT_PROJECT=pp"))
	if len(s) == 0 || s[len(s)-1] != (Step{"note", "pp"}) {
		t.Fatal(s)
	}
}
