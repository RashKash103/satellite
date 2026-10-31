// Package agent is the node's service (sat@PROJECT.service: `sat agent PROJECT`). It runs SLOTS
// lanes; each claims a job from the hub and runs the project's sat/job on it under a lease renewed
// every 2 min. Exit codes of the job: 0 passed (SAT_RESULT holds the words for `ok`), 1 failed
// (SAT_RESULT: reason line, then the summary), anything else: handed back for another lane. Keeps
// the bundle in step with the hub (every 5 min) and runs its sat/sync at start and hourly.
// `sat drain` (state/drain) stops new claims; the agent exits once the running jobs finish.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/RashKash103/satellite/internal/bundle"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/hub"
	"github.com/RashKash103/satellite/internal/plan"
)

// ErrRestart: the binary on disk was replaced; the caller re-executes it.
var ErrRestart = errors.New("restart")

type Intervals struct{ Renew, Poll, Unreach, Bundle, Sync, State, Protocol, Kill, Disk time.Duration }

func DefaultIntervals() Intervals {
	if os.Getenv("SAT_TEST_FAST") == "1" { // the end-to-end test
		return Intervals{2 * time.Second, 300 * time.Millisecond, time.Second, 3 * time.Second, time.Hour, 200 * time.Millisecond, 2 * time.Second, 3 * time.Second, time.Second}
	}
	return Intervals{120 * time.Second, 45 * time.Second, 60 * time.Second, 5 * time.Minute, time.Hour, 2 * time.Second, 5 * time.Minute, 30 * time.Second, 5 * time.Minute}
}

type Agent struct {
	P       *conf.Project
	Hub     hub.Caller
	IV      Intervals
	Version string
	Out     io.Writer // the log also goes to state/agent.log
	// Between calls the agent may ask whether to restart for an update (AUTO_UPDATE); may be nil.
	Update func(ctx context.Context, log func(string, ...any)) bool

	name    string
	env     []string
	swap    sync.RWMutex // held for writing while the bundle is swapped
	mu      sync.Mutex
	st      State
	restart bool
	proto   struct {
		gen uint64
		at  time.Time
		ok  bool
		msg string
	}
	logMu    sync.Mutex
	clean    sync.Mutex // one sat/clean at a time
	diskNote time.Time  // the last "not taking work" line
}

func (a *Agent) Logf(f string, v ...any) {
	l := time.Now().Format("01-02 15:04:05") + " " + fmt.Sprintf(f, v...) + "\n"
	a.logMu.Lock()
	defer a.logMu.Unlock()
	if a.Out != nil {
		io.WriteString(a.Out, l)
	}
	p := filepath.Join(a.P.State, "agent.log")
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		os.Rename(p, p+".1")
	}
	if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.WriteString(l)
		f.Close()
	}
}

// Env: the environment for the project's scripts: the agent's, with the SAT_ variables. The
// config and the project's env file are added by the shell that starts each script (Command).
func Env(p *conf.Project) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k != "SAT_PROJECT" && k != "SAT_BASE" && k != "SAT_BUNDLE" && k != "SAT_NAME" {
			env = append(env, kv)
		}
	}
	return append(env, "SAT_PROJECT="+p.Name, "SAT_BASE="+p.Base, "SAT_BUNDLE="+p.Bundle, "SAT_NAME="+p.NodeName())
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Command runs one of the project's scripts as the bash node did: through bash, with the config
// and env file sourced (set -a, so every setting reaches the script, with shell semantics), then
// ~/.sat/bin first on PATH. env is Env(p) plus anything per call (SAT_SLOT, ...).
func Command(env []string, script string, args ...string) *exec.Cmd {
	w := `set -a; . "$SAT_BASE/config"; [ -f "$SAT_BASE/env" ] && . "$SAT_BASE/env"; set +a
PATH=` + shq(conf.Bin()) + `:"$HOME/.local/bin:$PATH"; exec "$@"`
	cmd := exec.Command("bash", append([]string{"-c", w, "sat", script}, args...)...)
	cmd.Env = env
	return cmd
}

