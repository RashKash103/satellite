package tui

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/RashKash103/satellite/internal/ui"
)

// A modal is a dialog drawn over the live view: a bordered box in the middle of the screen with a
// title, a body, optional fields and a row of buttons. While one is open it takes every key:
// tab / shift+tab (and ↑ ↓) move between the fields and the buttons, ← → between the buttons (or
// through a choice), enter or space chooses the focused button, esc cancels, and a button's own
// shortcut (y, n ...) chooses it while no text field has the focus. The mouse clicks buttons and
// fields, and its wheel scrolls a long body.

type fieldKind int

const (
	fieldText   fieldKind = iota
	fieldChoice           // one of opts, ← → to change
	fieldCheck            // on or off, space to change
)

type field struct {
	label string
	kind  fieldKind
	in    textinput.Model
	opts  []string
	at    int  // the option chosen
	on    bool // a check's state
	note  string
	hide  func() bool // the field is left out while this says so
}

func textField(label, placeholder, value string) *field {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.SetValue(value)
	in.SetWidth(24)
	return &field{label: label, kind: fieldText, in: in}
}

func choiceField(label string, opts []string, at int) *field {
	return &field{label: label, kind: fieldChoice, opts: opts, at: min(max(at, 0), len(opts)-1)}
}

func checkField(label, note string, on bool) *field {
	return &field{label: label, kind: fieldCheck, on: on, note: note}
}

// value: a text field's trimmed text, a choice's option, a check's "on" or "".
func (f *field) value() string {
	switch f.kind {
	case fieldText:
		return strings.TrimSpace(f.in.Value())
	case fieldChoice:
		return f.opts[f.at]
	}
	if f.on {
		return "on"
	}
	return ""
}

func (f *field) hidden() bool { return f.hide != nil && f.hide() }

type button struct {
	label  string
	keys   []string // shortcuts, while no text field has the focus
	danger bool
	cancel bool // esc chooses it
	// do runs when the button is chosen; nil just closes. A reason ("" none) keeps the dialog
	// open and shows it.
	do  func(d *modal) (tea.Cmd, string)
	off func() bool // disabled while this says so
}

func (b *button) disabled() bool { return b.off != nil && b.off() }

type hit struct {
	r    rect
	item int // a field (index) or a button (len(fields) + index)
}

type modal struct {
	kind      string // what it is for (the tests ask)
	title     string
	body      []string
	fields    []*field
	buttons   []button
	focus     int // a field (index) or a button (len(fields) + index)
	err       string
	width     int                                      // the inner width it would like
	preview   func(d *modal) (string, bool)            // a line under the fields, from their values (false: a problem)
	closeKeys []string                                 // more keys that cancel (help: ? q)
	top       int                                      // the body's first line shown
	bodyH     int                                      // the body lines shown in the last drawing
	long      bool                                     // the body did not fit in the last drawing
	at        rect                                     // the box on screen, for the mouse
	hits      []hit                                    // the fields and buttons on screen
	onKey     func(d *modal, k string) bool            // a hook before the usual keys (true: handled)
	onChange  func(d *modal)                           // a field's value changed
	render    func(d *modal, t styles, w int) []string // the body, drawn for width w (in place of body)
}

// items: what can have the focus, in order: the shown fields, then the buttons that are enabled.
func (d *modal) items() []int {
	var r []int
	for i, f := range d.fields {
		if !f.hidden() {
			r = append(r, i)
		}
	}
	for i := range d.buttons {
		if !d.buttons[i].disabled() {
			r = append(r, len(d.fields)+i)
		}
	}
	return r
}

func (d *modal) curField() *field {
	if d.focus < len(d.fields) {
		return d.fields[d.focus]
	}
	return nil
}

func (d *modal) curButton() *button {
	if i := d.focus - len(d.fields); i >= 0 && i < len(d.buttons) {
		return &d.buttons[i]
	}
	return nil
}

// fix: the focus on something that can have it (a field just hidden, a button just disabled).
func (d *modal) fix() {
	it := d.items()
	if len(it) == 0 || slices.Contains(it, d.focus) {
		return
	}
	for _, i := range it {
		if i > d.focus {
			d.focus = i
			return
		}
	}
	d.focus = it[len(it)-1]
}

// move the focus step items on (or back), round.
func (d *modal) move(step int) tea.Cmd {
	d.fix()
	it := d.items()
	if len(it) == 0 {
		return nil
	}
	p := max(slices.Index(it, d.focus), 0)
	d.focus = it[(p+step+len(it)*4)%len(it)]
	return d.focusCmd()
}

// moveButton: the next (or previous) enabled button, not past the ends.
func (d *modal) moveButton(step int) {
	for i := d.focus - len(d.fields) + step; i >= 0 && i < len(d.buttons); i += step {
		if !d.buttons[i].disabled() {
			d.focus = len(d.fields) + i
			return
		}
	}
}

