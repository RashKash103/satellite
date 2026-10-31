package agent

import (
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

// Disk: a node keeps the larger of MIN_FREE_GB (default 20) and MIN_FREE_PCT (default 10) percent of
// the filesystem under the project's directory free, and MAX_GB (default 0: no cap) caps that
// directory. The settings are read from the config at each check, so a change applies at once.
type Disk struct {
	Avail int64 `json:"avail"`          // bytes free for us
	Need  int64 `json:"need"`           // the floor
	Used  int64 `json:"used"`           // the project's size (0: not measured)
	Cap   int64 `json:"cap"`            // MAX_GB in bytes, 0: none
	Size  int64 `json:"size,omitempty"` // the filesystem's size
}

const gb = 1 << 30

func setting(m map[string]string, k string, def int64) int64 {
	if v, err := strconv.ParseInt(strings.TrimSpace(m[k]), 10, 64); err == nil {
		return v
	}
	return def
}

// du: the project's size, measured at most every 30 min at low priority, cached in state/du (KiB,
// as the bash node kept it).
func du(p *conf.Project) int64 {
	c := filepath.Join(p.State, "du")
	if fi, err := os.Stat(c); err != nil || time.Since(fi.ModTime()) > 30*time.Minute {
		args := []string{"-sk", p.Base}
		name := "du"
		if _, err := exec.LookPath("ionice"); err == nil {
			name, args = "ionice", append([]string{"-c3", "nice", "du"}, args...)
		} else if _, err := exec.LookPath("nice"); err == nil {
			name, args = "nice", append([]string{"du"}, args...)
		}
		out, _ := exec.Command(name, args...).Output()
		if f := strings.Fields(string(out)); len(f) > 0 {
			conf.WriteAtomic(c, []byte(f[0]+"\n"), 0o644)
		}
	}
	b, _ := os.ReadFile(c)
	k, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return k * 1024
}

// Measure the disk for the project; size: also its size (cached), which the cap needs anyway.
func Measure(p *conf.Project, size bool) Disk {
	m, err := conf.Parse(filepath.Join(p.Base, "config"), nil)
	if err != nil {
		m = p.Conf
	}
	var d Disk
	var s syscall.Statfs_t
	if syscall.Statfs(p.Base, &s) == nil {
		d.Avail, d.Size = int64(s.Bavail)*int64(s.Bsize), int64(s.Blocks)*int64(s.Bsize)
		d.Need = max(setting(m, "MIN_FREE_GB", 20)*gb, int64(s.Blocks)*int64(s.Bsize)*setting(m, "MIN_FREE_PCT", 10)/100)
	}
	d.Cap = setting(m, "MAX_GB", 0) * gb
	if d.Cap > 0 || size {
		d.Used = du(p)
	}
	return d
}

// OK: room to take work. div 2: the floor for stopping running work, half of it (no cap then).
func (d Disk) OK(div int64) bool {
	if d.Avail < d.Need/div {
		return false
	}
	return div != 1 || d.Cap <= 0 || d.Used <= d.Cap
}

func (d Disk) Why() string {
	s := fmt.Sprintf("%d GB free (keeps %d GB free)", d.Avail/gb, d.Need/gb)
	if d.Used > 0 {
		s += fmt.Sprintf(", project %d GB", d.Used/gb)
		if d.Cap > 0 {
			s += fmt.Sprintf(" of %d GB", d.Cap/gb)
		}
	}
	return s
}