// Script: the bundle's sat/NAME, if it is there and executable.
func Script(p *conf.Project, name string) string {
	s := filepath.Join(p.Bundle, "sat", name)
	if fi, err := os.Stat(s); err != nil || fi.Mode()&0o111 == 0 || fi.IsDir() {
		return ""
	}
	return s
}

func (a *Agent) draining() bool {
	_, err := os.Stat(filepath.Join(a.P.State, "drain"))
	return err == nil
}

func (a *Agent) stopping() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.restart || a.draining()
}

// nap sleeps d, waking early for a drain, a restart or the end.
func (a *Agent) nap(ctx context.Context, d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) && !a.stopping() && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(min(time.Until(end), 250*time.Millisecond)):
		}
	}
}

func (a *Agent) contact(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.st.HubOK = err == nil
	if err != nil {
		a.st.HubErr = err.Error()
	} else {
		a.st.HubErr, a.st.LastContact = "", time.Now()
	}
}

// call runs sat-hub PROJECT ARGS; rc 255 with err when the hub can't be reached.
func (a *Agent) call(ctx context.Context, stdin io.Reader, args ...string) (string, int, error) {
	var out, errb strings.Builder
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rc, err := a.Hub.Call(cctx, stdin, &out, &errb, append([]string{a.P.Name}, args...)...)
	a.contact(err)
	if err == nil && rc != 0 && errb.Len() > 0 {
		err = fmt.Errorf("%s", strings.TrimSpace(errb.String()))
	}
	return out.String(), rc, err
}

