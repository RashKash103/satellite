package tui

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/ui"
)

func press(k string) tea.KeyPressMsg {
	switch k {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

// a dialog with a text field, a choice, a check and three buttons, recording what was chosen.
func testDialog(chosen *string) *modal {
	pick := func(l string) func(*modal) (tea.Cmd, string) {
		return func(d *modal) (tea.Cmd, string) {
			if l == "OK" && d.fields[0].value() == "" {
				return nil, "a name is needed"
			}
			*chosen = l
			return nil, ""
		}
	}
	d := &modal{title: "Test", body: []string{"Some words about it."}, width: 40,
		fields: []*field{textField("Name", "", ""), choiceField("Kind", []string{"a", "b", "c"}, 0), checkField("Flag", "", false)},
		buttons: []button{{label: "OK", do: pick("OK")}, {label: "Other", keys: []string{"o"}, do: pick("Other")},
			{label: "Cancel", keys: []string{"n"}, cancel: true, do: pick("Cancel")}}}
	d.focusCmd()
	return d
}

func TestModalFocus(t *testing.T) {
	var chosen string
	d := testDialog(&chosen)
	// tab and shift+tab go round fields and buttons; ↑ ↓ too, with fields
	for i, want := range []int{1, 2, 3, 4, 5, 0} {
		if d.key(press("tab")); d.focus != want {
			t.Fatalf("tab %d: focus %d", i, d.focus)
		}
	}
	d.key(press("shift+tab"))
	if d.focus != 5 {
		t.Fatalf("shift+tab: %d", d.focus)
	}
	d.key(press("down"))
	d.key(press("down"))
	if d.focus != 1 {
		t.Fatalf("down: %d", d.focus)
	}
	// ← → change a choice, round
	d.key(press("right"))
	d.key(press("right"))
	d.key(press("right"))
	if d.fields[1].at != 0 {
		t.Errorf("choice: %d", d.fields[1].at)
	}
	d.key(press("left"))
	if d.fields[1].value() != "c" {
		t.Errorf("choice left: %s", d.fields[1].value())
	}
	// space toggles a check
	d.key(press("down"))
	d.key(press("space"))
	if !d.fields[2].on || d.fields[2].value() != "on" {
		t.Error("space did not toggle")
	}
	// ← → between the buttons, not past the ends
	d.focus = 3
	d.key(press("left"))
	if d.focus != 3 {
		t.Errorf("left of the first: %d", d.focus)
	}
	d.key(press("right"))
	d.key(press("right"))
	d.key(press("right"))
	if d.focus != 5 {
		t.Errorf("right: %d", d.focus)
	}
	// a disabled button is skipped
	d.buttons[1].off = func() bool { return true }
	d.key(press("left"))
	if d.focus != 3 {
		t.Errorf("left past a disabled button: %d", d.focus)
	}
	d.focus = 4
	d.fix()
	if d.focus != 5 {
		t.Errorf("fix: %d", d.focus)
	}
}

func TestModalKeys(t *testing.T) {
	var chosen string
	d := testDialog(&chosen)
	// typing goes to the text field, and its shortcut letters with it
	for _, r := range "on" {
		d.key(press(string(r)))
	}
	if d.fields[0].value() != "on" || chosen != "" {
		t.Fatalf("typed %q, chose %q", d.fields[0].value(), chosen)
	}
	// enter on a field: the next one
	if _, done := d.key(press("enter")); done || d.focus != 1 {
		t.Fatal("enter on a field")
	}
	// off the text field, shortcuts work
	if _, done := d.key(press("o")); !done || chosen != "Other" {
		t.Fatalf("o: %v %q", done, chosen)
	}
	d = testDialog(&chosen)
	d.focus = 3
	if _, done := d.key(press("enter")); done || d.err != "a name is needed" {
		t.Fatal("a refusal closed it")
	}
	if !strings.Contains(ui.Strip(strings.Join(d.view(tstyle(ui.Palette(true)), 80, 24), "\n")), "a name is needed") {
		t.Error("the refusal is not shown")
	}
	d.focus = 0
	d.focusCmd()
	d.key(press("x"))
	if d.err != "" {
		t.Error("typing did not clear the refusal")
	}
	d.focus = 3
	if _, done := d.key(press("space")); !done || chosen != "OK" {
		t.Fatal("space on a button")
	}
	for _, k := range []string{"esc", "ctrl+c", "n"} {
		d = testDialog(&chosen)
		d.focus = 1
		chosen = ""
		if _, done := d.key(press(k)); !done || chosen != "Cancel" {
			t.Errorf("%s: %v %q", k, done, chosen)
		}
	}
	// keys that mean nothing do nothing
	d = testDialog(&chosen)
	d.focus = 4
	chosen = ""
	for _, k := range []string{"z", "q", "?", "1"} {
		if _, done := d.key(press(k)); done || chosen != "" {
			t.Errorf("%s closed it", k)
		}
	}
	// close keys
	d.closeKeys = []string{"q"}
	if _, done := d.key(press("q")); !done || chosen != "Cancel" {
		t.Error("a close key")
	}
	// a button with no action just closes; a dialog with no cancel button closes on esc
	d = &modal{buttons: []button{{label: "Close"}}}
	if _, done := d.key(press("enter")); !done {
		t.Error("Close")
	}
	if _, done := (&modal{}).key(press("esc")); !done {
		t.Error("esc")
	}
}

func TestModalScroll(t *testing.T) {
	var body []string
	for i := range 40 {
		body = append(body, "line "+string(rune('A'+i%26)))
	}
	d := &modal{title: "Long", body: body, buttons: []button{{label: "Close", cancel: true}}}
	st := tstyle(ui.Palette(true))
	v := ui.Strip(strings.Join(d.view(st, 60, 20), "\n"))
	if !strings.Contains(v, "line A") || !strings.Contains(v, "↓") || !d.long {
		t.Fatalf("%s", v)
	}
	d.key(press("down"))
	d.key(press("j"))
	if d.top != 2 {
		t.Errorf("down, j: %d", d.top)
	}
	d.key(press("G"))
	v = ui.Strip(strings.Join(d.view(st, 60, 20), "\n"))
	if !strings.Contains(v, "line N") || strings.Contains(v, "↓") || !strings.Contains(v, "[ Close ]") {
		t.Errorf("end:\n%s", v)
	}
	d.key(press("g"))
	if d.top != 0 {
		t.Error("g")
	}
	var hs []string
	d.hints(func(k, w string) string { hs = append(hs, k); return k })
	if !slices.Contains(hs, "j/k") {
		t.Errorf("hints: %v", hs)
	}
}

// Whatever the terminal's size, the dialog fits in it, and the focused field and the buttons stay
// in view as long as there is room for them.
func TestModalSmall(t *testing.T) {
	st := tstyle(ui.Palette(true))
	var chosen string
	for _, sz := range [][2]int{{0, 0}, {1, 1}, {3, 2}, {8, 3}, {12, 5}, {20, 6}, {30, 8}, {40, 10}, {80, 24}, {200, 60}} {
		for _, f := range []int{0, 2, 4} {
			d := testDialog(&chosen)
			d.body = []string{strings.Repeat("words and more words ", 20)}
			d.focus = f
			ls := d.view(st, sz[0], sz[1])
			if len(ls) > max(sz[1], 1) {
				t.Errorf("%v: %d lines", sz, len(ls))
			}
			for _, l := range ls {
				if ui.Width(l) > max(sz[0], 1) {
					t.Errorf("%v: %d wide: %q", sz, ui.Width(l), ui.Strip(l))
				}
			}
			if sz[1] >= 5 && sz[0] >= 20 {
				v := ui.Strip(strings.Join(ls, "\n"))
				if !strings.Contains(v, "[ OK ]") {
					t.Errorf("%v focus %d: no buttons:\n%s", sz, f, v)
				}
				if f == 2 && !strings.Contains(v, "Flag") {
					t.Errorf("%v: the focused field is out of view:\n%s", sz, v)
				}
			}
		}
	}
}

// The whole live view with a dialog over it, at every size: no panic, nothing wider or taller than
// the terminal, the footer showing the dialog's keys.
func TestViewWithDialogSizes(t *testing.T) {
	m := node(t)
	for _, k := range []string{"x", "a", "D", "?", "v"} {
		for _, sz := range [][2]int{{1, 1}, {2, 2}, {10, 4}, {20, 6}, {40, 12}, {80, 24}, {160, 50}} {
			m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
			key(m, k)
			if m.modal == nil {
				t.Fatalf("%s: no dialog", k)
			}
			ls := strings.Split(screen(m), "\n")
			if len(ls) > sz[1] {
				t.Errorf("%s %v: %d lines", k, sz, len(ls))
			}
			for i, l := range ls {
				if ui.Width(l) > sz[0] {
					t.Errorf("%s %v: line %d is %d wide", k, sz, i, ui.Width(l))
				}
			}
			if sz[0] >= 80 && !strings.Contains(ls[len(ls)-1], "esc cancel") {
				t.Errorf("%s %v: footer %q", k, sz, ls[len(ls)-1])
			}
			key(m, "esc")
			if m.modal != nil {
				t.Fatalf("%s: esc kept it", k)
			}
		}
	}
}

func TestQuitAsksWhileAnActionRuns(t *testing.T) {
	m := node(t)
	m.job = &job{title: "check hello", running: true}
	if c := key(m, "q"); c != nil || m.modal == nil || m.modal.kind != "quit" {
		t.Fatal("q quit while an action ran")
	}
	if key(m, "enter"); m.modal != nil {
		t.Fatal("enter (on Stay) did not close it")
	}
	key(m, "q")
	if c := key(m, "y"); c == nil {
		t.Fatal("y did not quit")
	}
	m.job.running = false
	if c := key(m, "q"); c == nil || m.modal != nil {
		t.Fatal("q with nothing running")
	}
}

func TestScheduleDialog(t *testing.T) {
	m := node(t)
	key(m, "D")
	d := m.modal
	if d == nil || d.kind != "schedule" || d.focus != 1 {
		t.Fatalf("D: %+v", d)
	}
	v := screen(m)
	for _, w := range []string{"Drain schedule · hello", "start at", "done by", "Time", "Repeat", "once", "daily", "Date", "[ Save ]", "[ Remove schedule ]", "taken as 30m"} {
		if !strings.Contains(v, w) {
			t.Errorf("lacks %q:\n%s", w, v)
		}
	}
	// no schedule: nothing to remove
	if !d.buttons[1].disabled() {
		t.Error("Remove offered with no schedule")
	}
	// inline validation
	typ(m, "25:00")
	if p, ok := d.preview(d); ok || !strings.Contains(p, "HH:MM") {
		t.Errorf("preview of a bad time: %q %v", p, ok)
	}
	if !strings.Contains(screen(m), "a time is HH:MM") {
		t.Error("the problem is not shown")
	}
	d.focus = 4 // Save
	if key(m, "enter"); m.modal == nil || d.err == "" || m.last.mode != "" {
		t.Fatal("saved a bad time")
	}
	// done by, daily: no date field
	d.focus = 1
	d.focusCmd()
	d.fields[1].in.SetValue("")
	typ(m, "23:59")
	d.focus = 0
	key(m, "right")
	d.focus = 2
	key(m, "right")
	if !d.fields[3].hidden() {
		t.Error("a date for a daily schedule")
	}
	if p, ok := d.preview(d); !ok || !strings.Contains(p, "done by 23:59") || !strings.Contains(p, "daily") {
		t.Errorf("preview: %q", p)
	}
	key(m, "tab") // past the hidden date: Save
	if b := d.curButton(); b == nil || b.label != "Save" {
		t.Fatalf("tab from Repeat: %d", d.focus)
	}
	if key(m, "enter"); m.modal != nil || !slices.Equal(m.last.args, []string{"hello", "schedule", "end", "23:59", "daily"}) {
		t.Fatalf("save: %v", m.last)
	}
	// once with no date: the command decides the next one
	m = node(t)
	key(m, "D")
	typ(m, "07:00")
	m.modal.focus = 4
	if key(m, "enter"); !slices.Equal(m.last.args, []string{"hello", "schedule", "start", "07:00"}) {
		t.Fatalf("save once: %v", m.last)
	}
	// a schedule there: the dialog starts from it, shows it, and can remove it
	m = node(t)
	p := m.cur().p
	day := time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	s, err := agent.NewSchedule(agent.AnchorStart, "06:30", false, day, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	agent.WriteSchedule(p, s)
	m.refresh()
	v = screen(m)
	if !strings.Contains(v, "drain 06:30") {
		t.Errorf("the project row lacks the schedule:\n%s", v)
	}
	key(m, "D")
	d = m.modal
	if d.fields[1].value() != "06:30" || d.fields[3].value() != day || d.fields[0].at != 0 || d.buttons[1].disabled() {
		t.Fatalf("from the schedule: %q %q", d.fields[1].value(), d.fields[3].value())
	}
	if !strings.Contains(screen(m), "Now: start draining at 06:30 on "+day) {
		t.Error("the current schedule is not shown")
	}
	d.focus = 5
	if key(m, "enter"); !slices.Equal(m.last.args, []string{"hello", "schedule", "off"}) {
		t.Fatalf("remove: %v", m.last)
	}
	// a click on a choice changes it
	m = node(t)
	key(m, "D")
	screen(m)
	for _, h := range m.modal.hits {
		if h.item == 0 {
			m.Update(tea.MouseClickMsg{X: h.r.x + 2, Y: h.r.y})
			break
		}
	}
	if m.modal.fields[0].at != 1 {
		t.Error("a click on the anchor did not change it")
	}
	os.Remove(agent.SchedulePath(p))
}
