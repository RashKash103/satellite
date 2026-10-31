package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RashKash103/satellite/internal/conf"
)

func TestEnvCmd(t *testing.T) {
	d := t.TempDir()
	p := &conf.Project{Name: "p", Base: d}
	f := filepath.Join(d, "env")
	os.WriteFile(f, []byte("# mine\nexport A=1\nB=2\n"), 0o644)
	envCmd(p, []string{"A=it's $HOME"})
	envCmd(p, []string{"-B"})
	envCmd(p, []string{"C=x y"})
	got, _ := os.ReadFile(f)
	want := "# mine\nexport A='it'\\''s $HOME'\nexport C='x y'\n"
	if string(got) != want {
		t.Fatalf("%q", got)
	}
	out, err := exec.Command("bash", "-c", `. "$1"; printf '%s|%s' "$A" "$C"`, "x", f).Output()
	if err != nil || string(out) != "it's $HOME|x y" {
		t.Fatalf("sourced: %q %v", out, err)
	}
	if envCmd(p, []string{"1X=2"}) == 0 || envCmd(p, []string{"X"}) == 0 {
		t.Fatal("bad input accepted")
	}
}

func TestAlias(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config")
	os.WriteFile(f, []byte("Host a\n  HostName 1\n"), 0o600)
	addAlias(f, "sat-hub-x", "x", "u", "/k")
	addAlias(f, "sat-hub-x", "x", "u", "/k")
	b, _ := os.ReadFile(f)
	if strings.Count(string(b), "Host sat-hub-x") != 1 {
		t.Fatal(string(b))
	}
	os.WriteFile(f, append(b, "Host z\n  User q\n"...), 0o600)
	if ok, _ := removeAlias(f, "sat-hub-x"); !ok {
		t.Fatal("not found")
	}
	if b, _ := os.ReadFile(f); string(b) != "Host a\n  HostName 1\nHost z\n  User q\n" {
		t.Fatalf("%q", b)
	}
	if ok, _ := removeAlias(f, "sat-hub-x"); ok {
		t.Fatal("removed twice")
	}
}

func TestBashOurs(t *testing.T) {
	d := t.TempDir()
	for _, f := range []string{"sat", "sat-agent", "sat-call", "sat-lib"} {
		if !bashOurs(filepath.Join("..", "..", "node", f)) {
			t.Errorf("node/%s not recognised", f)
		}
		os.WriteFile(filepath.Join(d, f), []byte("#!/bin/sh\n# something else\n"), 0o755)
		if bashOurs(filepath.Join(d, f)) {
			t.Errorf("%s taken for ours", f)
		}
	}
}
