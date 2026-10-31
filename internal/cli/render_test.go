package cli

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/plan"
	"github.com/RashKash103/satellite/internal/ui"
)

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func sample() *statusInfo {
	return &statusInfo{
		Name: "demo", State: "active", Enabled: "enabled", Hub: "sat-hub-192.0.2.1", HubVersion: "1", Live: true,
		Lanes: []agent.Lane{
			{Slot: 1, ID: "job42", Started: now.Add(-3 * time.Minute), Renewed: now.Add(-12 * time.Second), Percent: 40, Progress: "linking"},
			{Slot: 2, Percent: -1},
		},
		Disk: agent.Disk{Avail: 120 << 30, Need: 20 << 30, Used: 3 << 30},
		Log:  []string{"10-01 11:57:00 [1] claimed job42", "10-01 11:50:00 [2] job41 failed"},
	}
}

const plainStatus = `demo: active (autostart: enabled)
hub sat-hub-192.0.2.1: reachable, protocol 1
lane 1: job42 for 3m00s, renewed 12s ago, 40% linking
disk: 120 GB free (keeps 20 GB free), project 3 GB
10-01 11:57:00 [1] claimed job42
10-01 11:50:00 [2] job41 failed
`

const styledStatus = `● demo  running · autostart on
  hub     sat-hub-192.0.2.1  reachable · protocol 1
  disk    120 GB free · keeps 20 GB free · project 3.0 GB
  lane 1  job42  3m00s  renewed 12s ago  ████░░░░░░░░  40%  linking
  lane 2  idle: waiting for work
  recent log
    10-01 11:57:00 [1] claimed job42
    10-01 11:50:00 [2] job41 failed
`

func TestStatusPlain(t *testing.T) {
	var b strings.Builder
	sample().render(ui.Plain(&b), now)
	if b.String() != plainStatus {
		t.Fatalf("got\n%s\nwant\n%s", b.String(), plainStatus)
	}
	// The lines scripts and the e2e tests read.
	s := sample()
	s.State, s.Drain, s.HubErr, s.NotClaiming = "inactive", true, errors.New("hub speaks protocol 2"), true
	s.Live, s.Lanes, s.Claims, s.Missing = false, nil, [][2]string{{"1", "job9"}}, 2
	s.Disk.Avail = 1 << 30
	b.Reset()
	s.render(ui.Plain(&b), now)
	for _, w := range []string{"demo: inactive (autostart: enabled), draining\n", "hub sat-hub-192.0.2.1: reachable, but not claiming: hub speaks protocol 2\n",
		"lane 1: job9\n", "requirements: 2 missing", "disk: 1 GB free (keeps 20 GB free), project 3 GB  (not taking work: low on room)\n"} {
		if !strings.Contains(b.String(), w) {
			t.Errorf("lacks %q:\n%s", w, b.String())
		}
	}
	if strings.ContainsRune(b.String(), 0x1b) {
		t.Error("escape codes in plain output")
	}
}

func TestStatusStyled(t *testing.T) {
	for _, dark := range []bool{true, false} {
		var b strings.Builder
		sample().render(ui.Styled(&b, dark, 100), now)
		if !strings.Contains(b.String(), "\x1b[") {
			t.Fatal("not styled")
		}
		if got := ui.Strip(b.String()); got != styledStatus {
			t.Fatalf("got\n%s\nwant\n%s", got, styledStatus)
		}
	}
	// Narrow: no bar, log lines cut to the width.
	var b strings.Builder
	sample().render(ui.Styled(&b, true, 40), now)
	got := ui.Strip(b.String())
	if strings.Contains(got, "█") {
		t.Errorf("bar at 40 columns:\n%s", got)
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "    10-") && ui.Width(l) > 40 {
			t.Errorf("log line wider than 40: %q", l)
		}
	}
	s := sample()
	s.HubErr = errors.New("dial tcp: i/o timeout")
	b.Reset()
	s.render(ui.Styled(&b, true, 100), now)
	if !strings.Contains(ui.Strip(b.String()), "unreachable (dial tcp: i/o timeout)") {
		t.Error(ui.Strip(b.String()))
	}
	b.Reset()
	overview(ui.Styled(&b, true, 100), []*statusInfo{sample(), s})
	if got := ui.Strip(b.String()); !strings.Contains(got, "PROJECT  SERVICE  AUTOSTART  HUB          LANES") || !strings.Contains(got, "1 of 2 busy") {
		t.Errorf("overview:\n%s", got)
	}
}

