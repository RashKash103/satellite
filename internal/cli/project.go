package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/bundle"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/hub"
	"github.com/RashKash103/satellite/internal/plan"
	"github.com/RashKash103/satellite/internal/selfupdate"
	"github.com/RashKash103/satellite/internal/tui"
)

func project(name, cmd string, a []string) int {
	if cmd == "_release" {
		p, err := conf.Load(name)
		if err != nil {
			return 0
		}
		c := hub.For(p)
		defer c.Close()
		agent.Release(context.Background(), p, c, os.Stderr)
		return 0
	}
	p, err := conf.Load(name)
	if err != nil {
		return fail(err.Error())
	}
	st, u := p.State, p.Unit()
	switch cmd {
	case "start":
		return start(p)
	case "check":
		steps, svc := planEnv(p)
		ok := plan.Check(os.Stdout, steps, p.Name)
		fmt.Println(envNote(svc))
		if !ok {
			return 1
		}
	case "install":
		return install(p)
	case "stop":
		if err := sc("stop", u).Run(); err != nil {
			return fail("could not stop " + u + ": " + err.Error())
		}
		fmt.Printf("stopped %s; jobs in progress were handed back\n", p.Name)
	case "drain":
		if !active(u) {
			fmt.Println("not running")
			return 0
		}
		os.WriteFile(filepath.Join(st, "drain"), nil, 0o644)
		fmt.Printf("draining: no new jobs; stops after: %s\n", strings.Join(claims(p), " "))
	case "status":
		status(p)
		updateNote()
	case "logs":
		run(exec.Command(journalctl(), "--user", "-u", u, "-f", "-o", "cat"))
	case "autostart":
		switch {
		case len(a) == 1 && a[0] == "on":
			return code(run(sc("enable", u)))
		case len(a) == 1 && a[0] == "off":
			return code(run(sc("disable", u)))
		}
		return fail("autostart on|off")
	case "selftest":
		s := filepath.Join(p.Bundle, "sat", "selftest")
		if _, err := os.Stat(s); err != nil {
			fmt.Printf("%s has no selftest\n", p.Name)
			return 0
		}
		return code(run(agent.Command(agent.Env(p), s)))
	case "env":
		return envCmd(p, a)
	case "update":
		return update(p)
	default:
		fmt.Print(usage)
		return 2
	}
	return 0
}

// planOf: what the project's node is missing, checked in the environment its service will
// have (agent.CheckEnv), not this shell's.
func planOf(p *conf.Project) []plan.Step {
	s, _ := planEnv(p)
	return s
}

func planEnv(p *conf.Project) ([]plan.Step, bool) {
	env, svc := agent.CheckEnv(p)
	return plan.Of(agent.Script(p, "requirements"), func(s string) *exec.Cmd { return agent.Command(env, s) }), svc
}

// envNote: which environment a check ran in.
func envNote(svc bool) string {
	if svc {
		return "(checked in the environment the service runs with: the user manager's, as systemctl --user show-environment gives it, plus the project's config and env file)"
	}
	return "(checked in this shell's environment: the user manager's could not be read)"
}

