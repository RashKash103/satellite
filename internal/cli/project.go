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
	"github.com/RashKash103/satellite/internal/ui"
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
	t := ui.Out()
	switch cmd {
	case "start":
		return start(p)
	case "check":
		steps, svc := planEnv(p)
		ok := plan.Check(t, steps, p.Name)
		t.Println(t.Muted(envNote(svc)))
		if !ok {
			return 1
		}
	case "install":
		return install(p)
	case "stop":
		if err := sc("stop", u).Run(); err != nil {
			return fail("could not stop " + u + ": " + err.Error())
		}
		os.Remove(filepath.Join(st, "drain")) // stopped is not draining (the ExecStop clears it too)
		t.Done("stopped %s; jobs in progress were handed back", t.Bold(p.Name))
	case "drain":
		if !active(u) {
			t.Info("not running: nothing to drain")
			return 0
		}
		os.WriteFile(filepath.Join(st, "drain"), nil, 0o644)
		if ids := claims(p); len(ids) > 0 {
			t.Println(t.Mark(ui.MarkWarn) + "draining: no new jobs; stops after: " + t.Bold(strings.Join(ids, " ")))
		} else {
			t.Println(t.Mark(ui.MarkWarn) + "draining: no new jobs; no job is running, so it stops now")
		}
	case "status":
		status(p)
		updateNote()
	case "logs":
		logs(t, u)
	case "autostart":
		switch {
		case len(a) == 1 && (a[0] == "on" || a[0] == "off"):
			verb, say := "enable", p.Name+" starts at boot"
			if a[0] == "off" {
				verb, say = "disable", p.Name+" no longer starts at boot"
			}
			if out, err := sc(verb, u).CombinedOutput(); err != nil {
				os.Stderr.Write(out)
				return code(err)
			}
			t.Done("%s", say)
			return 0
		}
		return fail("usage: sat [PROJECT] autostart on|off")
	case "selftest":
		s := filepath.Join(p.Bundle, "sat", "selftest")
		if _, err := os.Stat(s); err != nil {
			t.Info("%s has no selftest", p.Name)
			return 0
		}
		if t.Color() {
			t.Println(t.Rule(p.Name + " selftest"))
		}
		err := run(agent.Command(agent.Env(p), s))
		if t.Color() {
			if err != nil {
				t.Println(t.Rule("") + "\n" + t.Mark(ui.MarkBad) + t.Bad(p.Name+" selftest failed") + t.Muted(" ("+err.Error()+")"))
			} else {
				t.Println(t.Rule("") + "\n" + t.Mark(ui.MarkOK) + p.Name + " selftest passed")
			}
		}
		return code(err)
	case "env":
		return envCmd(p, a)
	case "update":
		return update(p)
	case "views":
		return viewsCmd(p)
	case "view":
		return viewCmd(p, a)
	case "schedule":
		return scheduleCmd(p, a)
	default:
		failHint("no command "+cmd, "sat help lists them")
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
		e := ui.Err()
		e.Println(e.Bad("sat:") + " " + err.Error())
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
	t := ui.Out()
	if steps, svc := planEnv(p); len(steps) > 0 {
		plan.Check(t, steps, p.Name)
		t.Println(t.Muted(envNote(svc)))
		return 1
	}
	os.Remove(filepath.Join(p.State, "drain"))
	if err := sc("start", p.Unit()).Run(); err != nil {
		return fail("could not start " + p.Unit() + ": " + err.Error())
	}
	t.Done("started %s", t.Bold(p.Name))
	return 0
}

func installLog() string { return filepath.Join(conf.Root(), "installed.log") }

func install(p *conf.Project) int {
	steps := planOf(p)
	if len(steps) == 0 {
		ui.Out().Done("nothing missing")
		return 0
	}
	if !plan.Install(ui.Out(), os.Stdin, tty(), steps, logInstall(p.Name)) {
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

func update(p *conf.Project) int {
	c := hub.For(p)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t := ui.Out()
	var err, perr error
	var ch bool
	spin(t, "fetching the bundle from "+c.Name(), func() {
		_, perr = hub.CheckProtocol(ctx, c)
		ch, err = bundle.Sync(ctx, c, p.Name, p.Base, nil)
	})
	if perr != nil {
		t.Warned("hub %s: %v", c.Name(), perr)
	}
	if err != nil {
		return fail("bundle: " + err.Error())
	}
	if ch {
		t.Done("bundle updated from the hub")
	} else {
		t.Done("bundle unchanged")
	}
	if !plan.Check(t, planOf(p), p.Name) {
		return 1
	}
	if code := projectSetup(p); code != 0 {
		return code
	}
	selfupdate.Available(ctx, Version, false)
	updateNote()
	t.Done("updated %s", t.Muted("(a running service picks a changed bundle up by itself)"))
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
		// The same PID: the service stays up, and the running jobs stay this process's children
		// for the new binary to adopt.
		e := conf.Exe()
		err := syscall.Exec(e, []string{e, "agent", name}, os.Environ())
		fmt.Fprintf(os.Stdout, "could not run the new binary %s: %v\n", e, err)
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
