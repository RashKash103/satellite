// Package plan: what a node is missing, and installing it only when asked.
//
// Requirements are plan lines, from Needs (the engine's own) and the bundle's sat/requirements
// (empty output: nothing missing):
//
//	apt PKG...      Debian/Ubuntu packages
//	run COMMAND     any other install step, run as you (say sudo in it if it needs root)
//	note TEXT       something to do by hand
//
// Nothing is installed unless the user asks (sat setup --install, sat PROJECT install), and then
// only after every command has been shown and confirmed. apt steps are simulated first, so the
// exact packages are listed, and always run with --no-remove: apt stops rather than remove anything.
package plan

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/RashKash103/satellite/internal/ui"
)

type Step struct{ Kind, Rest string }

// Systemctl is the systemctl the node uses (tests point SAT_SYSTEMCTL elsewhere).
func Systemctl() string {
	if s := os.Getenv("SAT_SYSTEMCTL"); s != "" {
		return s
	}
	return "systemctl"
}

// Needs: what Satellite itself needs on a node. The binary needs nothing; jobs reach the hub
// with the system ssh (git, rsync), and the exec transport uses it too.
func Needs() []Step {
	var s []Step
	if _, err := exec.LookPath("ssh"); err != nil {
		s = append(s, Step{"apt", "openssh-client"})
	}
	if _, err := exec.LookPath(Systemctl()); err != nil || exec.Command(Systemctl(), "--user", "show-environment").Run() != nil {
		s = append(s, Step{"note", "systemd user services are not available (the node runs as sat@PROJECT.service)"})
	}
	return s
}

func Parse(out string) []Step {
	var s []Step
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, r, _ := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if k == "apt" || k == "run" || k == "note" {
			s = append(s, Step{k, strings.TrimSpace(r)})
		}
	}
	return s
}

// Of: the whole plan for a project's node, one step per line: Needs, then what the bundle's
// sat/requirements (req, "" if none) prints, run through mk.
func Of(req string, mk func(string) *exec.Cmd) []Step {
	s := Needs()
	if req != "" {
		cmd := mk(req)
		cmd.Stderr = os.Stderr
		out, _ := cmd.Output()
		s = append(s, Parse(string(out))...)
	}
	return s
}

// Lines: the steps as plan lines, "apt a b; run x; note y".
func Lines(steps []Step) string {
	l := make([]string, len(steps))
	for i, s := range steps {
		l[i] = s.Kind + " " + s.Rest
	}
	return strings.Join(l, "; ")
}

func AptLine(pkgs string) string { return "sudo apt-get install --no-remove " + pkgs }

// Show: the plan as commands to run by hand.
func Show(steps []Step) string {
	var b strings.Builder
	for _, s := range steps {
		switch s.Kind {
		case "apt":
			fmt.Fprintf(&b, "  %s\n", AptLine(s.Rest))
		case "run":
			fmt.Fprintf(&b, "  %s\n", s.Rest)
		case "note":
			fmt.Fprintf(&b, "  # by hand: %s\n", s.Rest)
		}
	}
	return b.String()
}

// Check reports what is missing; true when nothing is. On a styled ui.T the steps are grouped
// by kind, each with its command.
func Check(w io.Writer, steps []Step, project string) bool {
	t := ui.For(w)
	if len(steps) == 0 {
		t.Println(t.Mark(ui.MarkOK) + fmt.Sprintf("%s: this node has everything it needs.", project))
		return true
	}
	if !t.Color() {
		fmt.Fprintf(t, "%s: this node is missing what these commands would install. Install them yourself, or run\n", project)
		fmt.Fprintf(t, "`sat %s install` to see the exact steps and confirm them:\n\n%s", project, Show(steps))
		return false
	}
	t.Println(t.Mark(ui.MarkBad) + t.Bold(project) + ": this node is missing " + ui.Plural(len(steps), "thing", "things"))
	for _, g := range []struct{ kind, title string }{{"apt", "packages (apt)"}, {"run", "install commands"}, {"note", "to do by hand"}} {
		var lines []string
		for _, s := range steps {
			if s.Kind != g.kind {
				continue
			}
			switch s.Kind {
			case "apt":
				lines = append(lines, t.Accent(AptLine(s.Rest)))
			case "run":
				lines = append(lines, t.Accent(s.Rest))
			default:
				lines = append(lines, s.Rest)
			}
		}
		if len(lines) == 0 {
			continue
		}
		t.Println()
		t.Println("  " + t.Heading(g.title))
		for _, l := range lines {
			t.Println("    " + l)
		}
	}
	t.Println()
	t.Println(t.Muted("Install them yourself, or run ") + t.Accent("sat "+project+" install") + t.Muted(" to see the exact steps and confirm them."))
	return false
}

