package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RashKash103/satellite/internal/conf"
)

func TestDiskOK(t *testing.T) {
	for _, c := range []struct {
		d       Disk
		ok, ok2 bool
	}{
		{Disk{Avail: 30 * gb, Need: 20 * gb}, true, true},
		{Disk{Avail: 15 * gb, Need: 20 * gb}, false, true},
		{Disk{Avail: 5 * gb, Need: 20 * gb}, false, false},
		{Disk{Avail: 30 * gb, Need: 20 * gb, Used: 60 * gb, Cap: 50 * gb}, false, true},
		{Disk{Avail: 30 * gb, Need: 20 * gb, Used: 40 * gb, Cap: 50 * gb}, true, true},
	} {
		if c.d.OK(1) != c.ok || c.d.OK(2) != c.ok2 {
			t.Errorf("%+v: %v %v", c.d, c.d.OK(1), c.d.OK(2))
		}
	}
	if w := (Disk{Avail: 30 * gb, Need: 20 * gb, Used: 3 * gb, Cap: 50 * gb}).Why(); w != "30 GB free (keeps 20 GB free), project 3 GB of 50 GB" {
		t.Error(w)
	}
}

func TestMeasure(t *testing.T) {
	d := t.TempDir()
	p := &conf.Project{Name: "p", Base: d, State: filepath.Join(d, "state")}
	os.MkdirAll(p.State, 0o755)
	os.WriteFile(filepath.Join(d, "config"), []byte("MIN_FREE_GB=0\nMIN_FREE_PCT=100\nMAX_GB=7\n"), 0o644)
	m := Measure(p, false)
	if m.Need <= m.Avail || m.Cap != 7*gb || m.OK(1) {
		t.Errorf("%+v", m)
	}
	if b, _ := os.ReadFile(filepath.Join(p.State, "du")); len(b) == 0 {
		t.Error("no du cache")
	}
	os.WriteFile(filepath.Join(d, "config"), []byte("MIN_FREE_GB=0\nMIN_FREE_PCT=0\n"), 0o644)
	if m := Measure(p, false); !m.OK(1) || m.Used != 0 {
		t.Errorf("%+v", m)
	}
}