func code(err error) int {
	var x *exec.ExitError
	if errors.As(err, &x) {
		return x.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func claims(p *conf.Project) []string {
	fs, _ := filepath.Glob(filepath.Join(p.State, "slot*.claim"))
	var ids []string
	for _, f := range fs {
		if b, _ := os.ReadFile(f); len(strings.TrimSpace(string(b))) > 0 {
			ids = append(ids, strings.TrimSpace(string(b)))
		}
	}
	return ids
}

func start(p *conf.Project) int {
	if steps, svc := planEnv(p); len(steps) > 0 {
		plan.Check(os.Stdout, steps, p.Name)
		fmt.Println(envNote(svc))
		return 1
	}
	os.Remove(filepath.Join(p.State, "drain"))
	if err := sc("start", p.Unit()).Run(); err != nil {
		return fail("could not start " + p.Unit() + ": " + err.Error())
	}
	fmt.Printf("started %s\n", p.Name)
	return 0
}

func installLog() string { return filepath.Join(conf.Root(), "installed.log") }

func install(p *conf.Project) int {
	steps := planOf(p)
	if len(steps) == 0 {
		fmt.Println("nothing missing")
		return 0
	}
	if !plan.Install(os.Stdout, os.Stdin, tty(), steps, logInstall(p.Name)) {
		return 1
	}
	return 0
}

// logInstall records each command `install` runs, so uninstall can list them.
func logInstall(project string) func(string) {
	return func(c string) {
		os.MkdirAll(conf.Root(), 0o755)
		if f, err := os.OpenFile(installLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%s %s: %s\n", time.Now().Format("2006-01-02 15:04"), project, c)
			f.Close()
		}
	}
}

func du(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			if s, ok := fi.Sys().(*syscall.Stat_t); ok {
				n += s.Blocks * 512
			} else {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return tui.Dur(time.Since(t)) + " ago"
}

func status(p *conf.Project) {
	u := p.Unit()
	state := scOut("is-active", u)
	if state == "" {
		state = "unknown"
	}
	dr := ""
	if _, err := os.Stat(filepath.Join(p.State, "drain")); err == nil {
		dr = ", draining"
	}
	fmt.Printf("%s: %s (autostart: %s)%s\n", p.Name, state, scOut("is-enabled", u), dr)
	c := hub.For(p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	v, err := hub.CheckProtocol(ctx, c)
	if err == nil {
		_, _, err = hub.Out(ctx, c, p.Name, "status")
	}
	cancel()
	c.Close()
	var pe hub.ProtocolError
	switch {
	case err == nil:
		fmt.Printf("hub %s: reachable, protocol %s\n", c.Name(), v)
	case errors.As(err, &pe):
		fmt.Printf("hub %s: reachable, but not claiming: %v\n", c.Name(), err)
	default:
		fmt.Printf("hub %s: UNREACHABLE (%v)\n", c.Name(), err)
	}
	if s, err := agent.ReadState(p); err == nil && s.Live() {
		if s.Note != "" {
			fmt.Printf("agent: %s\n", s.Note)
		}
		for _, l := range s.Lanes {
			if l.ID == "" {
				continue
			}
			pr := ""
			if l.Percent >= 0 {
				pr = fmt.Sprintf(", %d%%", l.Percent)
			}
			if l.Progress != "" {
				pr += " " + l.Progress
			}
			fmt.Printf("lane %d: %s for %s, renewed %s%s\n", l.Slot, l.ID, tui.Dur(time.Since(l.Started)), ago(l.Renewed), pr)
		}
	} else {
		fs, _ := filepath.Glob(filepath.Join(p.State, "slot*.claim"))
		for _, f := range fs {
			if b, _ := os.ReadFile(f); len(b) > 0 {
				fmt.Printf("lane %s: %s\n", strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "slot"), ".claim"), strings.TrimSpace(string(b)))
			}
		}
	}
	if state != "active" {
		if steps := planOf(p); len(steps) > 0 {
			fmt.Printf("requirements: %d missing, so the service won't start (sat %s check)\n", len(steps), p.Name)
		}
	}
	d := agent.Measure(p, true)
	hold := ""
	if !d.OK(1) {
		hold = "  (not taking work: low on room)"
	}
	fmt.Printf("disk: %s%s\n", d.Why(), hold)
	j := exec.Command(journalctl(), "--user", "-u", u, "-n", "8", "--no-pager", "-o", "cat")
	j.Stdout = os.Stdout
	j.Run()
}

func statusAll() int {
	ps := conf.List()
	if len(ps) == 0 {
		fmt.Println("no projects on this node: sat setup HUB_IP PROJECT (docs/node.md)")
		return 0
	}
	for i, n := range ps {
		if i > 0 {
			fmt.Println()
		}
		if p, err := conf.Load(n); err == nil {
			status(p)
		}
	}
	updateNote()
	return 0
}

func updateNote() {
	if t := selfupdate.Cached(Version); t != "" {
		fmt.Printf("\nsat %s is available (you have %s): sat self-update\n", t, Version)
	}
}

func update(p *conf.Project) int {
	c := hub.For(p)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := hub.CheckProtocol(ctx, c); err != nil {
		fmt.Printf("hub %s: %v\n", c.Name(), err)
	}
	ch, err := bundle.Sync(ctx, c, p.Name, p.Base, nil)
	if err != nil {
		return fail("bundle: " + err.Error())
	}
	fmt.Println(map[bool]string{true: "bundle updated from the hub", false: "bundle unchanged"}[ch])
	if !plan.Check(os.Stdout, planOf(p), p.Name) {
		return 1
	}
	if code := projectSetup(p); code != 0 {
		return code
	}
	selfupdate.Available(ctx, Version, false)
	updateNote()
	fmt.Println("updated (a running service picks a changed bundle up by itself)")
	return 0
}

func projectSetup(p *conf.Project) int {
	s := filepath.Join(p.Bundle, "sat", "setup")
	if fi, err := os.Stat(s); err != nil || fi.Mode()&0o111 == 0 {
		return 0
	}
	return code(run(agent.Command(agent.Env(p), s)))
}

func runAgent(name string) int {
	p, err := conf.Load(name)
	if err != nil {
		return fail(err.Error())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	c := hub.For(p)
	a := &agent.Agent{P: p, Hub: c, IV: agent.DefaultIntervals(), Version: Version, Out: os.Stdout}
	if Version != "dev" {
		a.Update = autoUpdate
	}
	err = a.Run(ctx)
	c.Close()
	if errors.Is(err, agent.ErrRestart) {
		e := conf.Exe()
		syscall.Exec(e, []string{e, "agent", name}, os.Environ())
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}

// satCall: sat-call [PROJECT] COMMAND ...: call sat-hub on the hub for a project (default
// $SAT_PROJECT). Jobs use it for get, put and status.
func satCall(a []string) int {
	proj := os.Getenv("SAT_PROJECT")
	if proj == "" {
		if len(a) == 0 {
			return fail("usage: sat-call [PROJECT] COMMAND ...")
		}
		proj, a = a[0], a[1:]
	}
	p, err := conf.Load(proj)
	if err != nil {
		return fail("sat-call: " + err.Error())
	}
	c := hub.For(p)
	defer c.Close()
	var in io.Reader
	if len(a) > 0 && (a[0] == "put" || a[0] == "fail") {
		in = os.Stdin
	}
	rc, err := c.Call(context.Background(), in, os.Stdout, os.Stderr, append([]string{proj}, a...)...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sat-call:", err)
	}
	return rc
}
