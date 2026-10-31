package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
	"github.com/RashKash103/satellite/internal/view"
)

// viewNode: testNode's demo project, with views.
func viewNode(t *testing.T) string {
	home := testNode(t)
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("FORCE_COLOR", "")
	d := filepath.Join(conf.Dir("demo"), "bundle", "sat", "views")
	os.MkdirAll(d, 0o755)
	for n, body := range map[string]string{
		"colour": "#!/bin/sh\n# sat-view: title=In colour\nprintf '\\033[31mred\\033[0m and \\033[2Jplain color=%s\\n' \"${SAT_VIEW_COLOR:-off}\"\n",
		"board":  "#!/bin/sh\n# sat-view: title=Board; format=json\necho '{\"blocks\": [{\"type\": \"progress\", \"label\": \"done\", \"value\": 0.7}, {\"type\": \"bar\", \"data\": [{\"label\": \"queued\", \"value\": 3}]}, {\"type\": \"metric\", \"name\": \"q\", \"value\": 3, \"unit\": \"jobs\"}]}'\n",
		"broken": "#!/bin/sh\n# sat-view: format=json\necho 'not json'\n",
		"slow":   "#!/bin/sh\n# sat-view: timeout=0.2\necho before; sleep 5\n",
		"lane":   "#!/bin/sh\n# sat-view: scope=lane; mode=stream\necho \"lane $SAT_SLOT job $SAT_JOB claim $SAT_CLAIM once=$SAT_VIEW_ONCE\"\n",
		"oops":   "#!/bin/sh\n# sat-view: mode=sideways\n",
	} {
		os.WriteFile(filepath.Join(d, n), []byte(body), 0o755)
	}
	return home
}

func sat(args ...string) (string, int) {
	var rc int
	out := captureAll(func() { rc = Main("dev", append([]string{"sat"}, args...)) })
	return out, rc
}

// captureAll: stdout and stderr together.
func captureAll(f func()) string {
	r, w, _ := os.Pipe()
	oo, oe := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- b.String()
	}()
	f()
	w.Close()
	os.Stdout, os.Stderr = oo, oe
	return <-done
}

func TestViewsList(t *testing.T) {
	viewNode(t)
	out, rc := sat("demo", "views")
	for _, w := range []string{"NAME    TITLE", "board   Board         project  snapshot  json", "lane    lane          lane     stream    text", "oops    (bad header)  project", "sat: sat/views/oops: bad header: mode=sideways: want snapshot or stream"} {
		if !strings.Contains(out, w) {
			t.Errorf("lacks %q:\n%s", w, out)
		}
	}
	if rc != 1 {
		t.Errorf("a bad header exits %d", rc)
	}
}

func TestViewOncePlain(t *testing.T) {
	viewNode(t)
	if out, rc := sat("demo", "view", "colour"); out != "red and plain color=off\n" || rc != 0 {
		t.Errorf("%q %d", out, rc)
	}
	out, rc := sat("demo", "view", "board")
	if rc != 0 || !strings.Contains(out, "done 70%\n\nqueued  3\n\nq 3 jobs") || strings.ContainsAny(out, "\x1b█") {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "broken"); rc == 0 || !strings.Contains(out, "not a JSON document") {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "slow"); rc != 1 || !strings.Contains(out, "before\n") || !strings.Contains(out, "view slow timed out after 0.2s and was stopped") {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "nope"); rc != 2 || !strings.Contains(out, "demo has no view nope") {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "oops"); rc != 2 || !strings.Contains(out, "bad header: mode=sideways") {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "board", "--lane", "1"); rc != 2 || !strings.Contains(out, "--lane is for lane views") {
		t.Errorf("%q %d", out, rc)
	}
	// Coloured, the codes are kept (colour ones only) and the blocks are drawn.
	t.Setenv("CLICOLOR_FORCE", "1")
	if out, _ := sat("demo", "view", "colour"); !strings.Contains(out, "red") || !strings.Contains(out, "color=1") || strings.Contains(out, "\x1b[2J") || !strings.Contains(out, "\x1b[") {
		t.Errorf("%q", out)
	}
	if out, _ := sat("demo", "view", "board"); !strings.Contains(out, "█") {
		t.Errorf("%q", out)
	}
}

func lanes(t *testing.T, ls []agent.Lane) {
	st := agent.State{Project: "demo", Running: true, Updated: time.Now(), Lanes: ls}
	b, _ := json.Marshal(st)
	os.WriteFile(filepath.Join(conf.Dir("demo"), "state", "agent.json"), b, 0o644)
}

