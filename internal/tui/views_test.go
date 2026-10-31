package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
)

func addView(name, body string) {
	d := filepath.Join(conf.Dir("hello"), "bundle", "sat", "views")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, name), []byte(body), 0o755)
}

// withViews: node(t), with hello's views: a text snapshot, a json snapshot and a lane stream.
func withViews(t *testing.T) *model {
	m := node(t)
	addView("queue", "#!/bin/sh\n# sat-view: title=Queue; every=0.2\necho \"queued 3 run $SAT_VIEW_MODE\"\n")
	addView("board", "#!/bin/sh\n# sat-view: title=Board; every=0.2; format=json\necho '{\"blocks\": [{\"type\": \"progress\", \"label\": \"done\", \"value\": 0.5}, {\"type\": \"metric\", \"name\": \"q\", \"value\": 4}]}'\n")
	addView("tail", "#!/usr/bin/env bash\n# sat-view: title=Tail; scope=lane; mode=stream\necho $$ > \"$SAT_BASE/state/tail.pid\"\necho \"job $SAT_JOB lane $SAT_SLOT\"\nwhile :; do echo tick; sleep 0.1; done\n")
	m.refresh()
	m.syncTabs()
	return m
}

func keys(m *model) string {
	var s []string
	for _, e := range m.tabList() {
		s = append(s, e.key)
	}
	return strings.Join(s, " ")
}

// drive runs cmd and what it leads to, feeding the messages to m until cond holds.
func drive(t *testing.T, m *model, cmd tea.Cmd, what string, cond func() bool) {
	t.Helper()
	msgs := make(chan tea.Msg, 256)
	var run func(c tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			msg := c()
			if b, ok := msg.(tea.BatchMsg); ok {
				for _, x := range b {
					run(x)
				}
				return
			}
			if msg != nil {
				msgs <- msg
			}
		}()
	}
	run(cmd)
	end := time.After(10 * time.Second)
	for !cond() {
		select {
		case msg := <-msgs:
			if _, ok := msg.(tick); ok {
				continue // the 1 s refresh would reread the fake state; not needed here
			}
			_, c := m.Update(msg)
			run(c)
		case <-end:
			t.Fatalf("timed out waiting for %s:\n%s", what, screen(m))
		}
	}
}

func TestViewTabsPerProjectAndLane(t *testing.T) {
	m := withViews(t)
	if k := keys(m); k != "log service output alerts view:board view:queue view:tail" {
		t.Fatalf("lane 1 (busy): %s", k)
	}
	key(m, "2")
	key(m, "j") // lane 2: idle, so no lane views
	if k := keys(m); k != "log service output alerts view:board view:queue" {
		t.Errorf("lane 2: %s", k)
	}
	key(m, "1")
	key(m, "j") // other: no views at all
	if k := keys(m); k != "log service output alerts" {
		t.Errorf("other: %s", k)
	}
	key(m, "k")
	// A changed bundle is read again.
	addView("new", "#!/bin/sh\necho new\n")
	m.refresh()
	if k := keys(m); !strings.Contains(k, "view:new") {
		t.Errorf("after a bundle change: %s", k)
	}
	v := screen(m)
	if !strings.Contains(v, "◆ Board") && !strings.Contains(v, "of 8) [ ]") {
		t.Errorf("tabs not shown:\n%s", v)
	}
}

func TestViewRunsStartAndStop(t *testing.T) {
	m := withViews(t)
	c := key(m, "w") // the first view: board
	if m.curTab().key != "view:board" {
		t.Fatal(m.curTab().key)
	}
	if m.vr == nil || m.vr.v.Name != "board" {
		t.Fatal("no run")
	}
	r := m.vr
	drive(t, m, c, "two board runs", func() bool { return r.runs >= 2 })
	v := screen(m)
	if !strings.Contains(v, "done") || !strings.Contains(v, "50%") || !strings.Contains(v, "q  4") || !strings.Contains(v, "Board · every 0.2s") {
		t.Errorf("board:\n%s", v)
	}
	if len(r.hist.Get("q")) < 2 {
		t.Error("the metric's values were not kept")
	}
	c = key(m, "w") // queue: board stops
	if r.ctx.Err() == nil || m.vr == nil || m.vr.v.Name != "queue" {
		t.Fatal("board still runs")
	}
	q := m.vr
	drive(t, m, c, "queue", func() bool { return q.runs >= 1 })
	if v := screen(m); !strings.Contains(v, "queued 3 run live") {
		t.Errorf("queue:\n%s", v)
	}
	c = key(m, "w") // the lane's stream
	s := m.vr
	if s.v.Name != "tail" || s.lane == nil || s.lane.Job != "job42" {
		t.Fatalf("%+v", s)
	}
	drive(t, m, c, "the stream", func() bool { return len(s.lines) > 3 })
	if v := screen(m); !strings.Contains(v, "job job42 lane 1") || !strings.Contains(v, "following") {
		t.Errorf("tail:\n%s", v)
	}
	b, _ := os.ReadFile(filepath.Join(conf.Dir("hello"), "state", "tail.pid"))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	key(m, "2")
	key(m, "j") // lane 2 is idle: the lane view goes, and its run with it
	m.Update(nil)
	if m.vr != nil || s.ctx.Err() == nil || m.curTab().key != "log" {
		t.Fatalf("still running: %v, tab %s", m.vr != nil, m.curTab().key)
	}
	for i := 0; i < 40 && syscall.Kill(pid, 0) == nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if pid == 0 || syscall.Kill(pid, 0) == nil {
		t.Error("the stream outlived its tab")
	}
}

