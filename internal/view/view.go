// Package view runs a project's views: executables in its bundle at sat/views/NAME that show
// something about the project (a queue, a lane's work) in `sat PROJECT view NAME` and as tabs in
// the live view. A view runs as a job does (agent.Command: the config and env sourced, sat-call on
// PATH), with SAT_VIEW_MODE, SAT_VIEW_WIDTH and SAT_VIEW_COLOR besides. Its header, among its first
// 10 lines, says how:
//
//	# sat-view: title=Queue; scope=project; mode=snapshot; every=5; timeout=10; format=text
//
// A snapshot view prints the current state and exits; a stream view keeps printing. A text view
// prints lines (ANSI colour allowed); a json view prints {"blocks": [...]} (see Doc).
package view

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
)

const (
	ScopeProject = "project"
	ScopeLane    = "lane"
	ModeSnapshot = "snapshot"
	ModeStream   = "stream"
	FormatText   = "text"
	FormatJSON   = "json"
)

// View: one of a project's views, as its header describes it. Err is set when the header is
// wrong; such a view is listed, and running it shows the error.
type View struct {
	Name, Title, Scope, Mode, Format string
	Every, Timeout                   time.Duration
	Path                             string
	Err                              error
}

// Header: the defaults, changed by the `# sat-view:` line among the first 10 lines, if any.
// Unknown keys are ignored (a bundle may be written for a newer sat); a known key with a bad
// value is an error.
func Header(name string, lines []string) (View, error) {
	v := View{Name: name, Title: name, Scope: ScopeProject, Mode: ModeSnapshot, Format: FormatText, Every: 5 * time.Second, Timeout: 10 * time.Second}
	for i, l := range lines {
		if i >= 10 {
			break
		}
		s := strings.TrimSpace(l)
		if !strings.HasPrefix(s, "#") {
			continue
		}
		rest, ok := strings.CutPrefix(strings.TrimSpace(strings.TrimPrefix(s, "#")), "sat-view:")
		if !ok {
			continue
		}
		for _, kv := range strings.Split(rest, ";") {
			kv = strings.TrimSpace(kv)
			if kv == "" {
				continue
			}
			k, val, ok := strings.Cut(kv, "=")
			k, val = strings.TrimSpace(k), strings.TrimSpace(val)
			if !ok {
				return v, fmt.Errorf("%q: want key=value", kv)
			}
			switch k {
			case "title":
				if val == "" {
					return v, errors.New("title is empty")
				}
				v.Title = val
			case "scope":
				if val != ScopeProject && val != ScopeLane {
					return v, fmt.Errorf("scope=%s: want project or lane", val)
				}
				v.Scope = val
			case "mode":
				if val != ModeSnapshot && val != ModeStream {
					return v, fmt.Errorf("mode=%s: want snapshot or stream", val)
				}
				v.Mode = val
			case "format":
				if val != FormatText && val != FormatJSON {
					return v, fmt.Errorf("format=%s: want text or json", val)
				}
				v.Format = val
			case "every", "timeout":
				f, err := strconv.ParseFloat(val, 64)
				if err != nil || f <= 0 || f > 86400 {
					return v, fmt.Errorf("%s=%s: want a number of seconds above 0", k, val)
				}
				d := time.Duration(f * float64(time.Second))
				if k == "every" {
					v.Every = d
				} else {
					v.Timeout = d
				}
			}
		}
		break
	}
	return v, nil
}

// Dir: where a project's views are.
func Dir(p *conf.Project) string { return filepath.Join(p.Bundle, "sat", "views") }

// List: the project's views, by name. Files that aren't executable, or whose names aren't plain
// names, are left out.
func List(p *conf.Project) []View {
	es, _ := os.ReadDir(Dir(p))
	var vs []View
	for _, e := range es {
		if !conf.NameOK(e.Name()) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(Dir(p), e.Name())
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() || fi.Mode()&0o111 == 0 {
			continue
		}
		vs = append(vs, Load(e.Name(), path))
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].Name < vs[j].Name })
	return vs
}

// Load reads one view's header.
func Load(name, path string) View {
	var lines []string
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(io.LimitReader(f, 16<<10))
		for len(lines) < 10 && sc.Scan() {
			lines = append(lines, sc.Text())
		}
		f.Close()
	}
	v, err := Header(name, lines)
	v.Path, v.Err = path, err
	return v
}

// Find: the named view.
func Find(p *conf.Project, name string) (View, bool) {
	for _, v := range List(p) {
		if v.Name == name {
			return v, true
		}
	}
	return View{}, false
}

// Sig: a cheap fingerprint of the views directory, to notice a changed bundle.
func Sig(p *conf.Project) string {
	var b strings.Builder
	if fi, err := os.Stat(p.Bundle); err == nil {
		fmt.Fprintf(&b, "%d;", fi.ModTime().UnixNano())
	}
	es, _ := os.ReadDir(Dir(p))
	for _, e := range es {
		if fi, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d:%o;", e.Name(), fi.Size(), fi.ModTime().UnixNano(), fi.Mode())
		}
	}
	return b.String()
}