// Sim splits apt-get -s output into packages installed and upgraded.
func Sim(out string) (installs, upgrades []string) {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || f[0] != "Inst" {
			continue
		}
		if len(f) > 2 && strings.HasPrefix(f[2], "[") {
			upgrades = append(upgrades, f[1])
		} else {
			installs = append(installs, f[1])
		}
	}
	return
}

func list(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, " ")
}

// Install shows every step (apt ones simulated), asks once on a terminal, and runs them in order.
// ran is called with each command before it runs (the node keeps a log of them). It returns
// false when anything is left: nothing run, a step failed, or notes to do by hand.
func Install(w io.Writer, in io.Reader, tty bool, steps []Step, ran func(string)) bool {
	t := ui.For(w)
	c := t.Color()
	n, notes := 0, 0
	t.Println(t.Heading("These steps will run, in this order:"))
	num := func() string {
		if c {
			return t.Accent(fmt.Sprintf("%2d", n)) + "  "
		}
		return fmt.Sprintf("%d. ", n)
	}
	ind := "   "
	if c {
		ind = "    "
	}
	for _, s := range steps {
		switch s.Kind {
		case "apt":
			n++
			t.Printf("\n%s%s\n", num(), t.Bold(AptLine(s.Rest)))
			if _, err := exec.LookPath("apt-get"); err != nil {
				t.Println(ind + t.Bad("no apt-get on this machine: install these packages yourself"))
				return false
			}
			out, err := exec.Command("apt-get", append([]string{"-s", "install", "--no-remove"}, strings.Fields(s.Rest)...)...).CombinedOutput()
			if err != nil {
				for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
					t.Println(ind + t.Muted("apt: "+l))
				}
				t.Println(ind + t.Bad("apt can't do this without removing packages, so nothing was run."))
				return false
			}
			i, u := Sim(string(out))
			if c {
				pk := func(s []string, st func(string) string) string {
					if len(s) == 0 {
						return t.Muted("(none)")
					}
					return st(strings.Join(s, " "))
				}
				t.Print(ui.Columns(ind, [][]string{
					{t.Muted("installs"), pk(i, t.OK)},
					{t.Muted("upgrades"), pk(u, t.Warn)},
					{t.Muted("removes"), t.OK("nothing") + t.Muted(" (--no-remove: apt stops instead)")},
				}))
			} else {
				fmt.Fprintf(t, "   installs: %s\n   upgrades: %s\n   removes:  nothing (--no-remove: apt stops instead)\n", list(i), list(u))
			}
		case "run":
			n++
			t.Printf("\n%s%s\n", num(), t.Bold(s.Rest))
		case "note":
			notes++
			if c {
				t.Printf("\n%s  %s %s\n", t.Warn(" -"), t.Muted("by hand (not run):"), s.Rest)
			} else {
				t.Printf("\n-  by hand (not run): %s\n", s.Rest)
			}
		}
	}
	if n == 0 {
		t.Println(t.Muted("(nothing to run)"))
		return notes == 0
	}
	t.Println()
	if !tty {
		t.Println("Not a terminal: nothing was run. Run this from a terminal to confirm.")
		return false
	}
	q := "Run this step?"
	if n > 1 {
		q = fmt.Sprintf("Run these %d steps?", n)
	}
	t.Print(ui.Prompt(t, q))
	a, _ := bufio.NewReader(in).ReadString('\n')
	if a = strings.TrimSpace(a); a != "y" && a != "Y" {
		t.Println("Nothing was run.")
		return false
	}
	for _, s := range steps {
		var cmd *exec.Cmd
		var line string
		switch s.Kind {
		case "apt":
			line = AptLine(s.Rest)
			cmd = exec.Command("sudo", append([]string{"apt-get", "install", "--no-remove"}, strings.Fields(s.Rest)...)...)
		case "run":
			line = s.Rest
			cmd = exec.Command("bash", "-c", s.Rest)
		default:
			continue
		}
		if c {
			t.Println(t.Accent("▶") + " " + t.Bold(line))
		} else {
			t.Printf("+ %s\n", line)
		}
		ran(line)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, t.Raw(), t.Raw()
		if err := cmd.Run(); err != nil {
			t.Println(t.Mark(ui.MarkBad) + t.Bad(fmt.Sprintf("failed (%v): %s", err, line)) + "; nothing after it was run")
			return false
		}
		if c {
			t.Println(t.Mark(ui.MarkOK) + t.Muted("done"))
		}
	}
	if notes > 0 {
		t.Println(t.Mark(ui.MarkWarn) + "Still to do by hand: " + ui.Plural(notes, "note", "notes") + " above.")
	}
	return notes == 0
}
