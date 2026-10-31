package view

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/ui"
)

func TestParseErrors(t *testing.T) {
	for in, want := range map[string]string{
		``:                              "printed nothing",
		`{"blocks": [`:                  "not a JSON document",
		`hello`:                         "not a JSON document",
		`{"blocks": []} {"blocks": []}`: "more than one",
		`{"title": "x"}`:                `no "blocks"`,
		`{"blocks": [{"text": "x"}]}`:   "no type",
		`{"blocks": [{"type": "metric", "name": "q"}]}`:                               "numeric value",
		`{"blocks": [{"type": "metric", "value": 3}]}`:                                "needs a name",
		`{"blocks": [{"type": "progress", "value": "half"}]}`:                         "not a JSON document",
		`{"blocks": [{"type": "alert", "level": "panic", "text": "x"}]}`:              "want warn or error",
		`{"blocks": [{"type": "timeline", "items": [{"label": "a", "state": "x"}]}]}`: "want done",
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
	d, err := Parse([]byte(`{"blocks": [{"type": "sankey"}, {"type": "text", "text": "a\nb"}]}`))
	if err != nil || len(d.Blocks) != 2 {
		t.Fatal(err)
	}
	if l := (Render{}).Block(d.Blocks[0]); !strings.Contains(l[0], "sankey") {
		t.Error(l)
	}
}

const sample = `{"blocks": [
 {"type": "text", "lines": ["hello", "\u001b[31mred\u001b[0m"]},
 {"type": "kv", "title": "Hub", "rows": [["state", "up"], ["lanes", 2], ["ok", true]]},
 {"type": "table", "columns": ["id", "took"], "rows": [["j1", 1.5], ["j22", 12]]},
 {"type": "progress", "label": "build", "value": 0.7, "note": "linking"},
 {"type": "bar", "title": "Jobs", "data": [{"label": "queued", "value": 4}, {"label": "done", "value": 12}]},
 {"type": "timeline", "items": [{"label": "fetch", "start": 1700000000, "end": 1700000060, "state": "done"}, {"label": "test", "start": 1700000060, "state": "running"}, {"label": "ship", "state": "waiting"}]},
 {"type": "metric", "name": "queued", "value": 4, "unit": "jobs"},
 {"type": "line", "title": "Load", "series": [{"name": "cpu", "points": [[0, 10], [1, 30], [2, 20]]}]},
 {"type": "alert", "level": "error", "text": "lane 2 is stuck"}
]}`

func TestPlainFallbacks(t *testing.T) {
	d, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHist()
	for _, v := range []float64{9, 2, 4} {
		h.Push("queued", v)
	}
	r := Render{Width: 80, Hist: h, Now: time.Unix(1700000120, 0)}
	got := strings.Join(r.Doc(d), "\n")
	clock := func(s int64) string { return time.Unix(s, 0).Format("15:04:05") }
	for _, want := range []string{
		"error: lane 2 is stuck\n", // alerts first
		"hello\nred\n",
		"Hub\nstate  up\nlanes  2\nok     true",
		"id   took\nj1   1.5\nj22  12",
		"build 70% linking",
		"Jobs\nqueued  4\ndone    12",
		"fetch  done     " + clock(1700000000) + "-" + clock(1700000060) + "  (1m00s)",
		"test   running  " + clock(1700000060) + "-          (1m00s)",
		"ship   waiting  -",
		"queued 4 jobs (min 2, max 9 over 3)",
		"Load\ncpu  20  (min 10, max 30 over 3)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "\x1b█░") {
		t.Errorf("escapes or bars in plain output:\n%q", got)
	}
}

func TestStyledBlocks(t *testing.T) {
	d, _ := Parse([]byte(sample))
	for _, w := range []int{20, 50, 120} {
		r := Render{Width: w, Color: true, S: ui.Palette(true), Hist: NewHist(), Now: time.Unix(1700000120, 0)}
		ls := r.Doc(d)
		got := ui.Strip(strings.Join(ls, "\n"))
		for _, want := range []string{"✗ lane 2 is stuck", "70%", "queued", "fetch", "cpu"} {
			if !strings.Contains(got, want) {
				t.Errorf("%d: lacks %q:\n%s", w, want, got)
			}
		}
		if w >= 50 && !strings.Contains(got, "█") {
			t.Errorf("%d: no bars:\n%s", w, got)
		}
		if w >= 50 && !strings.ContainsAny(got, "⠀⠁⠂⠃⠄⠅⠆⠇⡀⢀⣀⠉⠒⠤") {
			t.Errorf("%d: no line chart:\n%s", w, got)
		}
		for _, l := range ls {
			if ui.Width(l) > max(w, 20)+2 {
				t.Errorf("%d: line %d wide: %q", w, ui.Width(l), ui.Strip(l))
			}
		}
	}
	// Odd input draws without trouble.
	odd := `{"blocks": [{"type": "bar", "data": []}, {"type": "bar", "data": [{"label": "neg", "value": -3}, {"label": "pos", "value": 2}]},
	 {"type": "line", "series": [{"name": "one", "points": [[5, 5]]}]}, {"type": "line", "series": []}, {"type": "timeline", "items": []},
	 {"type": "progress", "label": "over", "value": 7}, {"type": "table", "rows": []}, {"type": "kv", "rows": [[]]}]}`
	od, err := Parse([]byte(odd))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []bool{true, false} {
		ls := Render{Width: 60, Color: c, S: ui.Palette(false)}.Doc(od)
		if s := ui.Strip(strings.Join(ls, "\n")); !strings.Contains(s, "100%") || !strings.Contains(s, "neg") {
			t.Errorf("%v:\n%s", c, s)
		}
	}
}

func TestHistCap(t *testing.T) {
	h := NewHist()
	for i := range HistCap + 50 {
		h.Push("a", float64(i))
	}
	if v := h.Get("a"); len(v) != HistCap || v[0] != 50 || v[HistCap-1] != HistCap+49 {
		t.Fatalf("%d values from %v", len(v), v[0])
	}
	for i := range HistNames + 5 {
		h.Push(fmt.Sprintf("m%d", i), 1)
	}
	if len(h.M) != HistNames || h.Get("a") != nil || h.Get("m0") != nil || h.Get(fmt.Sprintf("m%d", HistNames+4)) == nil {
		t.Errorf("%d names; a=%v", len(h.M), h.Get("a") != nil)
	}
	path := filepath.Join(t.TempDir(), "views", "x.metrics.json")
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}
	g := LoadHist(path)
	g.Push("m6", 2)
	if v := g.Get("m6"); len(v) != 2 || v[1] != 2 {
		t.Error(v)
	}
	if LoadHist(filepath.Join(t.TempDir(), "none")).Get("x") != nil {
		t.Error("a missing file has values")
	}
	d, _ := Parse([]byte(`{"blocks": [{"type": "metric", "name": "q", "value": 3}, {"type": "metric", "name": "q", "value": 4}]}`))
	g.Record(d)
	if v := g.Get("q"); len(v) != 2 {
		t.Error(v)
	}
}
