package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
)

// completeNode: viewNode's demo project (views, an env file, three lanes, lane 2 busy), and a
// second project, other.
func completeNode(t *testing.T) string {
	home := viewNode(t)
	base := conf.Dir("demo")
	os.WriteFile(filepath.Join(base, "config"), []byte("HUB=sat-hub-192.0.2.1\nHUB_IP=192.0.2.1\nHUB_USER=hubuser\nSAT_NAME=n1\nSLOTS=3\n"), 0o644)
	os.WriteFile(filepath.Join(base, "env"), []byte("# mine\nexport TOOL_HOME=\"$HOME/.tool\"\nPATH=$HOME/bin:$PATH\n"), 0o644)
	st := agent.State{Project: "demo", Running: true, Updated: time.Now(), Lanes: []agent.Lane{{Slot: 1, Percent: -1}, {Slot: 2, ID: "job7", Percent: -1}, {Slot: 3, Percent: -1}}}
	b, _ := json.Marshal(st)
	os.WriteFile(filepath.Join(base, "state", "agent.json"), b, 0o644)
	o := conf.Dir("other")
	os.MkdirAll(o, 0o755)
	os.WriteFile(filepath.Join(o, "config"), []byte("HUB=sat-hub-192.0.2.9\nHUB_IP=192.0.2.9\n"), 0o644)
	return home
}

// comp: the candidates sat __complete gives, by group: "tag: v1 v2 ...".
func comp(t *testing.T, svc string, words ...string) map[string][]string {
	t.Helper()
	out := capture(t, func() { Main("dev", append([]string{"sat", "__complete", svc}, words...)) })
	m := map[string][]string{}
	tag := ""
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l == "" {
			continue
		}
		if g, ok := strings.CutPrefix(l, "#"); ok {
			tag, _, _ = strings.Cut(g, " ")
			continue
		}
		v, d, ok := strings.Cut(l, "\t")
		if !ok || strings.Contains(d, "\t") {
			t.Fatalf("bad line %q", l)
		}
		m[tag] = append(m[tag], v)
	}
	return m
}

func has(t *testing.T, ctx string, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			t.Errorf("%s: no %q in %v", ctx, w, got)
		}
	}
}

func TestComplete(t *testing.T) {
	completeNode(t)
	m := comp(t, "sat", "")
	has(t, "first word: commands", m["commands"], "start", "drain", "env", "view", "autostart")
	has(t, "first word: node commands", m["node-commands"], "setup", "self-update", "uninstall", "completion", "help")
	has(t, "first word: projects", m["projects"], "demo", "other")
	if m := comp(t, "sat", "demo", ""); len(m["projects"]) > 0 || len(m["node-commands"]) > 0 || len(m["commands"]) < 10 {
		t.Errorf("after a project, only its commands: %v", m)
	}
	// both orders
	for _, w := range [][]string{{"demo", "view", ""}, {"view", "demo", ""}} {
		m := comp(t, "sat", w...)
		has(t, strings.Join(w, " "), m["views"], "board", "colour", "lane")
		if len(m["projects"]) > 0 {
			t.Errorf("%v: projects after one", w)
		}
	}
	has(t, "sat start: a project may follow", comp(t, "sat", "start", "")["projects"], "demo", "other")
	has(t, "flags", comp(t, "sat", "demo", "view", "board", "-")["options"], "-w", "--watch", "--lane")
	has(t, "--lane: the lane numbers", comp(t, "sat", "demo", "view", "lane", "--lane", "")["lanes"], "1", "2", "3")
	has(t, "--lane, project second", comp(t, "sat", "view", "demo", "--lane", "")["lanes"], "1", "2", "3")
	if m := comp(t, "sat", "demo", "view", "--lane", "2", ""); len(m["views"]) == 0 {
		t.Errorf("a view after --lane N: %v", m)
	}
	has(t, "autostart", comp(t, "sat", "demo", "autostart", "")["values"], "on", "off")
	if m := comp(t, "sat", "demo", "autostart", "on", ""); len(m["values"]) > 0 {
		t.Errorf("autostart takes one word: %v", m)
	}
	m = comp(t, "sat", "demo", "env", "")
	has(t, "env set", m["keys"], "TOOL_HOME=", "PATH=")
	has(t, "env unset", m["unset"], "-TOOL_HOME", "-PATH")
	has(t, "alerts", comp(t, "sat", "demo", "alerts", "-")["options"], "-w", "--watch")
	has(t, "self-update", comp(t, "sat", "self-update", "")["options"], "--yes")
	has(t, "migrate", comp(t, "sat", "migrate", "-")["options"], "--yes")
	m = comp(t, "sat", "uninstall", "")
	has(t, "uninstall", m["projects"], "demo", "other")
	has(t, "uninstall --yes", comp(t, "sat", "uninstall", "-")["options"], "--yes")
	has(t, "setup --install", comp(t, "sat", "setup", "-")["options"], "--install")
	has(t, "setup HUB_IP", comp(t, "sat", "setup", "")["hubs"], "192.0.2.1", "192.0.2.9")
	has(t, "setup HUB_USER", comp(t, "sat", "setup", "--install", "192.0.2.1", "new", "")["users"], "hubuser")
	has(t, "help topics", comp(t, "sat", "help", "")["topics"], "start", "view", "self-update", "completion")
	has(t, "completion", comp(t, "sat", "completion", "")["values"], "zsh", "bash", "install")
	has(t, "sat-call: a project", comp(t, "sat-call", "")["projects"], "demo")
	has(t, "sat-call: the hub's commands", comp(t, "sat-call", "demo", "")["commands"], "get", "put", "status")
	t.Setenv("SAT_PROJECT", "demo")
	has(t, "sat-call in a job", comp(t, "sat-call", "")["commands"], "get", "put")

	// descriptions: a view's title, a lane's job, the env value
	out := capture(t, func() { Main("dev", []string{"sat", "__complete", "sat", "demo", "view", "--lane", ""}) })
	if !strings.Contains(out, "2\trunning job7\n") || !strings.Contains(out, "1\tidle\n") {
		t.Errorf("lanes:\n%s", out)
	}
	if out := capture(t, func() { Main("dev", []string{"sat", "__complete", "sat", "demo", "view", ""}) }); !strings.Contains(out, "colour\tIn colour\n") || !strings.Contains(out, "#views view\n") {
		t.Errorf("views:\n%s", out)
	}
	// nothing for a project that isn't there, and never an error
	if m := comp(t, "sat", "nope", "view", ""); len(m) != 0 {
		t.Errorf("unknown project: %v", m)
	}
}

