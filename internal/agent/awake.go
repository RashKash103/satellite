package agent

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

// While any lane runs a job, the agent holds a logind inhibitor (systemd-inhibit, sleep and idle,
// mode block), so the machine does not suspend in the middle of one; desktops that honour logind
// inhibitors (KDE Plasma, GNOME) list it as what keeps the machine awake. KEEP_AWAKE=off in the
// project's config or ~/.sat/config turns it off. The inhibitor's command reads a pipe the agent
// holds open: when the agent closes it, exits, crashes or re-executes for an update (the pipe is
// close-on-exec), the command ends and logind drops the lock, so it can never outlive the jobs.
type awake struct {
	mu   sync.Mutex
	w    *os.File      // the agent's end of the pipe; nil while not held
	done chan struct{} // closed when the inhibitor's command exits
	gave bool          // logind refused every kind this busy spell: wait for the next one
}

// inhibitCmd: the command that holds the lock until its stdin closes. A variable, for tests.
var inhibitCmd = func(what, why string) *exec.Cmd {
	return exec.Command("systemd-inhibit", "--what="+what, "--who=sat", "--why="+why, "--mode=block", "cat")
}

// Blocking sleep needs a login session (polkit allows it to the user's active or inactive session,
// asks for an admin otherwise, as on a node nobody is logged in to); idle is allowed to anyone, and
// still keeps desktops from suspending the machine for being idle.
var inhibitKinds = []string{"sleep:idle", "idle"}

// set: hold the inhibitor while busy and on, drop it otherwise. logf reports the changes.
func (k *awake) set(busy, on bool, why string, logf func(string, ...any)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.w != nil {
		select {
		case <-k.done: // it ended by itself (logind refused it, or it was killed)
			k.w.Close()
			k.w = nil
		default:
		}
	}
	switch {
	case busy && on && k.w == nil && !k.gave:
		if _, err := exec.LookPath(inhibitCmd("idle", "").Path); err != nil {
			k.gave = true // no systemd-inhibit on this machine: nothing to hold, nothing to say
			break
		}
		for n, what := range inhibitKinds {
			if k.hold(what, why) {
				if n > 0 {
					logf("keeping the machine from idling into sleep while jobs run (blocking sleep itself needs a login session)")
				} else {
					logf("keeping the machine awake while jobs run (KEEP_AWAKE=off to allow sleep)")
				}
				return
			}
		}
		k.gave = true
		logf("could not keep the machine awake: logind refused an inhibitor (systemd-inhibit)")
	case !(busy && on) && k.w != nil:
		k.w.Close()
		k.w = nil
		logf("no job running: the machine may sleep again")
	}
	if !busy {
		k.gave = false
	}
}

// hold: start the inhibitor's command for what; true when it is still running a moment later
// (logind refuses at once, so a refusal ends it within that moment).
func (k *awake) hold(what, why string) bool {
	c := inhibitCmd(what, why)
	r, w, err := os.Pipe()
	if err != nil {
		return false
	}
	c.Stdin = r
	if err := c.Start(); err != nil {
		r.Close()
		w.Close()
		return false
	}
	r.Close()
	done := make(chan struct{})
	go func() { c.Wait(); close(done) }()
	select {
	case <-done:
		w.Close()
		return false
	case <-time.After(holdCheck):
	}
	k.w, k.done = w, done
	return true
}

// holdCheck: how long a refused inhibitor takes at most to end. A variable, for tests.
var holdCheck = 500 * time.Millisecond

// held: the inhibitor's command is running.
func (k *awake) held() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.w == nil {
		return false
	}
	select {
	case <-k.done:
		return false
	default:
		return true
	}
}