func TestLaneViewPicksTheLane(t *testing.T) {
	viewNode(t)
	if out, rc := sat("demo", "view", "lane"); rc != 2 || !strings.Contains(out, "demo is not running") {
		t.Errorf("%q %d", out, rc)
	}
	lanes(t, []agent.Lane{{Slot: 1}, {Slot: 2, ID: "j2", Line: "j2 x y"}})
	if out, rc := sat("demo", "view", "lane"); rc != 0 || out != "lane 2 job j2 claim j2 x y once=1\n" {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "lane", "--lane", "1"); rc != 2 || !strings.Contains(out, "lane 1 has no job now") {
		t.Errorf("%q %d", out, rc)
	}
	lanes(t, []agent.Lane{{Slot: 1, ID: "j1", Line: "j1"}, {Slot: 2, ID: "j2", Line: "j2"}})
	if out, rc := sat("demo", "view", "lane"); rc != 2 || !strings.Contains(out, "lanes 1 2 have jobs: which one? --lane N") {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "lane", "--lane=1"); rc != 0 || !strings.Contains(out, "lane 1 job j1") {
		t.Errorf("%q %d", out, rc)
	}
	if out, rc := sat("demo", "view", "lane", "--lane", "9"); rc != 2 || !strings.Contains(out, "no lane 9") {
		t.Errorf("%q %d", out, rc)
	}
}

func TestViewWatchPlain(t *testing.T) {
	viewNode(t)
	d := filepath.Join(conf.Dir("demo"), "bundle", "sat", "views")
	os.WriteFile(filepath.Join(d, "count"), []byte("#!/bin/sh\n# sat-view: every=0.1; format=json\nn=$(cat \"$SAT_BASE/state/n\" 2>/dev/null || echo 0); n=$((n+1)); echo $n > \"$SAT_BASE/state/n\"\n[ $n -ge 4 ] && kill -INT $PPID $(cat \"$SAT_BASE/state/me\")\necho \"{\\\"blocks\\\": [{\\\"type\\\": \\\"metric\\\", \\\"name\\\": \\\"n\\\", \\\"value\\\": $n}]}\"\n"), 0o755)
	os.WriteFile(filepath.Join(conf.Dir("demo"), "state", "me"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	out, rc := sat("demo", "view", "count", "-w")
	if rc != 0 || strings.Count(out, "== count ") < 3 || !strings.Contains(out, "n 3 (min 1, max 3 over 3)") {
		t.Errorf("%q %d", out, rc)
	}
	// -w kept the metric's values.
	if h := view.LoadHist(view.HistFile(mustLoad(t, "demo"), "count")); len(h.Get("n")) < 3 {
		t.Errorf("history %v", h.Get("n"))
	}
}

func TestAlertsCommand(t *testing.T) {
	viewNode(t)
	if out, rc := sat("alerts"); rc != 0 || out != "no alerts\n" {
		t.Errorf("%q %d", out, rc)
	}
	st := agent.State{Project: "demo", Running: true, Updated: time.Now(), Alerts: []agent.Alert{
		{ID: "disk", Level: agent.Warn, Since: time.Now().Add(-2 * time.Minute), Text: "not taking work: 1 GB free"},
		{ID: "hub", Level: agent.Error, Since: time.Now().Add(-7 * time.Minute), Text: "the hub has not answered for 7m00s"}}}
	b, _ := json.Marshal(st)
	os.WriteFile(filepath.Join(conf.Dir("demo"), "state", "agent.json"), b, 0o644)
	out, rc := sat("demo", "alerts")
	want := "PROJECT  LEVEL  SINCE  ALERT\ndemo     error  7m00s  the hub has not answered for 7m00s\ndemo     warn   2m00s  not taking work: 1 GB free\n"
	if rc != 0 || out != want {
		t.Errorf("%q\nwant %q", out, want)
	}
	if out, _ := sat("demo", "status"); !strings.Contains(out, "alert: error: the hub has not answered for 7m00s (since 7m00s)\nalert: warn: not taking work") {
		t.Errorf("status: %s", out)
	}
	var b2 strings.Builder
	s := sample()
	s.Alerts = st.Alerts
	s.render(ui.Styled(&b2, true, 100), time.Now())
	if got := ui.Strip(b2.String()); !strings.Contains(got, "alert   ✗ the hub has not answered for 7m00s  7m00s") {
		t.Errorf("styled:\n%s", got)
	}
}