// focusCmd focuses the text field with the focus, and blurs the others.
func (d *modal) focusCmd() tea.Cmd {
	var c tea.Cmd
	for i, f := range d.fields {
		if f.kind != fieldText {
			continue
		}
		if i == d.focus {
			c = f.in.Focus()
		} else {
			f.in.Blur()
		}
	}
	return c
}

// focusButton: the focus on the button labelled l.
func (d *modal) focusButton(l string) {
	for i := range d.buttons {
		if d.buttons[i].label == l {
			d.focus = len(d.fields) + i
		}
	}
	d.focusCmd()
}

func (d *modal) changed() {
	d.err = ""
	if d.onChange != nil {
		d.onChange(d)
	}
}

// choose runs button b. done: the dialog closes (and cmd is what b started).
func (d *modal) choose(b *button) (tea.Cmd, bool) {
	if b.disabled() {
		return nil, false
	}
	if b.do == nil {
		return nil, true
	}
	c, why := b.do(d)
	if why != "" {
		d.err = why
		return nil, false
	}
	return c, true
}

func (d *modal) cancel() (tea.Cmd, bool) {
	for i := range d.buttons {
		if d.buttons[i].cancel {
			return d.choose(&d.buttons[i])
		}
	}
	return nil, true
}

// scroll the body by n lines.
func (d *modal) scroll(n int) { d.top = max(d.top+n, 0) }

// key handles a key press. done: the dialog closes; cmd is what was chosen.
func (d *modal) key(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	k := msg.String()
	d.fix()
	if d.onKey != nil && d.onKey(d, k) {
		return nil, false
	}
	f, b := d.curField(), d.curButton()
	text := f != nil && f.kind == fieldText
	page := max(d.bodyH-1, 1)
	switch k {
	case "esc", "ctrl+c":
		if k == "ctrl+c" {
			for i := range d.buttons {
				if slices.Contains(d.buttons[i].keys, k) {
					return d.choose(&d.buttons[i])
				}
			}
		}
		return d.cancel()
	case "tab":
		return d.move(1), false
	case "shift+tab":
		return d.move(-1), false
	case "down", "up":
		step := 1
		if k == "up" {
			step = -1
		}
		if len(d.fields) == 0 { // a dialog with no fields: the arrows scroll its body
			d.scroll(step)
			return nil, false
		}
		return d.move(step), false
	case "pgdown", "pgup":
		if k == "pgup" {
			page = -page
		}
		d.scroll(page)
		return nil, false
	case "left", "right":
		step := 1
		if k == "left" {
			step = -1
		}
		switch {
		case b != nil:
			d.moveButton(step)
			return nil, false
		case f != nil && f.kind == fieldChoice:
			f.at = (f.at + step + len(f.opts)) % len(f.opts)
			d.changed()
			return nil, false
		case f != nil && f.kind == fieldCheck:
			f.on = !f.on
			d.changed()
			return nil, false
		}
	case "enter":
		if b != nil {
			return d.choose(b)
		}
		return d.move(1), false
	case "space":
		switch {
		case b != nil:
			return d.choose(b)
		case f != nil && f.kind == fieldChoice:
			f.at = (f.at + 1) % len(f.opts)
			d.changed()
			return nil, false
		case f != nil && f.kind == fieldCheck:
			f.on = !f.on
			d.changed()
			return nil, false
		}
	}
	if !text {
		switch k {
		case "j":
			d.scroll(1)
			return nil, false
		case "k":
			d.scroll(-1)
			return nil, false
		case "g", "home":
			d.top = 0
			return nil, false
		case "G", "end":
			d.top = 1 << 30
			return nil, false
		}
		if slices.Contains(d.closeKeys, k) {
			return d.cancel()
		}
		for i := range d.buttons {
			if slices.Contains(d.buttons[i].keys, k) {
				return d.choose(&d.buttons[i])
			}
		}
		return nil, false
	}
	var c tea.Cmd
	old := f.in.Value()
	f.in, c = f.in.Update(msg)
	if f.in.Value() != old {
		d.changed()
	}
	return c, false
}

// update passes other messages (a text field's cursor blink) to the focused text field.
func (d *modal) update(msg tea.Msg) tea.Cmd {
	if f := d.curField(); f != nil && f.kind == fieldText {
		var c tea.Cmd
		f.in, c = f.in.Update(msg)
		return c
	}
	return nil
}

