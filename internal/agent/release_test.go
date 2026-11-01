package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RashKash103/satellite/internal/conf"
)

type recCaller struct{ calls []string }

func (c *recCaller) Call(_ context.Context, _ io.Reader, _, _ io.Writer, args ...string) (int, error) {
	c.calls = append(c.calls, strings.Join(args, " "))
	return 0, nil
}
func (c *recCaller) Gen() uint64  { return 0 }
func (c *recCaller) Name() string { return "rec" }
func (c *recCaller) Close() error { return nil }

// A stop (the unit's ExecStop, also at shutdown) hands the jobs back and ends a drain: a stopped
// project is not draining, and its next start must not drain.
func TestReleaseEndsADrain(t *testing.T) {
	d := t.TempDir()
	p := &conf.Project{Name: "p", Base: d, State: filepath.Join(d, "state")}
	os.MkdirAll(p.State, 0o755)
	os.WriteFile(filepath.Join(p.State, "drain"), nil, 0o644)
	os.WriteFile(filepath.Join(p.State, "slot1.claim"), []byte("job-1\n"), 0o644)
	os.WriteFile(filepath.Join(p.State, "slot2.claim"), nil, 0o644)
	c := &recCaller{}
	Release(context.Background(), p, c, io.Discard)
	if _, err := os.Stat(filepath.Join(p.State, "drain")); err == nil {
		t.Error("drain left behind after the stop")
	}
	if len(c.calls) != 1 || !strings.HasPrefix(c.calls[0], "p release ") || !strings.HasSuffix(c.calls[0], " job-1") {
		t.Errorf("calls %q", c.calls)
	}
	if fs, _ := filepath.Glob(filepath.Join(p.State, "slot*.claim")); len(fs) != 0 {
		t.Errorf("claims left: %v", fs)
	}
}
