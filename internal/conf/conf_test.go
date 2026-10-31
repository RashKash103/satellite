package conf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseLine(t *testing.T) {
	env := map[string]string{"HOME": "/h", "PATH": "/usr/bin"}
	get := func(k string) string { return env[k] }
	for _, c := range []struct{ in, k, v string }{
		{"HUB=sat-hub-192.0.2.1", "HUB", "sat-hub-192.0.2.1"},
		{"SAT_NAME=box   # bound to this node's key on the hub", "SAT_NAME", "box"},
		{"export PATH=$HOME/bin:$PATH", "PATH", "/h/bin:/usr/bin"},
		{`X="a b $HOME"`, "X", "a b /h"},
		{`Y='a $HOME'`, "Y", "a $HOME"},
		{"Z=${HOME}x", "Z", "/hx"},
		{"E=", "E", ""},
		{`W=a\ b`, "W", "a b"},
	} {
		k, v, ok := ParseLine(c.in, get)
		if !ok || k != c.k || v != c.v {
			t.Errorf("%q: got %q=%q %v, want %q=%q", c.in, k, v, ok, c.k, c.v)
		}
	}
	for _, bad := range []string{"# comment", "", "echo hi", "1X=2", `Q="open`} {
		if _, _, ok := ParseLine(bad, get); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestParseUsesEarlierLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c")
	os.WriteFile(p, []byte("A=1\nB=$A-2\n# x\nSLOTS=2\n"), 0o644)
	m, err := Parse(p, nil)
	if err != nil || m["B"] != "1-2" || m["SLOTS"] != "2" {
		t.Fatal(m, err)
	}
}

func TestNameOK(t *testing.T) {
	for _, s := range []string{"hello", "a.b-c_1"} {
		if !NameOK(s) {
			t.Error(s)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", "a b", "x;y"} {
		if NameOK(s) {
			t.Error(s)
		}
	}
}

func TestList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	os.MkdirAll(filepath.Join(Projects(), "b"), 0o755)
	os.WriteFile(filepath.Join(Projects(), "b", "config"), nil, 0o644)
	os.MkdirAll(filepath.Join(Projects(), "a"), 0o755)
	os.WriteFile(filepath.Join(Projects(), "a", "config"), []byte("SAT_NAME=n\n"), 0o644)
	os.MkdirAll(filepath.Join(Projects(), "noconf"), 0o755)
	if l := List(); len(l) != 2 || l[0] != "a" || l[1] != "b" {
		t.Fatal(l)
	}
	p, err := Load("a")
	if err != nil || p.NodeName() != "n" || p.Unit() != "sat@a.service" {
		t.Fatal(p, err)
	}
}
