package cli

import (
	"fmt"
	"strings"

	"github.com/RashKash103/satellite/internal/ui"
)

// A command's help: its usage line, one line on what it does, and more when it needs it.
type cmdHelp struct {
	name, args, short, long string
	node                    bool // a node command (sat CMD), not a project one (sat [PROJECT] CMD)
}

var helps = []cmdHelp{
	{name: "start", short: "take jobs from the hub now",
		long: "Checks the project's requirements first (in the service's environment) and starts\nsat@PROJECT.service. A drain still pending is cancelled."},
	{name: "stop", short: "stop now; jobs in progress go straight back to the hub"},
	{name: "drain", short: "take no new jobs; stop once the running ones finish",
		long: "Use it before shutting the machine down to keep the work in progress. A normal\nshutdown is safe without it: stopping hands running jobs back."},
	{name: "schedule", args: "[start|end HH:MM [daily|YYYY-MM-DD] | off]", short: "drain at a time, or be stopped by it; once or daily",
		long: "start HH:MM: stop taking jobs at that time, and stop once the running ones finish.\nend HH:MM: be stopped by that time: stop taking jobs once a typical job (the p80 of\nthis node's recent ones) would no longer finish by it. daily repeats it every day;\nelse it is once, on the date given or the next time the clock reads HH:MM. With no\nwords, shows the schedule; off removes it. The service enforces it, and start\nkeeps it. docs/node.md#scheduled-drains"},
	{name: "status", short: "service state, hub, jobs in progress, disk use, recent log lines",
		long: "With no PROJECT, or several projects on the node, it shows them all."},
	{name: "logs", short: "follow the service log"},
	{name: "alerts", args: "[-w]", short: "what needs attention: stalled lanes, an unreachable hub, a full disk ...",
		long: "With no PROJECT, or several projects on the node, it shows them all. -w keeps the\nlist live. docs/node.md#alerts lists the conditions."},
	{name: "autostart", args: "on|off", short: "start at boot (off by default)"},
	{name: "check", short: "what this node is missing, and the commands that would install it",
		long: "Checked in the environment the service will have (the user manager's, plus the\nproject's config and env file), not your shell's."},
	{name: "install", short: "show those steps (apt ones simulated, never removing), ask, run them",
		long: "apt steps run with --no-remove: apt stops rather than remove a package. Nothing\nruns without a confirmation on a terminal. The commands run are kept in\n~/.sat/installed.log."},
	{name: "selftest", short: "the project's own check of this node (its sat/selftest)"},
	{name: "env", args: "[KEY=VALUE | -KEY]", short: "list, set or unset the project's env file",
		long: "Every job and project script sources it (with shell semantics) when it starts, so\na change applies from the next job."},
	{name: "update", short: "refresh the bundle from the hub, check, rerun the project's sat/setup"},
	{name: "views", short: "the project's views: name, title, scope, mode, format"},
	{name: "view", args: "NAME [--lane N] [-w]", short: "show one of the project's views; -w keeps it live",
		long: "A snapshot view runs once, or with -w every few seconds, redrawn in place on a\nterminal. A stream view prints what it has, or with -w is followed until ctrl+c.\nA lane view looks at the only lane with a job, or the one --lane names."},
	{name: "setup", args: "[--install] HUB_IP PROJECT [HUB_USER]", short: "make this machine a node for PROJECT", node: true,
		long: "Safe to rerun. It installs no software unless given --install, and then only\nafter showing every step and asking. Nothing starts until sat PROJECT start."},
	{name: "self-update", args: "[--yes]", short: "install the newest release (docs/security.md)", node: true,
		long: "Shows the release notes and asks; checks the download's checksum and its build\nattestation; then swaps the binary. Running services re-execute on it at once and\nadopt their running jobs, which keep running."},
	{name: "uninstall", args: "[--yes] [PROJECT]", short: "remove a project, or with the last one everything", node: true,
		long: "Lists exactly what it will remove and asks. It never removes packages."},
	{name: "migrate", args: "[--yes]", short: "move a node set up by the bash scripts (~/sat) to ~/.sat", node: true},
	{name: "completion", args: "zsh|bash|install", short: "shell completion (docs/node.md)", node: true,
		long: "zsh prints the zsh completion: sat completion zsh > \"${fpath[1]}/_sat\". install writes it\nto ~/.sat/completions/_sat and prints the line to add to ~/.zshrc (it never edits it).\nbash prints the bash one. Candidates come from this node's own files, as they change."},
	{name: "version", short: "this binary's version", node: true},
	{name: "help", args: "[COMMAND]", short: "this help, or a command's", node: true},
}

