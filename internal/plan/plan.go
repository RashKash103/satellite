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
	"path/filepath"
	"strings"
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

// Of: the whole plan for a project's node, one step per line. env is the job environment.
func Of(bundle string, env []string) []Step {
	s := Needs()
	r := filepath.Join(bundle, "sat", "requirements")
	if fi, err := os.Stat(r); err == nil && fi.Mode()&0o111 != 0 {
		cmd := exec.Command(r)
		cmd.Env, cmd.Stderr = env, os.Stderr
		out, _ := cmd.Output()
		s = append(s, Parse(string(out))...)
	}
	return s
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

// Check reports what is missing; true when nothing is.
func Check(w io.Writer, steps []Step, project string) bool {
	if len(steps) == 0 {
		fmt.Fprintf(w, "%s: this node has everything it needs.\n", project)
		return true
	}
	fmt.Fprintf(w, "%s: this node is missing what these commands would install. Install them yourself, or run\n", project)
	fmt.Fprintf(w, "`sat %s install` to see the exact steps and confirm them:\n\n%s", project, Show(steps))
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
	n, notes := 0, 0
	fmt.Fprintln(w, "These steps will run, in this order:")
	for _, s := range steps {
		switch s.Kind {
		case "apt":
			n++
			fmt.Fprintf(w, "\n%d. %s\n", n, AptLine(s.Rest))
			if _, err := exec.LookPath("apt-get"); err != nil {
				fmt.Fprintln(w, "   no apt-get on this machine: install these packages yourself")
				return false
			}
			out, err := exec.Command("apt-get", append([]string{"-s", "install", "--no-remove"}, strings.Fields(s.Rest)...)...).CombinedOutput()
			if err != nil {
				for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
					fmt.Fprintf(w, "   apt: %s\n", l)
				}
				fmt.Fprintln(w, "   apt can't do this without removing packages, so nothing was run.")
				return false
			}
			i, u := Sim(string(out))
			fmt.Fprintf(w, "   installs: %s\n   upgrades: %s\n   removes:  nothing (--no-remove: apt stops instead)\n", list(i), list(u))
		case "run":
			n++
			fmt.Fprintf(w, "\n%d. %s\n", n, s.Rest)
		case "note":
			notes++
			fmt.Fprintf(w, "\n-  by hand (not run): %s\n", s.Rest)
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "(nothing to run)")
		return notes == 0
	}
	fmt.Fprintln(w)
	if !tty {
		fmt.Fprintln(w, "Not a terminal: nothing was run. Run this from a terminal to confirm.")
		return false
	}
	fmt.Fprintf(w, "Run these %d step(s)? [y/N] ", n)
	a, _ := bufio.NewReader(in).ReadString('\n')
	if a = strings.TrimSpace(a); a != "y" && a != "Y" {
		fmt.Fprintln(w, "Nothing was run.")
		return false
	}
	for _, s := range steps {
		var cmd *exec.Cmd
		switch s.Kind {
		case "apt":
			fmt.Fprintf(w, "+ %s\n", AptLine(s.Rest))
			ran(AptLine(s.Rest))
			cmd = exec.Command("sudo", append([]string{"apt-get", "install", "--no-remove"}, strings.Fields(s.Rest)...)...)
		case "run":
			fmt.Fprintf(w, "+ %s\n", s.Rest)
			ran(s.Rest)
			cmd = exec.Command("bash", "-c", s.Rest)
		default:
			continue
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, w, w
		if cmd.Run() != nil {
			return false
		}
	}
	return notes == 0
}