// Lane: the lane a lane view looks at.
type Lane struct {
	Slot          int
	Job, Claim    string
	Started       time.Time
	Log, Progress string
}

// LaneOf: the lane as the agent's state describes it.
func LaneOf(p *conf.Project, l agent.Lane) *Lane {
	return &Lane{Slot: l.Slot, Job: l.ID, Claim: l.Line, Started: l.Started,
		Log:      filepath.Join(p.State, fmt.Sprintf("slot%d.log", l.Slot)),
		Progress: filepath.Join(p.State, fmt.Sprintf("slot%d.progress", l.Slot))}
}

// Opts: how a view is run.
type Opts struct {
	Live  bool // live (refreshed or followed), else once
	Width int
	Color bool
	Lane  *Lane // for a lane view
}

// ours: the variables sat sets for a view, never inherited.
var ours = []string{"SAT_VIEW_MODE", "SAT_VIEW_WIDTH", "SAT_VIEW_COLOR", "SAT_VIEW_ONCE", "SAT_VIEW", "SAT_SLOT", "SAT_JOB",
	"SAT_CLAIM", "SAT_LOG", "SAT_PROGRESS", "SAT_STARTED", "SAT_RESULT"}

// Env: the environment for a run, from base (agent.Env's input).
func Env(base []string, p *conf.Project, v View, o Opts) []string {
	var env []string
	for _, kv := range agent.EnvFrom(base, p) {
		k, _, _ := strings.Cut(kv, "=")
		keep := true
		for _, x := range ours {
			if k == x {
				keep = false
			}
		}
		if keep {
			env = append(env, kv)
		}
	}
	mode := "once"
	if o.Live {
		mode = "live"
	}
	w := o.Width
	if w <= 0 {
		w = 80
	}
	env = append(env, "SAT_VIEW="+v.Name, "SAT_VIEW_MODE="+mode, "SAT_VIEW_WIDTH="+strconv.Itoa(w))
	if o.Color {
		env = append(env, "SAT_VIEW_COLOR=1")
	}
	if !o.Live && v.Mode == ModeStream {
		env = append(env, "SAT_VIEW_ONCE=1")
	}
	if l := o.Lane; l != nil {
		env = append(env, "SAT_SLOT="+strconv.Itoa(l.Slot), "SAT_JOB="+l.Job, "SAT_CLAIM="+l.Claim, "SAT_LOG="+l.Log, "SAT_PROGRESS="+l.Progress)
		if !l.Started.IsZero() {
			env = append(env, "SAT_STARTED="+strconv.FormatInt(l.Started.Unix(), 10))
		}
	}
	return env
}

// TimeoutError: a run took longer than the view's timeout and was stopped.
type TimeoutError struct{ After time.Duration }

func (e TimeoutError) Error() string { return "timed out after " + Secs(e.After) + " and was stopped" }

// Secs: 5s, 0.5s, 90s.
func Secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s" }

// Describe: what a run's error means, for a person.
func Describe(err error) string {
	var x *exec.ExitError
	var te TimeoutError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &te):
		return te.Error()
	case errors.As(err, &x) && x.ExitCode() >= 0:
		return fmt.Sprintf("exited %d", x.ExitCode())
	case errors.As(err, &x):
		return "killed (" + x.String() + ")"
	}
	return err.Error()
}

// Run runs v and waits: a once run (or a snapshot's live run) is stopped after its timeout, a
// live stream when ctx ends. Its output goes to stdout and stderr. The view runs in its own
// process group, all of which is stopped (SIGTERM, then SIGKILL a second later).
func Run(ctx context.Context, p *conf.Project, v View, o Opts, stdout, stderr io.Writer) error {
	if v.Err != nil {
		return fmt.Errorf("sat/views/%s: %v", v.Name, v.Err)
	}
	cmd := agent.Command(Env(os.Environ(), p, v, o), v.Path)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	pg := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var limit <-chan time.Time
	if !o.Live || v.Mode == ModeSnapshot {
		t := time.NewTimer(v.Timeout)
		defer t.Stop()
		limit = t.C
	}
	stop := func() {
		syscall.Kill(-pg, syscall.SIGTERM)
		k := time.AfterFunc(time.Second, func() { syscall.Kill(-pg, syscall.SIGKILL) })
		<-done
		k.Stop()
		syscall.Kill(-pg, syscall.SIGKILL) // anything left in the group
	}
	select {
	case err := <-done:
		return err
	case <-limit:
		stop()
		return TimeoutError{v.Timeout}
	case <-ctx.Done():
		stop()
		return ctx.Err()
	}
}