// click: a press at x, y on the screen. done and cmd as for key.
func (d *modal) click(x, y int) (tea.Cmd, bool) {
	for _, h := range d.hits {
		if !h.r.has(x, y) {
			continue
		}
		d.focus = h.item
		if b := d.curButton(); b != nil {
			return d.choose(b)
		}
		if f := d.curField(); f != nil {
			switch f.kind {
			case fieldChoice:
				f.at = (f.at + 1) % len(f.opts)
				d.changed()
			case fieldCheck:
				f.on = !f.on
				d.changed()
			}
		}
		return d.focusCmd(), false
	}
	return nil, false
}

// line: one line of the box's content, and what the mouse finds on it.
type mline struct {
	s     string
	item  int      // -1: nothing
	spans [][3]int // buttons: x from, x to, item
	keep  bool     // never dropped for room (the buttons)
}

// view draws the dialog in at most sw x sh cells. It is at least as small as the screen, whatever
// the screen's size.
func (d *modal) view(t styles, sw, sh int) []string {
	sw, sh = max(sw, 1), max(sh, 1)
	maxW := sw
	if sw >= 24 {
		maxW = sw - 4
	}
	maxH := sh
	if sh >= 12 {
		maxH = sh - 2
	}
	w := min(max(d.width, 30)+4, maxW)
	w = max(w, min(6, sw))
	cw := max(w-4, 1) // the content: inside the border and a space each side
	d.fix()

	// the fields, the preview, the error and the buttons: what must show
	var fixed []mline
	lw := 0
	for _, f := range d.fields {
		lw = max(lw, ui.Width(f.label))
	}
	lw = min(lw+2, max(cw/2, 1))
	for i, f := range d.fields {
		if f.hidden() {
			continue
		}
		mark := "  "
		if d.focus == i {
			mark = t.accent.Render("› ")
		}
		var v string
		vw := max(cw-lw-2, 1)
		switch f.kind {
		case fieldText:
			f.in.SetWidth(max(vw-1, 1))
			v = f.in.View()
		case fieldChoice:
			var os []string
			for j, o := range f.opts {
				switch {
				case j == f.at && d.focus == i:
					os = append(os, t.sel.Render(" "+o+" "))
				case j == f.at:
					os = append(os, t.selDim.Render("["+o+"]"))
				default:
					os = append(os, t.muted.Render(" "+o+" "))
				}
			}
			v = strings.Join(os, " ")
		case fieldCheck:
			v = "[ ]"
			if f.on {
				v = "[x]"
			}
			if d.focus == i {
				v = t.sel.Render(v)
			}
		}
		if f.note != "" {
			v += t.muted.Render(" " + f.note)
		}
		fixed = append(fixed, mline{s: mark + ui.Pad(ui.Truncate(f.label, lw-1), lw) + v, item: i})
	}
	if d.preview != nil {
		if p, ok := d.preview(d); p != "" {
			st := t.muted
			if !ok {
				st = t.warn
			}
			for _, l := range wrap(p, cw) {
				fixed = append(fixed, mline{s: st.Render(l), item: -1})
			}
		}
	}
	if d.err != "" {
		for _, l := range wrap(d.err, cw) {
			fixed = append(fixed, mline{s: t.bad.Render(l), item: -1})
		}
	}
	var bl mline
	bl.item, bl.keep = -1, true
	x := 0
	for i := range d.buttons {
		b := &d.buttons[i]
		lab := "[ " + b.label + " ]"
		var s string
		switch {
		case b.disabled():
			s = t.muted.Faint(true).Render(lab)
		case d.focus == len(d.fields)+i:
			st := t.sel
			if b.danger {
				st = t.bad.Reverse(true).Bold(true)
			}
			s = st.Render(lab)
		case b.danger:
			s = t.bad.Render(lab)
		default:
			s = t.accent.Render(lab)
		}
		if i > 0 {
			bl.s += "  "
			x += 2
		}
		bl.spans = append(bl.spans, [3]int{x, x + ui.Width(lab), len(d.fields) + i})
		bl.s += s
		x += ui.Width(lab)
	}
	if bw := ui.Width(bl.s); bw < cw { // centred
		off := (cw - bw) / 2
		bl.s = strings.Repeat(" ", off) + bl.s
		for i := range bl.spans {
			bl.spans[i][0] += off
			bl.spans[i][1] += off
		}
	}

	// the body takes the room that is left, scrolled when it doesn't fit
	var body []string
	if d.render != nil {
		body = d.render(d, t, cw)
	} else {
		for _, l := range d.body {
			body = append(body, wrap(l, cw)...)
		}
	}
	room := max(maxH-2, 0) // inside the border
	need := len(fixed) + 1
	gap1 := len(body) > 0 && len(fixed) > 0
	gap2 := true
	bh := room - need
	if gap1 {
		bh--
	}
	if gap2 && len(d.buttons) > 0 {
		bh--
	}
	if bh < len(body) && bh < 3 { // short of room: no blank lines
		gap1, gap2 = false, false
		bh = room - need
	}
	bh = max(min(bh, len(body)), 0)
	d.top = min(d.top, max(len(body)-bh, 0))
	d.bodyH, d.long = bh, len(body) > bh
	var ls []mline
	for _, l := range body[d.top : d.top+bh] {
		ls = append(ls, mline{s: l, item: -1})
	}
	if gap1 {
		ls = append(ls, mline{item: -1})
	}
	ls = append(ls, fixed...)
	if len(d.buttons) > 0 {
		if gap2 {
			ls = append(ls, mline{item: -1})
		}
		ls = append(ls, bl)
	}
	if len(ls) > room { // still too many: keep the focused line and the buttons in view
		foc := 0
		for i, l := range ls {
			if l.item == d.focus {
				foc = i
			}
		}
		start := max(len(ls)-room, 0)
		if foc < start {
			start = foc
		}
		end := min(start+room, len(ls))
		if end < len(ls) && room > 1 && ls[len(ls)-1].keep {
			ls = append(append([]mline(nil), ls[start:start+room-1]...), ls[len(ls)-1])
		} else {
			ls = ls[start:end]
		}
	}

	// the box
	b := t.borderOn
	in := w - 2
	title := ""
	if in >= 6 {
		title = ui.Truncate(t.head.Render(d.title), in-4)
	}
	head := "─ "
	if title == "" {
		head = "─"
	}
	top := b.Render("╭"+head) + title + b.Render(" "+strings.Repeat("─", max(in-ui.Width(head)-ui.Width(title)-1, 0)))
	if title == "" {
		top = b.Render("╭" + strings.Repeat("─", max(in, 0)))
	}
	top = ansi.Truncate(top, w-1, "") + b.Render("╮")
	out := []string{top}
	d.hits = d.hits[:0]
	for i, l := range ls {
		s := ui.Pad(ui.Truncate(" "+l.s, in), in)
		out = append(out, b.Render("│")+s+b.Render("│"))
		y := d.at.y + 1 + i
		if l.item >= 0 {
			d.hits = append(d.hits, hit{rect{d.at.x + 1, y, in, 1}, l.item})
		}
		for _, sp := range l.spans {
			d.hits = append(d.hits, hit{rect{d.at.x + 2 + sp[0], y, sp[1] - sp[0], 1}, sp[2]})
		}
	}
	note := ""
	if len(body) > bh {
		note = more(t, "", d.top, bh, len(body))
	}
	if note != "" && in > ui.Width(note)+4 {
		note = " " + note + " "
	} else {
		note = ""
	}
	out = append(out, b.Render("╰"+strings.Repeat("─", max(in-ui.Width(note)-1, 0)))+t.muted.Render(note)+b.Render(strings.Repeat("─", max(min(1, in), 0))+"╯"))
	if len(out) > sh {
		out = out[:sh]
	}
	for i := range out {
		out[i] = ansi.Truncate(out[i], sw, "")
	}
	return out
}