func helpFor(name string) *cmdHelp {
	for i := range helps {
		if helps[i].name == name {
			return &helps[i]
		}
	}
	return nil
}

func (h *cmdHelp) usage() string {
	s := "sat "
	if !h.node {
		s += "[PROJECT] "
	}
	s += h.name
	if h.args != "" {
		s += " " + h.args
	}
	return s
}

// printUsage: the overview. Plain, it is the text sat always printed.
func printUsage(t *ui.T) {
	if !t.Color() {
		fmt.Fprint(t, usage)
		return
	}
	row := func(h *cmdHelp) []string {
		n := h.name
		if h.args != "" {
			n += " " + h.args
		}
		if h.node {
			n = "sat " + n
		}
		return []string{t.Accent(n), h.short}
	}
	var pr, nr [][]string
	for i := range helps {
		if helps[i].node {
			nr = append(nr, row(&helps[i]))
		} else {
			pr = append(pr, row(&helps[i]))
		}
	}
	t.Println(t.Bold("sat") + t.Muted(" · a Satellite node: runs a hub's jobs while this machine is on"))
	t.Println()
	t.Println(t.Heading("Usage"))
	t.Print(ui.Columns("  ", [][]string{
		{t.Accent("sat"), "the live view: projects, lanes, jobs, progress, logs"},
		{t.Accent("sat [PROJECT] COMMAND"), "PROJECT may be left out when only one is set up"},
	}))
	t.Println()
	t.Println(t.Heading("Project commands"))
	t.Print(fold(t, pr))
	t.Println()
	t.Println(t.Heading("Node commands"))
	t.Print(fold(t, nr))
	t.Println()
	t.Println(t.Muted("sat help COMMAND (or COMMAND --help) for more · docs/node.md"))
}

// printHelp: one command's help screen.
func printHelp(t *ui.T, h *cmdHelp) {
	if !t.Color() {
		fmt.Fprintf(t, "usage: %s\n\n%s\n", h.usage(), h.short)
		if h.long != "" {
			fmt.Fprintf(t, "\n%s\n", h.long)
		}
		return
	}
	t.Println(t.Heading("Usage") + "  " + t.Accent(h.usage()))
	t.Println()
	t.Println(h.short)
	if h.long != "" {
		t.Println()
		t.Println(t.Muted(h.long))
	}
}

// wantsHelp: --help anywhere, or -h (except for env, where -h unsets h).
func wantsHelp(cmd string, a []string) bool {
	for _, x := range a {
		if x == "--help" || x == "-h" && cmd != "env" {
			return true
		}
	}
	return false
}

func help(a []string) int {
	t := ui.Out()
	if len(a) == 0 {
		printUsage(t)
		return 0
	}
	if h := helpFor(a[0]); h != nil {
		printHelp(t, h)
		return 0
	}
	return failHint("no command "+a[0], "sat help lists them")
}

// fail: "sat: what went wrong" on stderr; exit 2.
func fail(s string) int { return failHint(s, "") }

// failHint: fail, with a hint on the next line (what to run, where to look).
func failHint(s, hint string) int {
	t := ui.Err()
	if u, ok := strings.CutPrefix(s, "usage: "); ok {
		t.Println(t.Bold("usage:") + " " + u)
		if hint == "" {
			if f := strings.Fields(u); len(f) > 1 && f[0] == "sat" {
				if h := helpFor(f[1]); h != nil {
					hint = "sat help " + h.name + " for more"
				}
			}
		}
	} else {
		t.Println(t.Bad("sat:") + " " + s)
	}
	if hint != "" {
		t.Println("  " + t.Muted(hint))
	}
	return 2
}

// warnf: a warning line on stderr.
func warnf(f string, v ...any) {
	t := ui.Err()
	t.Println(t.Warn("sat:") + " " + fmt.Sprintf(f, v...))
}

// fold: aligned rows, or each description under its command when that is too wide.
func fold(t *ui.T, rows [][]string) string {
	c := ui.Columns("  ", rows)
	wide := 0
	for _, l := range strings.Split(c, "\n") {
		wide = max(wide, ui.Width(l))
	}
	w := t.Width()
	if w == 0 {
		w = 100
	}
	if wide <= w {
		return c
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("  " + r[0] + "\n      " + r[1] + "\n")
	}
	return b.String()
}
