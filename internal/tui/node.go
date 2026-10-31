package tui

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// nodeStats: this machine's CPU, memory and load, sampled from /proc every 2 s while the live
// view runs, for the sparklines in Details.
type nodeStats struct {
	proc               string // /proc (a directory of fakes in tests)
	at                 time.Time
	cpu, mem, load     []float64 // cpu and mem 0..100, load as /proc/loadavg's first field
	busy, total        uint64    // the last /proc/stat reading
	cpus               int
	memTotal, memAvail uint64 // kB
}

const statsCap = 120

func newNodeStats() *nodeStats {
	return &nodeStats{proc: "/proc"}
}

func push(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > statsCap {
		s = s[len(s)-statsCap:]
	}
	return s
}

// sample reads /proc once (at most every 2 s).
func (n *nodeStats) sample(now time.Time) {
	if now.Sub(n.at) < 2*time.Second {
		return
	}
	n.at = now
	if b, err := os.ReadFile(n.proc + "/stat"); err == nil {
		cpus := 0
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) < 5 {
				continue
			}
			if f[0] != "cpu" {
				if strings.HasPrefix(f[0], "cpu") {
					cpus++
				}
				continue
			}
			var busy, total uint64
			for i, x := range f[1:] {
				v, _ := strconv.ParseUint(x, 10, 64)
				total += v
				if i != 3 && i != 4 { // idle, iowait
					busy += v
				}
			}
			if n.total > 0 && total > n.total {
				n.cpu = push(n.cpu, 100*float64(busy-n.busy)/float64(total-n.total))
			}
			n.busy, n.total = busy, total
		}
		n.cpus = max(cpus, 1)
	}
	if b, err := os.ReadFile(n.proc + "/meminfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				n.memTotal = v
			case "MemAvailable:":
				n.memAvail = v
			}
		}
		if n.memTotal > 0 {
			n.mem = push(n.mem, 100*float64(n.memTotal-min(n.memAvail, n.memTotal))/float64(n.memTotal))
		}
	}
	if b, err := os.ReadFile(n.proc + "/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				n.load = push(n.load, v)
			}
		}
	}
}

func last(s []float64) (float64, bool) {
	if len(s) == 0 {
		return 0, false
	}
	return s[len(s)-1], true
}