// With one project, sat view NAME still names a view; with several, sat CMD PROJECT works.
func TestCommandThenProject(t *testing.T) {
	completeNode(t)
	if out := capture(t, func() { Main("dev", []string{"sat", "status", "other"}) }); !strings.HasPrefix(out, "other: ") || strings.Contains(out, "demo:") {
		t.Errorf("sat status other:\n%s", out)
	}
	if out := capture(t, func() { Main("dev", []string{"sat", "env", "demo"}) }); !strings.Contains(out, "TOOL_HOME") {
		t.Errorf("sat env demo:\n%s", out)
	}
}

func TestCompletionScripts(t *testing.T) {
	home := completeNode(t)
	zsh := capture(t, func() { Main("dev", []string{"sat", "completion", "zsh"}) })
	bash := capture(t, func() { Main("dev", []string{"sat", "completion", "bash"}) })
	if !strings.HasPrefix(zsh, "#compdef sat sat-call\n") || !strings.Contains(bash, "complete -F _sat sat sat-call") {
		t.Fatalf("scripts:\n%s\n%s", zsh, bash)
	}
	out := capture(t, func() { Main("dev", []string{"sat", "completion", "install"}) })
	f := filepath.Join(home, ".sat", "completions", "_sat")
	if b, _ := os.ReadFile(f); string(b) != zsh || !strings.Contains(out, completionLine) {
		t.Errorf("install: %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); err == nil {
		t.Error("touched .zshrc")
	}
	// The scripts run sat from PATH: this test binary, as sat.
	bin := filepath.Join(home, "satbin")
	os.MkdirAll(bin, 0o755)
	me, _ := os.Executable()
	os.WriteFile(filepath.Join(bin, "sat"), []byte("#!/bin/sh\nSAT_CLI_TEST_EXEC=1 exec "+me+" \"$@\"\n"), 0o755)
	path := bin + ":" + os.Getenv("PATH")

	if _, err := exec.LookPath("zsh"); err != nil {
		t.Log("no zsh: its script is not checked")
	} else {
		if out, err := exec.Command("zsh", "-n", f).CombinedOutput(); err != nil {
			t.Fatalf("zsh -n: %v\n%s", err, out)
		}
		// _describe stubbed: what the function hands zsh, for `sat demo env <tab>`.
		c := exec.Command("zsh", "-f", "-c", `_describe() { local a=$4; print -r -- "[$1 $2 $3 ${(j:,:)@[5,-1]}]" "${(@P)a}"; }
service=sat words=(sat demo env '') CURRENT=4; source "$1"; echo "rc=$?"
service=sat-call words=(sat-call de) CURRENT=2; _sat`, "z", f)
		c.Env = append(os.Environ(), "PATH="+path)
		out, err := c.CombinedOutput()
		got := string(out)
		for _, w := range []string{"[-t keys set a key -S,] TOOL_HOME=:set (now \"$HOME/.tool\") PATH=:set", "[-t unset unset a key ] -TOOL_HOME:unset", "rc=0", "[-t projects project ] demo:project, hub 192.0.2.1, 3 lanes other:"} {
			if !strings.Contains(got, w) {
				t.Errorf("zsh: lacks %q: %v\n%s", w, err, got)
			}
		}
	}
	c := exec.Command("bash", "-c", `source "$1"
for l in "sat demo vi" "sat demo env TO" "sat vi" "sat demo view --lane " "sat demo env TOOL_HOME="; do
  COMP_LINE=$l COMP_POINT=${#l}; read -ra COMP_WORDS <<< "$l"; COMP_CWORD=$(( ${#COMP_WORDS[@]} - 1 ))
  _sat sat; echo "$l|${COMPREPLY[*]}"
done`, "b", writeTemp(t, bash))
	c.Env = append(os.Environ(), "PATH="+path)
	bo, err := c.CombinedOutput()
	for _, w := range []string{"sat demo vi|views view\n", "sat demo env TO|TOOL_HOME=\n", "sat vi|views view\n", "sat demo view --lane |1 2 3\n", "sat demo env TOOL_HOME=|\n"} {
		if !strings.Contains(string(bo), w) {
			t.Errorf("bash: lacks %q: %v\n%s", w, err, bo)
		}
	}
}

func writeTemp(t *testing.T, s string) string {
	f := filepath.Join(t.TempDir(), "f")
	os.WriteFile(f, []byte(s), 0o644)
	return f
}

// The test binary doubles as sat when a script under test runs it.
func TestMain(m *testing.M) {
	if os.Getenv("SAT_CLI_TEST_EXEC") == "1" {
		os.Exit(Main("dev", os.Args))
	}
	os.Exit(m.Run())
}