// protocolOK checks the hub's protocol on each new connection and every few minutes.
func (a *Agent) protocolOK(ctx context.Context) bool {
	g := a.Hub.Gen()
	a.mu.Lock()
	fresh := a.proto.at.IsZero() || g != a.proto.gen || time.Since(a.proto.at) > a.IV.Protocol
	a.mu.Unlock()
	if !fresh {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.proto.ok
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	v, err := hub.CheckProtocol(cctx, a.Hub)
	cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	var pe hub.ProtocolError
	if err != nil && !errors.As(err, &pe) {
		a.st.HubOK, a.st.HubErr = false, err.Error() // unreachable: ask again next time
		return false
	}
	a.proto.gen, a.proto.at, a.proto.ok = a.Hub.Gen(), time.Now(), err == nil
	a.st.HubOK, a.st.Protocol, a.st.ProtocolOK = true, v, err == nil
	msg := ""
	if err != nil {
		msg = "not claiming: " + err.Error()
	}
	if msg != a.proto.msg {
		a.proto.msg = msg
		a.st.Note = msg
		go a.Logf("%s", map[bool]string{true: "hub speaks protocol " + v + ": claiming", false: msg}[err == nil])
	}
	return err == nil
}

func (a *Agent) setLane(i int, f func(*Lane)) {
	a.mu.Lock()
	f(&a.st.Lanes[i])
	a.mu.Unlock()
}

func (a *Agent) writeState(running bool) {
	a.mu.Lock()
	s := a.st
	s.Lanes = append([]Lane(nil), a.st.Lanes...)
	a.mu.Unlock()
	s.Updated, s.Running, s.Draining = time.Now(), running, a.draining()
	for i := range s.Lanes {
		if s.Lanes[i].ID != "" {
			s.Lanes[i].Progress, s.Lanes[i].Percent = Progress(filepath.Join(a.P.State, fmt.Sprintf("slot%d.progress", s.Lanes[i].Slot)))
		}
	}
	b, _ := jsonIndent(s)
	conf.WriteAtomic(StatePath(a.P), b, 0o644)
}

// exeChanged: the binary on disk is no longer the one running (an update or a reinstall).
func exeChanged(path string) bool {
	a, err1 := os.Stat(path)
	b, err2 := os.Stat("/proc/self/exe")
	if err1 != nil || err2 != nil {
		return false
	}
	return !os.SameFile(a, b)
}

func (a *Agent) syncBundle(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ch, err := bundle.Sync(cctx, a.Hub, a.P.Name, a.P.Base, func() func() { a.swap.Lock(); return a.swap.Unlock })
	a.contact(err)
	if ch {
		a.Logf("bundle updated from the hub")
	}
	return err
}

func (a *Agent) runScript(ctx context.Context, name string) {
	s := Script(a.P, name)
	if s == "" {
		return
	}
	cmd := Command(a.env, s)
	cmd.Stdout, cmd.Stderr = a.Out, a.Out
	if err := cmd.Run(); err != nil {
		a.Logf("sat/%s: %v", name, err)
	}
}

// Run is the service. It returns nil after a drain, ErrRestart for an update.
func (a *Agent) Run(ctx context.Context) error {
	p := a.P
	os.MkdirAll(p.State, 0o755)
	a.name, a.env = p.NodeName(), Env(p)
	slots, _ := strconv.Atoi(p.Get("SLOTS", "1"))
	slots = max(slots, 1)
	exe, _ := os.Executable()
	a.st = State{Project: p.Name, Node: a.name, Version: a.Version, PID: os.Getpid(), Started: time.Now(), Hub: a.Hub.Name()}
	for i := range slots {
		a.st.Lanes = append(a.st.Lanes, Lane{Slot: i + 1, Percent: -1})
	}
	a.Logf("starting %s on %s: %d lane(s), hub %s, disk: %s", p.Name, a.name, slots, a.Hub.Name(), a.disk().Why())
	if err := a.syncBundle(ctx); err != nil {
		a.Logf("cannot reach the hub: %v", err)
		return err
	}
	if s := plan.Of(Script(p, "requirements"), func(s string) *exec.Cmd { return Command(a.env, s) }); len(s) > 0 {
		a.Logf("this node is missing what %s needs (see: sat %s check); not starting", p.Name, p.Name)
		a.mu.Lock()
		a.st.Note = "missing requirements: sat " + p.Name + " check"
		a.mu.Unlock()
		a.writeState(false)
		return nil
	}
	a.runScript(ctx, "sync")
	a.writeState(true)

	var wg sync.WaitGroup
	for i := range slots {
		wg.Go(func() { a.lane(ctx, i) })
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	st := time.NewTicker(a.IV.State)
	defer st.Stop()
	lastBundle, lastSync, lastUpd := time.Now(), time.Now(), time.Time{}
	for {
		select {
		case <-done:
			a.writeState(false)
			a.mu.Lock()
			r := a.restart
			a.mu.Unlock()
			if ctx.Err() != nil {
				a.Logf("stopped")
				return nil
			}
			if r {
				a.Logf("restarting for the new binary")
				return ErrRestart
			}
			os.Remove(filepath.Join(p.State, "drain"))
			a.Logf("drained: stopping")
			return nil
		case <-st.C:
			a.writeState(true)
			if time.Since(lastBundle) >= a.IV.Bundle {
				lastBundle = time.Now()
				go func() {
					if err := a.syncBundle(ctx); err != nil {
						a.Logf("bundle: %v", err)
					}
				}()
			}
			if time.Since(lastSync) >= a.IV.Sync {
				lastSync = time.Now()
				go a.runScript(ctx, "sync")
			}
			if a.Update != nil && time.Since(lastUpd) >= time.Hour {
				lastUpd = time.Now()
				go func() {
					if a.Update(ctx, a.Logf) {
						a.mu.Lock()
						a.restart = true
						a.mu.Unlock()
					}
				}()
			}
			if exeChanged(exe) {
				a.mu.Lock()
				if !a.restart {
					a.restart = true
					go a.Logf("the binary was replaced: restarting once the running jobs finish")
				}
				a.mu.Unlock()
			}
		}
	}
}

func (a *Agent) lane(ctx context.Context, i int) {
	slot := i + 1
	for !a.stopping() && ctx.Err() == nil {
		if !a.ready(i) {
			a.nap(ctx, a.IV.Unreach)
			continue
		}
		if !a.roomy(i) {
			a.nap(ctx, a.IV.Disk)
			continue
		}
		if !a.protocolOK(ctx) {
			a.setLane(i, func(l *Lane) { l.Waiting = "hub" })
			a.nap(ctx, a.IV.Unreach)
			continue
		}
		line, rc, err := a.call(ctx, nil, "claim", a.name, strconv.Itoa(slot))
		if err != nil || rc != 0 {
			if ctx.Err() == nil {
				a.Logf("[%d] hub unreachable: %v", slot, errOr(err, rc))
				a.setLane(i, func(l *Lane) { l.Waiting = "hub" })
				a.nap(ctx, a.IV.Unreach)
			}
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			a.setLane(i, func(l *Lane) { l.Waiting = "" })
			a.nap(ctx, a.IV.Poll)
			continue
		}
		a.run(ctx, i, line)
	}
}

// ready: the bundle's sat/ready, if any, lets this lane claim now (it can hold the node back,
// e.g. for its own usage limit). Its output goes to the lane's log.
func (a *Agent) ready(i int) bool {
	s := Script(a.P, "ready")
	if s == "" {
		return true
	}
	cmd := Command(append(append([]string(nil), a.env...), "SAT_SLOT="+strconv.Itoa(i+1)), s)
	if f, err := os.OpenFile(filepath.Join(a.P.State, fmt.Sprintf("slot%d.log", i+1)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		defer f.Close()
		cmd.Stdout, cmd.Stderr = f, f
	}
	if cmd.Run() != nil {
		a.setLane(i, func(l *Lane) { l.Waiting = "ready" })
		return false
	}
	return true
}

// roomy: below the disk floor or over MAX_GB, let the project clean up (sat/clean, one at a time),
// then measure again; still short, take no work (said at most every 30 min).
func (a *Agent) roomy(i int) bool {
	d := a.disk()
	if d.OK(1) {
		return true
	}
	slot := i + 1
	s := Script(a.P, "clean")
	if s != "" && !a.clean.TryLock() {
		a.setLane(i, func(l *Lane) { l.Waiting = "disk" })
		return false // another lane is cleaning up: wait for it
	}
	if s != "" {
		cmd := Command(a.env, s)
		if f, err := os.OpenFile(filepath.Join(a.P.State, "clean.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			cmd.Stdout, cmd.Stderr = f, f
			cmd.Run()
			f.Close()
		}
		os.Remove(filepath.Join(a.P.State, "du"))
		a.clean.Unlock()
		d = a.disk()
		a.Logf("[%d] low on disk: ran sat/clean (%s)", slot, d.Why())
		if d.OK(1) {
			return true
		}
	}
	a.mu.Lock()
	note := time.Since(a.diskNote) > 30*time.Minute
	if note {
		a.diskNote = time.Now()
	}
	a.mu.Unlock()
	if note {
		a.Logf("[%d] not taking work: %s", slot, d.Why())
	}
	a.setLane(i, func(l *Lane) { l.Waiting = "disk" })
	return false
}

func (a *Agent) disk() Disk {
	d := Measure(a.P, false)
	a.mu.Lock()
	a.st.Disk, a.st.DiskOK = d, d.OK(1)
	a.mu.Unlock()
	return d
}

func errOr(err error, rc int) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("exit %d", rc)
}

// printable keeps what the gate lets through: words of printable ASCII.
func printable(s string) []string {
	return strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return ' '
		}
		return r
	}, s))
}

