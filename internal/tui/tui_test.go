package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

// node: a temp HOME with projects hello (two lanes, one busy) and other (not running).
func node(t *testing.T) *model {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sc := filepath.Join(home, "systemctl")
	os.WriteFile(sc, []byte("#!/bin/sh\ncase \"$*\" in *is-active*sat@hello*) echo active ;; *is-enabled*sat@hello*) echo disabled ;; *is-active*) echo inactive ;; *) echo disabled ;; esac\n"), 0o755)
	t.Setenv("SAT_SYSTEMCTL", sc)
	t.Setenv("SAT_JOURNALCTL", "true")
	for _, n := range []string{"hello", "other"} {
		base := conf.Dir(n)
		os.MkdirAll(filepath.Join(base, "state"), 0o755)
		os.WriteFile(filepath.Join(base, "config"), []byte("HUB=sat-hub-192.0.2.1\nSLOTS=2\nMIN_FREE_GB=0\nMIN_FREE_PCT=0\n"), 0o644)
	}
	base := conf.Dir("hello")
	now := time.Now()
	st := agent.State{Project: "hello", Running: true, Updated: now, HubOK: true, ProtocolOK: true, Protocol: "1", LastContact: now,
		Lanes: []agent.Lane{{Slot: 1, ID: "job42", Started: now.Add(-3 * time.Minute), Renewed: now, Percent: 40, Progress: "linking"}, {Slot: 2, Percent: -1}}}
	b, _ := json.Marshal(st)
	os.WriteFile(filepath.Join(base, "state", "agent.json"), b, 0o644)
	os.WriteFile(filepath.Join(base, "state", "slot1.log"), []byte("compiling a\ncompiling b\nlinking c\n"), 0o644)
	m := newModel("dev", "/bin/false", Options{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

func key(m *model, k string) tea.Cmd {
	var msg tea.KeyPressMsg
	switch k {
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+u":
		msg = tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	default:
		r := []rune(k)[0]
		msg = tea.KeyPressMsg{Code: r, Text: k}
	}
	_, c := m.Update(msg)
	return c
}

func typ(m *model, s string) {
	for _, r := range s {
		key(m, string(r))
	}
}

func view(m *model) string { return ui.Strip(m.View().Content) }

func TestViewSizes(t *testing.T) {
	m := node(t)
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {100, 30}} {
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		v := view(m)
		ls := strings.Split(v, "\n")
		if len(ls) != sz[1] {
			t.Errorf("%dx%d: %d lines", sz[0], sz[1], len(ls))
		}
		for i, l := range ls {
			if ui.Width(l) > sz[0] {
				t.Errorf("%dx%d: line %d is %d wide: %q", sz[0], sz[1], i, ui.Width(l), l)
			}
		}
		for _, w := range []string{"1 Projects", "2 Lanes", "3 Details", "Lane log", "hello", "other", "running", "job42", "40%",
			"idle: waiting for work", "reachable", "sat-hub-192.0.2.1", "linking c", "hello lane 1", "? keys"} {
			if !strings.Contains(v, w) {
				t.Errorf("%dx%d lacks %q:\n%s", sz[0], sz[1], w, v)
			}
		}
	}
	// Light and dark backgrounds give different colours, the same text.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	dark := m.View().Content
	m.Update(tea.BackgroundColorMsg{Color: lipColor(255)})
	if light := m.View().Content; light == dark || ui.Strip(light) != ui.Strip(dark) {
		t.Error("the palette did not follow the background")
	}
}

func TestFocusAndMoves(t *testing.T) {
	m := node(t)
	for i, want := range []pane{paneLanes, paneDetails, paneMain, paneProjects} {
		key(m, "tab")
		if m.focus != want {
			t.Fatalf("tab %d: focus %d", i, m.focus)
		}
	}
	key(m, "shift+tab")
	if m.focus != paneMain {
		t.Fatalf("shift+tab: %d", m.focus)
	}
	key(m, "2")
	key(m, "j")
	if m.lane() != 2 {
		t.Errorf("lane %d", m.lane())
	}
	key(m, "1")
	key(m, "j")
	if m.cur().p.Name != "other" || m.lane() != 1 {
		t.Errorf("%s lane %d", m.cur().p.Name, m.lane())
	}
	key(m, "k")
	key(m, "enter")
	key(m, "enter")
	if m.focus != paneMain || m.tab != tabLog {
		t.Errorf("enter: %d %d", m.focus, m.tab)
	}
	key(m, "]")
	if m.tab != tabService || !strings.Contains(view(m), "nothing logged yet") {
		t.Errorf("tab %d", m.tab)
	}
	key(m, "[")
	// Click a project and a lane.
	view(m)
	r := m.geo.panes[paneProjects]
	m.Update(tea.MouseClickMsg{X: r.x + 3, Y: r.y + 2})
	if m.cur().p.Name != "other" || m.focus != paneProjects {
		t.Errorf("click: %s %d", m.cur().p.Name, m.focus)
	}
}

func TestFilter(t *testing.T) {
	m := node(t)
	key(m, "/")
	typ(m, "oth")
	if v := view(m); strings.Contains(strings.Split(v, "2 Lanes")[0], "hello ") || m.cur().p.Name != "other" {
		t.Errorf("filter:\n%s", v)
	}
	key(m, "enter")
	if m.filter[paneProjects] != "oth" || m.input != nil {
		t.Error("enter did not keep the filter")
	}
	key(m, "esc")
	if len(m.shown()) != 2 {
		t.Error("esc did not clear it")
	}
	// The right pane's lines.
	key(m, "4")
	key(m, "/")
	typ(m, "link")
	v := view(m)
	if !strings.Contains(v, "linking c") || strings.Contains(v, "compiling a") {
		t.Errorf("log filter:\n%s", v)
	}
	key(m, "esc")
	if !strings.Contains(view(m), "compiling a") {
		t.Error("esc kept the log filter")
	}
}

func TestHelpOverlay(t *testing.T) {
	m := node(t)
	key(m, "?")
	v := view(m)
	for _, w := range []string{"Keys", "Everywhere", "Projects and Lanes", "Right pane and Details", "add a project", "uninstall this project",
		"install it", "edit env", "self-update", "migrate", "autostart", "selftest"} {
		if !strings.Contains(v, w) {
			t.Errorf("help lacks %q", w)
		}
	}
	key(m, "s") // keys do nothing under the help
	if m.last.mode != "" {
		t.Error("ran under the help")
	}
	key(m, "esc")
	if m.help {
		t.Error("esc did not close it")
	}
}

func TestStopAsks(t *testing.T) {
	m := node(t)
	key(m, "x")
	if !strings.Contains(view(m), "Stop hello now?") || m.last.mode != "" {
		t.Fatal("stop did not ask")
	}
	if c := key(m, "n"); c != nil || m.confirm != nil || m.last.mode != "" {
		t.Fatal("stopped without a yes")
	}
	key(m, "x")
	if c := key(m, "y"); c == nil || !slices.Equal(m.last.args, []string{"hello", "stop"}) {
		t.Fatalf("y did not stop: %v", m.last)
	}
}

func TestDispatch(t *testing.T) {
	for _, c := range []struct {
		key, mode string
		args      []string
	}{
		{"s", "stream", []string{"hello", "start"}},
		{"d", "stream", []string{"hello", "drain"}},
		{"A", "stream", []string{"hello", "autostart", "on"}},
		{"c", "stream", []string{"hello", "check"}},
		{"t", "stream", []string{"hello", "selftest"}},
		{"u", "stream", []string{"hello", "update"}},
		// Whatever installs or removes runs on the terminal, where sat lists it and asks.
		{"i", "terminal", []string{"hello", "install"}},
		{"U", "terminal", []string{"uninstall", "hello"}},
		{"ctrl+u", "terminal", []string{"uninstall"}},
		{"S", "terminal", []string{"self-update"}},
	} {
		m := node(t)
		if cmd := key(m, c.key); cmd == nil || m.last.mode != c.mode || !slices.Equal(m.last.args, c.args) {
			t.Errorf("%s: %v (cmd %v)", c.key, m.last, cmd != nil)
		}
		if slices.Contains(m.last.args, "--yes") || slices.Contains(m.last.args, "-y") {
			t.Errorf("%s skips the confirmation", c.key)
		}
	}
	m := node(t)
	key(m, "M")
	if m.last.mode != "" {
		t.Error("migrate without a bash node")
	}
	m.opt.Legacy = func() bool { return true }
	m.refresh()
	if key(m, "M"); !slices.Equal(m.last.args, []string{"migrate"}) || m.last.mode != "terminal" {
		t.Error(m.last)
	}
	// env set through the input.
	m = node(t)
	key(m, "v")
	typ(m, "FOO=a b")
	if key(m, "enter"); !slices.Equal(m.last.args, []string{"hello", "env", "FOO=a b"}) {
		t.Error(m.last)
	}
}

// The terminal commands are sat itself; with no terminal to confirm on it runs nothing.
func TestTerminalCommandsAsk(t *testing.T) {
	m := node(t)
	d := t.TempDir()
	fake := filepath.Join(d, "sat")
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" > "+filepath.Join(d, "args")+"\n"), 0o755)
	m.exe = fake
	c := m.terminalCmd("hello", "install")
	c.Stdin = strings.NewReader("\n")
	if out, err := c.CombinedOutput(); err != nil || !strings.Contains(string(out), "Enter: back to the live view") {
		t.Fatalf("%v %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(d, "args")); string(b) != "hello install\n" {
		t.Fatalf("ran %q", b)
	}
}

func TestStreamedOutput(t *testing.T) {
	m := node(t)
	d := t.TempDir()
	fake := filepath.Join(d, "sat")
	os.WriteFile(fake, []byte("#!/bin/sh\necho \"checking $*\"\necho 'missing: x' >&2\n[ -n \"$NO_COLOR\" ] && echo plain\nexit 1\n"), 0o755)
	m.exe = fake
	cmd := key(m, "c")
	var j *job = m.job
	for deadline := time.Now().Add(5 * time.Second); j.running && time.Now().Before(deadline); {
		msg := <-j.ch
		m.Update(msg)
	}
	_ = cmd
	v := view(m)
	for _, w := range []string{"Output", "$ sat hello check", "checking hello check", "missing: x", "plain", "exit status 1", "check hello failed"} {
		if !strings.Contains(v, w) {
			t.Errorf("lacks %q:\n%s", w, v)
		}
	}
	if !m.msgBad {
		t.Error("a failure is not shown as one")
	}
}

func TestEditor(t *testing.T) {
	m := node(t)
	d := t.TempDir()
	ed := filepath.Join(d, "ed")
	os.WriteFile(ed, []byte("#!/bin/sh\necho 'FOO=1' >> \"$1\"\n"), 0o755)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", ed)
	if c := key(m, "e"); c == nil || m.last.mode != "edit" {
		t.Fatal("e did not open the editor")
	}
	f := m.last.args[0]
	if err := editor(f).Run(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f); string(b) != "FOO=1\n" {
		t.Fatalf("%q", b)
	}
	m.Update(edited{"hello", "env", nil})
	if !strings.Contains(m.msg, "applies from the next job") {
		t.Error(m.msg)
	}
	m.Update(edited{"hello", "config", nil})
	if !strings.Contains(m.msg, "next start") {
		t.Error(m.msg)
	}
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", "code -w")
	if c := editor("x"); !slices.Equal(c.Args, []string{"code", "-w", "x"}) {
		t.Error(c.Args)
	}
}

func TestSetupForm(t *testing.T) {
	m := node(t)
	key(m, "a")
	if !strings.Contains(view(m), "Add a project") {
		t.Fatal("no form")
	}
	key(m, "enter")
	key(m, "enter")
	key(m, "enter")
	key(m, "enter") // submit with nothing filled in
	if m.form == nil || !strings.Contains(view(m), "the hub's IP address is needed") {
		t.Fatal("submitted an empty form")
	}
	m.form.at = 0
	m.form.focus()
	typ(m, "192.0.2.7")
	key(m, "tab")
	typ(m, "newproj")
	key(m, "tab")
	key(m, "tab")
	key(m, "space")
	key(m, "enter")
	if m.form != nil || m.last.mode != "terminal" || !slices.Equal(m.last.args, []string{"setup", "--install", "192.0.2.7", "newproj"}) {
		t.Fatalf("%v", m.last)
	}
	m = node(t)
	key(m, "a")
	typ(m, "192.0.2.7")
	key(m, "tab")
	typ(m, "p2")
	key(m, "tab")
	typ(m, "hubuser")
	key(m, "tab")
	key(m, "enter")
	if m.last.mode != "stream" || !slices.Equal(m.last.args, []string{"setup", "192.0.2.7", "p2", "hubuser"}) {
		t.Fatalf("%v", m.last)
	}
	key(m, "a")
	key(m, "esc")
	if m.form != nil {
		t.Error("esc kept the form")
	}
}

func TestNoProjects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SAT_SYSTEMCTL", "true")
	m := newModel("dev", "/bin/false", Options{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if v := view(m); !strings.Contains(v, "none yet: a adds one") {
		t.Error(v)
	}
	if key(m, "s"); m.last.mode != "" || !strings.Contains(m.msg, "a adds one") {
		t.Error(m.last, m.msg)
	}
}

type lipColor uint8

func (c lipColor) RGBA() (r, g, b, a uint32) {
	v := uint32(c) * 0x101
	return v, v, v, 0xffff
}