func TestViewErrorsInThePane(t *testing.T) {
	m := withViews(t)
	addView("broken", "#!/bin/sh\n# sat-view: every=5; format=json\necho '{\"blocks\": [ oops'\n")
	addView("fails", "#!/bin/sh\n# sat-view: every=5\necho 'half'; exit 2\n")
	addView("slow", "#!/bin/sh\n# sat-view: every=5; timeout=0.2\nsleep 5\n")
	addView("header", "#!/bin/sh\n# sat-view: mode=sometimes\n")
	m.refresh()
	for _, c := range []struct{ name, want string }{
		{"broken", "not a JSON document"}, {"fails", "exited 2"}, {"slow", "timed out after 0.2s"}, {"header", "bad header in sat/views/header: mode=sometimes"},
	} {
		var cmd tea.Cmd
		for m.curTab().key != "view:"+c.name {
			cmd = key(m, "w")
		}
		r := m.vr
		if c.name != "header" {
			drive(t, m, cmd, c.name, func() bool { return r.runs >= 1 })
		}
		if v := screen(m); !strings.Contains(v, c.want) {
			t.Errorf("%s: lacks %q:\n%s", c.name, c.want, v)
		}
	}
}

func TestTabRememberedPerProject(t *testing.T) {
	m := withViews(t)
	key(m, "w")
	key(m, "w") // hello: queue
	key(m, "1")
	key(m, "j") // other has no views: the lane log
	if m.curTab().key != "log" {
		t.Errorf("other: %s", m.curTab().key)
	}
	key(m, "!")
	key(m, "k") // back to hello: its view again
	if m.curTab().key != "view:queue" {
		t.Errorf("hello: %s", m.curTab().key)
	}
	key(m, "j")
	if m.curTab().key != "alerts" {
		t.Errorf("other: %s", m.curTab().key)
	}
	m.stopRun()
}

func writeAlerts(t *testing.T, as []agent.Alert) {
	p := filepath.Join(conf.Dir("hello"), "state", "agent.json")
	b, _ := os.ReadFile(p)
	var st agent.State
	json.Unmarshal(b, &st)
	st.Updated, st.Alerts = time.Now(), as
	b, _ = json.Marshal(st)
	os.WriteFile(p, b, 0o644)
}

func TestAlertHighlight(t *testing.T) {
	m := withViews(t)
	if strings.Contains(screen(m), "!1") {
		t.Fatal("an alert before any")
	}
	writeAlerts(t, []agent.Alert{{ID: "stall:1", Level: agent.Warn, Since: time.Now().Add(-time.Minute), Text: "lane 1 stalled: job42 has shown nothing new for 31m"}})
	m.refresh()
	v := screen(m)
	if !strings.Contains(v, "hello !1") || !strings.Contains(v, "1 alert") {
		t.Errorf("no badge:\n%s", v)
	}
	if !strings.Contains(m.View().Content, m.st.Warn.Bold(true).Render("!1")) {
		t.Error("the badge is not in the warning colour")
	}
	key(m, "!")
	if v := screen(m); !strings.Contains(v, "! lane 1 stalled") || !strings.Contains(v, "1m00s   ! lane") {
		t.Errorf("alerts tab:\n%s", v)
	}
	writeAlerts(t, nil) // cleared
	m.refresh()
	if strings.Contains(screen(m), "!1") {
		t.Error("the badge outlived the alert")
	}
	// A view's alert block highlights the project too, and heads the view.
	addView("watch", "#!/bin/sh\n# sat-view: every=5; format=json\necho '{\"blocks\": [{\"type\": \"text\", \"text\": \"body\"}, {\"type\": \"alert\", \"level\": \"error\", \"text\": \"queue is stuck\"}]}'\n")
	m.refresh()
	var c tea.Cmd
	for m.curTab().key != "view:watch" {
		c = key(m, "w")
	}
	r := m.vr
	drive(t, m, c, "watch", func() bool { return r.runs >= 1 })
	m.refresh()
	v = screen(m)
	if !strings.Contains(v, "hello !1") || !strings.Contains(v, "✗ queue is stuck") || strings.Index(v, "queue is stuck") > strings.Index(v, "body") {
		t.Errorf("view alert:\n%s", v)
	}
	if !strings.Contains(m.View().Content, m.st.Bad.Bold(true).Render("!1")) {
		t.Error("not in the error colour")
	}
}

