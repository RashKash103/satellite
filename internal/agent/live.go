package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
)

// Live configuration: the agent re-reads the project's config whenever it changes (checked every
// state tick) and applies what it can at once. SLOTS opens or closes lanes: a closing lane that
// runs a job drains (finishes it, claims nothing more, then closes); idle lanes close first. Keys
// that shape the connection to the hub only apply at the next start: they show as pending in
// `sat status` and the live view. Everything else is read where it is used (jobs source the config
// themselves; the disk floor, alerts and KEEP_AWAKE read the agent's live copy or the file).

// restartKeys: config keys that only take effect when the service starts again.
var restartKeys = []string{"HUB", "HUB_IP", "HUB_PORT", "HUB_USER", "SAT_NAME", "TRANSPORT"}

// RestartOnly: whether a config key only applies when the service starts again.
func RestartOnly(k string) bool { return slices.Contains(restartKeys, k) }

// confDiff: the keys whose values differ between two configs, sorted; split into those that apply
// live and those that need a restart.
func confDiff(old, cur map[string]string) (live, restart []string) {
	seen := map[string]bool{}
	for _, m := range []map[string]string{old, cur} {
		for k := range m {
			if seen[k] || old[k] == cur[k] {
				continue
			}
			seen[k] = true
			if slices.Contains(restartKeys, k) {
				restart = append(restart, k)
			} else {
				live = append(live, k)
			}
		}
	}
	slices.Sort(live)
	slices.Sort(restart)
	return live, restart
}

// laneState: what slot planning needs of one lane.
type laneState struct{ open, retiring, busy bool }

// slotPlan: how to get from the lanes as they are to want open ones. unretire: retiring lanes that
// stay open after all; reopen: closed lanes to open again; add: new lanes after the last; retire:
// open lanes to close (idle ones close at once, busy ones after their job).
type slotPlan struct {
	unretire, reopen, retire []int
	add                      int
}

// planSlots: raising cancels drains first (the lowest lanes first), then reopens closed lanes
// (lowest first), then adds lanes; lowering retires idle lanes first, then busy ones, each from
// the highest down, exactly as many as it must.
func planSlots(ls []laneState, want int) slotPlan {
	want = max(want, 1)
	var p slotPlan
	active := 0
	for _, l := range ls {
		if l.open && !l.retiring {
			active++
		}
	}
	switch {
	case want > active:
		need := want - active
		for i, l := range ls {
			if need > 0 && l.open && l.retiring {
				p.unretire = append(p.unretire, i)
				need--
			}
		}
		for i, l := range ls {
			if need > 0 && !l.open {
				p.reopen = append(p.reopen, i)
				need--
			}
		}
		p.add = need
	case want < active:
		need := active - want
		for _, busy := range []bool{false, true} {
			for i := len(ls) - 1; i >= 0 && need > 0; i-- {
				if l := ls[i]; l.open && !l.retiring && l.busy == busy {
					p.retire = append(p.retire, i)
					need--
				}
			}
		}
	}
	return p
}

// get: the live value of a config key (the project's config as last read, else ~/.sat/config).
func (a *Agent) get(k, def string) string {
	a.mu.Lock()
	v := a.cfg[k]
	a.mu.Unlock()
	if v != "" {
		return v
	}
	if v := conf.Node()[k]; v != "" {
		return v
	}
	return def
}

func (a *Agent) configPath() string { return filepath.Join(a.P.Base, "config") }

// reload: the config changed on disk: apply SLOTS, note keys that wait for a restart.
func (a *Agent) reload(ctx context.Context) {
	fi, err := os.Stat(a.configPath())
	if err != nil {
		return
	}
	a.mu.Lock()
	same := fi.ModTime().Equal(a.cfgMod)
	a.mu.Unlock()
	if same {
		return
	}
	m, err := conf.Parse(a.configPath(), nil)
	if err != nil {
		return
	}
	a.mu.Lock()
	old := a.cfg
	a.cfg, a.cfgMod = m, fi.ModTime()
	_, pending := confDiff(a.startCfg, m)
	was := a.pending
	a.pending = pending
	live, _ := confDiff(old, m)
	if len(live) > 0 {
		a.cfgGen++ // a lane waiting on disk room, say, checks again at once with the new values
	}
	a.mu.Unlock()
	if len(live) > 0 {
		a.Logf("config changed: %s (applied now)", strings.Join(live, ", "))
	}
	if !slices.Equal(was, pending) && len(pending) > 0 {
		a.Logf("config changed: %s (applies when the service starts again: sat %s stop && sat %s start)", strings.Join(pending, ", "), a.P.Name, a.P.Name)
	}
	if slices.Contains(live, "SLOTS") {
		n, _ := strconv.Atoi(m["SLOTS"])
		if m["SLOTS"] == "" {
			n = 1
		}
		a.setSlots(ctx, n)
	}
}

// setSlots: open or close lanes until n are open (planSlots), logging what happens to each.
func (a *Agent) setSlots(ctx context.Context, n int) {
	n = max(n, 1)
	a.mu.Lock()
	from := a.want
	ls := make([]laneState, len(a.st.Lanes))
	for i, l := range a.st.Lanes {
		ls[i] = laneState{open: !a.off[i], retiring: a.retire[i], busy: l.ID != ""}
	}
	p := planSlots(ls, n)
	a.want = n
	var start []int
	var said []string
	for _, i := range p.unretire {
		delete(a.retire, i)
		said = append(said, "lane "+strconv.Itoa(i+1)+" stays open (its drain is cancelled)")
	}
	for _, i := range p.reopen {
		delete(a.off, i)
		start = append(start, i)
		said = append(said, "lane "+strconv.Itoa(i+1)+" opens")
	}
	for range p.add {
		i := len(a.st.Lanes)
		a.st.Lanes = append(a.st.Lanes, Lane{Slot: i + 1, Percent: -1})
		start = append(start, i)
		said = append(said, "lane "+strconv.Itoa(i+1)+" opens")
	}
	for _, i := range p.retire {
		a.retire[i] = true
		if ls[i].busy {
			said = append(said, "lane "+strconv.Itoa(i+1)+" finishes its job, then closes")
		} else {
			said = append(said, "lane "+strconv.Itoa(i+1)+" closes")
		}
	}
	a.mu.Unlock()
	if len(said) > 0 {
		a.Logf("SLOTS %d -> %d: %s", from, n, strings.Join(said, "; "))
	}
	for _, i := range start {
		a.wg.Go(func() { a.lane(ctx, i) })
	}
}

// leave: whether lane i closes now (it was retired); a closing lane is marked closed at once, under
// the same lock setSlots plans with, so a lane is either still open or closed, never lost between.
func (a *Agent) leave(i int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.retire[i] {
		return false
	}
	delete(a.retire, i)
	a.off[i] = true
	return true
}

func (a *Agent) configGen() uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfgGen
}

func (a *Agent) retiring(i int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.retire[i]
}

// laneNap: nap, waking early when lane i is retired or a live setting changes (MAX_GB raised while
// the lane waits for disk room: it looks again now, not at the end of its nap).
func (a *Agent) laneNap(ctx context.Context, i int, d time.Duration) {
	end := time.Now().Add(d)
	gen := a.configGen()
	for time.Now().Before(end) && !a.stopping() && !a.retiring(i) && a.configGen() == gen && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(min(time.Until(end), 250*time.Millisecond)):
		}
	}
}