var steps = []plan.Step{{Kind: "apt", Rest: "libfoo-dev"}, {Kind: "run", Rest: "curl -fsS https://example.com/i | sh"}, {Kind: "note", Rest: "plug the dongle in"}}

const plainCheck = "demo: this node is missing what these commands would install. Install them yourself, or run\n" +
	"`sat demo install` to see the exact steps and confirm them:\n\n" +
	"  sudo apt-get install --no-remove libfoo-dev\n  curl -fsS https://example.com/i | sh\n  # by hand: plug the dongle in\n"

const styledCheck = `✗ demo: this node is missing 3 things

  packages (apt)
    sudo apt-get install --no-remove libfoo-dev

  install commands
    curl -fsS https://example.com/i | sh

  to do by hand
    plug the dongle in

Install them yourself, or run sat demo install to see the exact steps and confirm them.
`

func TestCheckRendering(t *testing.T) {
	var b strings.Builder
	if plan.Check(ui.Plain(&b), steps, "demo") || b.String() != plainCheck {
		t.Fatalf("plain:\n%s", b.String())
	}
	b.Reset()
	if plan.Check(ui.Styled(&b, false, 80), steps, "demo") || ui.Strip(b.String()) != styledCheck {
		t.Fatalf("styled:\n%s", ui.Strip(b.String()))
	}
	b.Reset()
	plan.Check(&b, nil, "demo") // a plain writer is plain
	if b.String() != "demo: this node has everything it needs.\n" {
		t.Fatalf("%q", b.String())
	}
	b.Reset()
	plan.Check(ui.Styled(&b, true, 80), nil, "demo")
	if ui.Strip(b.String()) != "✓ demo: this node has everything it needs.\n" {
		t.Fatalf("%q", b.String())
	}
}

// Commands run with stdout to a pipe (as here) print no escape codes, and the lines scripts read
// keep their form.
func TestPipedOutputIsPlain(t *testing.T) {
	testNode(t)
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("FORCE_COLOR", "")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"sat", "version"}, "sat dev\n"},
		{[]string{"sat", "help"}, usage},
		{[]string{"sat", "help", "check"}, "usage: sat [PROJECT] check\n"},
		{[]string{"sat", "demo", "env"}, "demo has no env"},
		{[]string{"sat", "demo", "check"}, "demo: this node is missing what these commands would install"},
		{[]string{"sat", "demo", "status"}, "demo: inactive (autostart: disabled)\n"},
		{[]string{"sat", "status"}, "hub sat-hub-192.0.2.1: UNREACHABLE"},
		{[]string{"sat", "demo", "drain"}, "not running"},
	} {
		out := capture(t, func() { Main("dev", c.args) })
		if strings.ContainsRune(out, 0x1b) || strings.ContainsAny(out, "✓●•") {
			t.Errorf("%v: escapes or marks in %q", c.args, out)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("%v: lacks %q:\n%s", c.args, c.want, out)
		}
	}
	// NO_COLOR beats CLICOLOR_FORCE.
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "1")
	if out := capture(t, func() { Main("dev", []string{"sat", "demo", "check"}) }); strings.ContainsRune(out, 0x1b) {
		t.Errorf("NO_COLOR ignored: %q", out)
	}
	// And CLICOLOR_FORCE styles a pipe; version's first line stays.
	t.Setenv("NO_COLOR", "")
	out := capture(t, func() { Main("dev", []string{"sat", "demo", "check"}) })
	if !strings.ContainsRune(out, 0x1b) {
		t.Errorf("CLICOLOR_FORCE ignored: %q", out)
	}
	if out := capture(t, func() { Main("dev", []string{"sat", "version"}) }); !strings.HasPrefix(out, "sat dev\n") {
		t.Errorf("version: %q", out)
	}
}

func TestErrors(t *testing.T) {
	testNode(t)
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	rc := Main("dev", []string{"sat", "setup", "x"})
	rc2 := Main("dev", []string{"sat", "demo", "bogus"})
	w.Close()
	os.Stderr = old
	b := make([]byte, 4096)
	n, _ := r.Read(b)
	got := string(b[:n])
	if rc != 2 || rc2 != 2 || !strings.Contains(got, "usage: sat setup [--install] HUB_IP PROJECT [HUB_USER]\n  sat help setup for more") || !strings.Contains(got, "sat: no command bogus\n  sat help lists them") {
		t.Fatalf("%d %d %q", rc, rc2, got)
	}
}
