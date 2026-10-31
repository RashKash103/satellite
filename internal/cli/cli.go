// Package cli is the sat command. As sat-call (argv[0]) it is what jobs call the hub with.
package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/plan"
	"github.com/RashKash103/satellite/internal/selfupdate"
	"github.com/RashKash103/satellite/internal/tui"
	"github.com/RashKash103/satellite/internal/ui"
	"golang.org/x/term"
)

const usage = `sat                          the live view: lanes, jobs, progress, logs
sat [PROJECT] COMMAND        PROJECT may be left out when only one is set up
  start            take jobs from the hub now
  stop             stop now; jobs in progress go straight back to the hub
  drain            take no new jobs; stop once the running ones finish
  status           service state, hub, jobs in progress, disk use, recent log lines
  logs             follow the service log
  alerts [-w]      what needs attention: stalled lanes, an unreachable hub, a full disk ...
  autostart on|off start at boot (off by default)
  check            what this node is missing, and the commands that would install it
  install          show those steps (apt ones simulated, never removing), ask, run them
  selftest         the project's own check of this node (its sat/selftest)
  update           refresh the bundle from the hub, check, rerun the project's sat/setup
  views            the project's views (sat/views/ in its bundle)
  view NAME [--lane N] [-w]   show one; -w keeps it live
sat setup [--install] HUB_IP PROJECT [HUB_USER]   make this machine a node for PROJECT
sat self-update [--yes]      install the newest release (docs/security.md)
sat uninstall [--yes] [PROJECT]   remove a project, or with the last one everything
sat migrate [--yes]          move a node set up by the bash scripts (~/sat) to ~/.sat
sat completion zsh|bash|install   shell completion (docs/node.md)
sat version
`

var Version = "dev"

func Main(version string, args []string) int {
	Version = version
	if filepath.Base(args[0]) == "sat-call" {
		return satCall(args[1:])
	}
	a := args[1:]
	if len(a) == 0 {
		if !term.IsTerminal(int(os.Stdout.Fd())) || !term.IsTerminal(int(os.Stdin.Fd())) {
			return statusAll()
		}
		return tui.Run(Version, exe(), tui.Options{Legacy: bashLayout})
	}
	if h := helpFor(a[0]); h != nil && h.node && wantsHelp(a[0], a[1:]) {
		printHelp(ui.Out(), h)
		return 0
	}
	switch a[0] {
	case "setup":
		return setup(a[1:])
	case "migrate":
		return migrate(a[1:])
	case "self-update":
		return selfUpdate(a[1:])
	case "uninstall":
		return uninstall(a[1:])
	case "agent":
		if len(a) != 2 {
			return fail("usage: sat agent PROJECT")
		}
		return runAgent(a[1])
	case "_unit":
		return installUnit()
	case "__complete":
		return completeCmd(a[1:])
	case "completion":
		return completionCmd(a[1:])
	case "version", "--version":
		return printVersion()
	case "help", "-h", "--help":
		return help(a[1:])
	}
	ps := conf.List()
	// sat COMMAND PROJECT ... is sat PROJECT COMMAND ... (but with one project, sat view NAME names a view)
	if len(a) > 1 && !slices.Contains(ps, a[0]) && slices.Contains(ps, a[1]) {
		if h := helpFor(a[0]); h != nil && !h.node && (a[0] != "view" || len(ps) > 1) {
			a = append([]string{a[1], a[0]}, a[2:]...)
		}
	}
	var name string
	switch {
	case slices.Contains(ps, a[0]):
		name, a = a[0], a[1:]
	case len(a) > 1 && a[1] == "_release" && conf.NameOK(a[0]):
		name, a = a[0], a[1:] // a unit stopping for a project that is gone: nothing to do
	case len(ps) == 1:
		name = ps[0]
	case a[0] == "status":
		return statusAll()
	case a[0] == "alerts":
		return alerts(ps, a[1:])
	case len(ps) == 0:
		if h := helpFor(a[0]); h != nil && wantsHelp(a[0], a[1:]) {
			printHelp(ui.Out(), h)
			return 0
		}
		return failHint("no projects on this node", "sat setup HUB_IP PROJECT (docs/node.md)")
	default:
		if h := helpFor(a[0]); h != nil && wantsHelp(a[0], a[1:]) {
			printHelp(ui.Out(), h)
			return 0
		}
		return failHint("which project? "+strings.Join(ps, " "), "sat PROJECT "+a[0]+", with one of them as PROJECT")
	}
	if len(a) == 0 {
		printUsage(ui.Out())
		return 2
	}
	if h := helpFor(a[0]); h != nil && wantsHelp(a[0], a[1:]) {
		printHelp(ui.Out(), h)
		return 0
	}
	if a[0] == "alerts" {
		return alerts([]string{name}, a[1:])
	}
	return project(name, a[0], a[1:])
}

// version: the first line is "sat VERSION", always (the agent compares it); a terminal gets
// the build and any update too.
func printVersion() int {
	fmt.Println("sat", Version)
	if t := ui.Out(); t.Color() {
		t.Println(t.Muted(fmt.Sprintf("built with %s for %s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH)))
		if u := selfupdate.Cached(Version); u != "" {
			t.Println(t.Warn("↑ " + u + " is available: sat self-update"))
		}
	}
	return 0
}

func exe() string {
	e, err := os.Executable()
	if err != nil {
		return conf.Exe()
	}
	return e
}

func tty() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// ask once on a terminal; never yes without one.
func ask(q string) bool {
	t := ui.Out()
	if !tty() {
		t.Println("Not a terminal: nothing was done. Run this from a terminal to confirm, or pass --yes.")
		return false
	}
	t.Print(ui.Prompt(t, q))
	a, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	a = strings.TrimSpace(a)
	if a != "y" && a != "Y" {
		t.Println(t.Muted("Nothing was done."))
		return false
	}
	return true
}

func flag(a []string, names ...string) (bool, []string) {
	var rest []string
	found := false
	for _, x := range a {
		if slices.Contains(names, x) {
			found = true
		} else {
			rest = append(rest, x)
		}
	}
	return found, rest
}

// sc runs systemctl --user (SAT_SYSTEMCTL in tests).
func sc(args ...string) *exec.Cmd {
	return exec.Command(plan.Systemctl(), append([]string{"--user"}, args...)...)
}

func scOut(args ...string) string {
	out, _ := sc(args...).Output()
	return strings.TrimSpace(string(out))
}

func active(unit string) bool { return sc("is-active", "-q", unit).Run() == nil }

func journalctl() string {
	if j := os.Getenv("SAT_JOURNALCTL"); j != "" {
		return j
	}
	return "journalctl"
}

func run(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func human(n int64) string { return ui.Bytes(n) }

// tilde shortens a path under the home directory.
func tilde(p string) string {
	if r, err := filepath.Rel(conf.Home(), p); err == nil && !strings.HasPrefix(r, "..") {
		return "~/" + r
	}
	return p
}
