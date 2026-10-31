package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
)

// Adopting running jobs. A replaced binary makes the agent re-execute itself at once
// (syscall.Exec: the same PID, so the service and its control group stay up). Its jobs, children in
// their own process groups, keep running; the new agent finds them in state/slotN.job, checks each
// is still that process (/proc/PID/stat: its start time and group), waits for it (wait4: it is still
// this process's child), and reports it from its exit status. A job's wrapper writes that status to
// state/slotN.exit too, for a job that isn't this process's child. It covers re-executing only: a
// crashed service's control group goes with it (KillMode=control-group), jobs and all.

// Job is what a lane's state/slotN.job records of its running job.
type Job struct {
	ID       string    `json:"id"`
	Line     string    `json:"line"`
	PID      int       `json:"pid"`
	PGID     int       `json:"pgid"`
	Start    uint64    `json:"start_ticks"` // /proc/PID/stat starttime: tells it from a later process with its PID
	Started  time.Time `json:"started"`
	Exit     string    `json:"exit"` // where the job's wrapper writes its exit status
	Result   string    `json:"result"`
	Progress string    `json:"progress"`
	alive    bool
}

// noExit: the job ended with no exit status recorded: handed back.
const noExit = -2

type slotFiles struct{ claim, result, prog, log, job, exit string }

func (a *Agent) files(slot int) slotFiles {
	f := func(ext string) string { return filepath.Join(a.P.State, fmt.Sprintf("slot%d.%s", slot, ext)) }
	return slotFiles{f("claim"), f("result"), f("progress"), f("log"), f("job"), f("exit")}
}

// JobCommand is Command for a lane's job: the job runs under a bash that writes its exit status to
// exit (beside, then renamed), whatever becomes of the agent meanwhile, and exits with it.
func JobCommand(env []string, exit, script string, args ...string) *exec.Cmd {
	w := `set -a; . "$SAT_BASE/config"; [ -f "$SAT_BASE/env" ] && . "$SAT_BASE/env"; set +a +e
PATH=` + shq(conf.Bin()) + `:"$HOME/.local/bin:$PATH"; "${@:2}"; rc=$?
printf '%s\n' "$rc" > "$1.new" && command mv -f "$1.new" "$1"; exit "$rc"`
	cmd := exec.Command("bash", append([]string{"-c", w, "sat", exit, script}, args...)...)
	cmd.Env = env
	return cmd
}

type procInfo struct {
	state byte
	pgid  int
	start uint64
}

// readProc: a process's state, group and start time, from /proc/PID/stat.
func readProc(pid int) (procInfo, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procInfo{}, err
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return procInfo{}, errors.New("bad stat")
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return procInfo{}, errors.New("bad stat")
	}
	pg, err1 := strconv.Atoi(f[2])
	st, err2 := strconv.ParseUint(f[19], 10, 64)
	if err1 != nil || err2 != nil || len(f[0]) != 1 {
		return procInfo{}, errors.New("bad stat")
	}
	return procInfo{state: f[0][0], pgid: pg, start: st}, nil
}

// is: the process is still j's (not one that took its PID since). zombie: an exited child not yet
// waited for counts.
func (j *Job) is(zombie bool) bool {
	if j.PID <= 0 || j.Start == 0 {
		return false
	}
	p, err := readProc(j.PID)
	return err == nil && p.start == j.Start && p.pgid == j.PGID && (zombie || p.state != 'Z')
}

// exitStatus: the status the job's wrapper recorded, or noExit.
func exitStatus(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return noExit
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return noExit
	}
	return n
}

// waitJob: the adopted job's exit status once it ends. wait4 when it is this process's child (it
// is, after an exec); else watch /proc and read the status its wrapper wrote.
func waitJob(j *Job) <-chan int {
	ch := make(chan int, 1)
	go func() {
		var ws syscall.WaitStatus
		for {
			p, err := syscall.Wait4(j.PID, &ws, 0, nil)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err == nil && p == j.PID {
				if ws.Exited() {
					ch <- ws.ExitStatus()
				} else {
					ch <- -1 // killed: handed back
				}
				return
			}
			break // not our child
		}
		for j.is(false) {
			time.Sleep(500 * time.Millisecond)
		}
		ch <- exitStatus(j.Exit)
	}()
	return ch
}

// running jobs: what each lane's state files say it was running when the last agent stopped. A
// job file without its claim (handed back by the unit's ExecStop) is stale: removed.
func (a *Agent) findJobs() map[int]*Job {
	fs, _ := filepath.Glob(filepath.Join(a.P.State, "slot*.job"))
	m := map[int]*Job{}
	for _, f := range fs {
		slot, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "slot"), ".job"))
		if err != nil || slot < 1 {
			continue
		}
		sf := a.files(slot)
		b, err1 := os.ReadFile(f)
		c, err2 := os.ReadFile(sf.claim)
		var j Job
		if err1 != nil || err2 != nil || json.Unmarshal(b, &j) != nil || j.ID == "" || strings.TrimSpace(string(c)) != j.ID {
			os.Remove(f)
			continue
		}
		j.alive = j.is(true)
		m[slot] = &j
	}
	return m
}

// handoff: written by an agent about to re-execute itself, read by the one it becomes.
type handoff struct {
	From string    `json:"from"`
	At   time.Time `json:"at"`
}

func (a *Agent) handoffPath() string { return filepath.Join(a.P.State, "restart") }

// restartNote: what the agent says after taking over n running jobs ("" when it took over none
// and wasn't handed over to).
func restartNote(h *handoff, version string, n int) string {
	jobs := fmt.Sprintf("adopted %d running job", n)
	if n != 1 {
		jobs += "s"
	}
	switch {
	case h == nil && n == 0:
		return ""
	case h == nil:
		return "restarted, " + jobs
	case h.From != version:
		return "updated from " + h.From + " to " + version + ", " + jobs
	}
	return "restarted for a replaced binary, " + jobs
}

func (a *Agent) readHandoff() *handoff {
	b, err := os.ReadFile(a.handoffPath())
	os.Remove(a.handoffPath())
	var h handoff
	if err != nil || json.Unmarshal(b, &h) != nil || time.Since(h.At) > 10*time.Minute {
		return nil
	}
	return &h
}