func (a *Agent) run(ctx context.Context, i int, line string) {
	slot := i + 1
	st := a.P.State
	words := strings.Fields(line)
	id := words[0]
	claim := filepath.Join(st, fmt.Sprintf("slot%d.claim", slot))
	res := filepath.Join(st, fmt.Sprintf("slot%d.result", slot))
	prog := filepath.Join(st, fmt.Sprintf("slot%d.progress", slot))
	logp := filepath.Join(st, fmt.Sprintf("slot%d.log", slot))
	os.WriteFile(claim, []byte(id+"\n"), 0o644)
	os.Remove(res)
	os.WriteFile(prog, nil, 0o644)
	if fi, err := os.Stat(logp); err == nil && fi.Size() > 10<<20 {
		os.Rename(logp, logp+".1")
	}
	now := time.Now()
	a.setLane(i, func(l *Lane) { *l = Lane{Slot: slot, ID: id, Line: line, Started: now, Renewed: now, Percent: -1} })
	defer func() {
		os.Remove(claim)
		a.setLane(i, func(l *Lane) { *l = Lane{Slot: slot, Percent: -1} })
	}()
	a.Logf("[%d] claimed %s", slot, id)

	lf, _ := os.OpenFile(logp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if lf != nil {
		defer lf.Close()
	}
	cmd := Command(append(append([]string(nil), a.env...), "SAT_SLOT="+strconv.Itoa(slot), "SAT_RESULT="+res, "SAT_PROGRESS="+prog),
		filepath.Join(a.P.Bundle, "sat", "job"), words...)
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	a.swap.RLock()
	err := cmd.Start()
	a.swap.RUnlock()
	if err != nil {
		a.Logf("[%d] %s: %v, handing it back", slot, id, err)
		a.call(context.WithoutCancel(ctx), nil, "release", a.name, id)
		return
	}
	pg := cmd.Process.Pid
	kill := func() {
		syscall.Kill(-pg, syscall.SIGTERM)
		time.AfterFunc(a.IV.Kill, func() { syscall.Kill(-pg, syscall.SIGKILL) })
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	renew := time.NewTicker(a.IV.Renew)
	defer renew.Stop()
	lost, stopped, full := false, false, false
	cdone := ctx.Done()
loop:
	for {
		select {
		case err = <-waited:
			break loop
		case <-cdone:
			cdone, stopped = nil, true
			kill()
		case <-renew.C:
			_, rc, _ := a.call(ctx, nil, "renew", a.name, id)
			switch rc {
			case 0:
				a.setLane(i, func(l *Lane) { l.Renewed = time.Now() })
			case 3:
				if !lost {
					lost = true
					a.Logf("[%d] lease on %s lost: stopping it", slot, id)
					kill()
				}
			}
			if d := a.disk(); !d.OK(2) && !full && !lost {
				full = true
				a.Logf("[%d] disk nearly full (%s): stopping %s, handing it back", slot, d.Why(), id)
				kill()
			}
		}
	}
	rc := 0
	var x *exec.ExitError
	if errors.As(err, &x) {
		rc = x.ExitCode() // -1 when killed by a signal: handed back
	} else if err != nil {
		rc = -1
	}
	bg := context.WithoutCancel(ctx)
	switch {
	case lost:
		a.Logf("[%d] %s stopped (lease lost)", slot, id)
	case stopped, full:
		a.call(bg, nil, "release", a.name, id)
		a.Logf("[%d] %s handed back", slot, id)
	case rc == 0:
		b, _ := os.ReadFile(res)
		args := append([]string{"ok", a.name, id}, printable(string(b))...)
		if _, orc, err := a.call(bg, nil, args...); orc == 0 {
			a.Logf("[%d] %s passed", slot, id)
		} else {
			a.Logf("[%d] %s passed, but the hub did not take the result: %v", slot, id, errOr(err, orc))
		}
	case rc == 1:
		b, _ := os.ReadFile(res)
		reason, summary, _ := strings.Cut(string(b), "\n")
		args := append([]string{"fail", a.name, id}, printable(reason)...)
		if _, frc, err := a.call(bg, strings.NewReader(summary), args...); frc != 0 {
			a.Logf("[%d] %s failed; the hub did not take the report: %v", slot, id, errOr(err, frc))
		} else {
			a.Logf("[%d] %s failed", slot, id)
		}
	default:
		a.Logf("[%d] %s: exit %d, handing it back", slot, id, rc)
		a.call(bg, nil, "release", a.name, id)
	}
}

// Release hands back whatever the project's lanes hold (sat@.service's ExecStop: before systemd
// stops the processes).
func Release(ctx context.Context, p *conf.Project, c hub.Caller, w io.Writer) {
	fs, _ := filepath.Glob(filepath.Join(p.State, "slot*.claim"))
	for _, f := range fs {
		b, _ := os.ReadFile(f)
		if id := strings.TrimSpace(string(b)); id != "" {
			cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if _, err := c.Call(cctx, nil, w, w, p.Name, "release", p.NodeName(), id); err != nil {
				fmt.Fprintf(w, "could not hand %s back: %v\n", id, err)
			}
			cancel()
		}
		os.Remove(f)
	}
}