func fakeProc(t *testing.T, m *model) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "stat"), []byte("cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1 1 1 1\ncpu1 1 1 1 1\n"), 0o644)
	os.WriteFile(filepath.Join(d, "meminfo"), []byte("MemTotal: 1000 kB\nMemAvailable: 250 kB\n"), 0o644)
	os.WriteFile(filepath.Join(d, "loadavg"), []byte("1.50 1.0 0.5 1/100 42\n"), 0o644)
	m.node = &nodeStats{proc: d}
	m.node.sample(time.Now())
	os.WriteFile(filepath.Join(d, "stat"), []byte("cpu  200 0 200 1400 0 0 0 0 0 0\ncpu0 1 1 1 1\ncpu1 1 1 1 1\n"), 0o644)
	m.node.sample(time.Now().Add(3 * time.Second))
}

func TestResponsive(t *testing.T) {
	m := withViews(t)
	fakeProc(t, m)
	if v, ok := last(m.node.cpu); !ok || v != 25 {
		t.Errorf("cpu %v", m.node.cpu)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	small := screen(m)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	big := screen(m)
	for _, w := range []string{"Node", "cpu", " 25%", "mem", " 75%", "load", "1.5 of 2", "floor"} {
		if strings.Contains(small, w) {
			t.Errorf("80x24 shows %q:\n%s", w, small)
		}
		if !strings.Contains(big, w) {
			t.Errorf("160x50 lacks %q:\n%s", w, big)
		}
	}
	for _, v := range []string{small, big} {
		if !strings.Contains(v, "3 Details") || !strings.Contains(v, "job42") {
			t.Errorf("lost a pane:\n%s", v)
		}
	}
	// Smaller still: Details goes, the lanes stay.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	tiny := screen(m)
	if strings.Contains(tiny, "3 Details") || !strings.Contains(tiny, "job42") || len(strings.Split(tiny, "\n")) != 14 {
		t.Errorf("80x14:\n%s", tiny)
	}
	key(m, "3")
	if m.focus == paneDetails {
		t.Error("focused a hidden pane")
	}
	for _, sz := range [][2]int{{80, 24}, {160, 50}, {60, 20}, {200, 60}} {
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		for i, l := range strings.Split(screen(m), "\n") {
			if w := len([]rune(l)); w > sz[0] {
				t.Errorf("%dx%d line %d is %d wide", sz[0], sz[1], i, w)
			}
		}
	}
}

func TestScrolling(t *testing.T) {
	m := withViews(t)
	var log []string
	for i := range 100 {
		log = append(log, fmt.Sprintf("line %03d", i))
	}
	os.WriteFile(filepath.Join(conf.Dir("hello"), "state", "slot1.log"), []byte(strings.Join(log, "\n")+"\n"), 0o644)
	v := screen(m)
	if !strings.Contains(v, "line 099") || !strings.Contains(v, "↑") {
		t.Errorf("the log's end, and that there is more:\n%s", v)
	}
	key(m, "4")
	for range 5 {
		key(m, "k")
	}
	if v := screen(m); !strings.Contains(v, "↓5") || strings.Contains(v, "line 099") {
		t.Errorf("scrolled up 5:\n%s", v)
	}
	m.Update(tea.MouseWheelMsg{X: m.geo.panes[paneMain].x + 3, Y: 5, Button: tea.MouseWheelDown})
	if !strings.Contains(screen(m), "↓4") {
		t.Error("the wheel did not scroll")
	}
	key(m, "G")
	if v := screen(m); strings.Contains(v, "↓") {
		t.Error("G did not go to the end")
	}
	// Details, when it doesn't fit.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 18})
	v = screen(m)
	if !m.lay.details || !strings.Contains(v, "↓") {
		t.Fatalf("details:\n%s", v)
	}
	key(m, "3")
	key(m, "j")
	if v := screen(m); m.dtop != 1 || !strings.Contains(v, "↑1") {
		t.Errorf("details scrolled %d:\n%s", m.dtop, v)
	}
	key(m, "g")
	if m.dtop != 0 {
		t.Error("g")
	}
	// Projects: many of them.
	for i := range 12 {
		base := conf.Dir(fmt.Sprintf("p%02d", i))
		os.MkdirAll(filepath.Join(base, "state"), 0o755)
		os.WriteFile(filepath.Join(base, "config"), []byte("HUB=h\n"), 0o644)
	}
	m.refresh()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if v := screen(m); !strings.Contains(v, "of 14 ↓") {
		t.Errorf("projects:\n%s", v)
	}
}