// place: where the dialog goes, centred in a screen of sw x sh, given how big it drew.
func (d *modal) place(sw, sh, w, h int) {
	old := d.at
	d.at = rect{max((sw-w)/2, 0), max((sh-h)/2, 0), w, h}
	dx, dy := d.at.x-old.x, d.at.y-old.y
	for i := range d.hits {
		d.hits[i].r.x += dx
		d.hits[i].r.y += dy
	}
}

// wrap s to w columns (styled text kept as it is), breaking long words.
func wrap(s string, w int) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(ansi.Wrap(s, max(w, 1), ""), "\n")
}

// overlay draws box over base (dimmed) at x, y.
func overlay(t styles, base []string, box []string, x, y int) []string {
	out := make([]string, len(base))
	for i, l := range base {
		out[i] = t.muted.Render(ui.Strip(l))
	}
	for i, bl := range box {
		r := y + i
		if r < 0 || r >= len(out) {
			continue
		}
		l := ui.Pad(ui.Strip(base[r]), x+ui.Width(bl))
		left := ansi.Truncate(l, x, "")
		right := ansi.TruncateLeft(l, x+ui.Width(bl), "")
		out[r] = t.muted.Render(left) + bl + t.muted.Render(right)
	}
	return out
}

// hints: the dialog's keys, for the footer.
func (d *modal) hints(k func(key, what string) string) []string {
	var r []string
	if len(d.items()) > 1 {
		r = append(r, k("tab", "move"))
	}
	if len(d.buttons) > 1 {
		r = append(r, k("← →", "buttons"))
	}
	r = append(r, k("enter", "choose"))
	for _, b := range d.buttons {
		if len(b.keys) > 0 && !b.disabled() {
			r = append(r, k(b.keys[0], strings.ToLower(b.label)))
		}
	}
	if d.long {
		r = append(r, k("j/k", "scroll"))
	}
	return append(r, k("esc", "cancel"))
}
