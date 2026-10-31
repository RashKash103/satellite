// Package ui is how sat prints for people: one palette, marks, headings, aligned rows and units,
// shared by the commands and the live view.
//
// Styling is only for a terminal that supports it. Output to a pipe or a file, NO_COLOR, TERM=dumb
// or CLICOLOR=0 get plain text with no escape codes, laid out as sat always printed it, so what
// scripts read stays the same. CLICOLOR_FORCE or FORCE_COLOR turn styling on anyway. Colours
// follow the terminal's background (light or dark) and are reduced to what it can show.
package ui

import (
	"fmt"
	"image/color"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// T prints to one writer, styled or plain.
type T struct {
	w     io.Writer // through a colorprofile writer when styled
	raw   io.Writer
	color bool // styled: colour, marks, the richer layouts
	live  bool // a terminal that can redraw a line (spinners)
	width int
	S     Styles
}

// Env decides styling for a terminal (or not) and an environment, as no-color.org and
// bixense.com/clicolors describe: NO_COLOR wins, then CLICOLOR_FORCE / FORCE_COLOR, then a
// terminal that isn't dumb, unless CLICOLOR=0.
func Env(isTerm bool, getenv func(string) string) bool {
	set := func(k string) bool { v := getenv(k); return v != "" && v != "0" && v != "false" }
	switch {
	case getenv("NO_COLOR") != "":
		return false
	case set("CLICOLOR_FORCE"), set("FORCE_COLOR"):
		return true
	case !isTerm, getenv("TERM") == "dumb", getenv("TERM") == "" && getenv("COLORTERM") == "":
		return false
	case getenv("CLICOLOR") == "0":
		return false
	}
	return true
}

func isTerm(w io.Writer) (*os.File, bool) {
	f, ok := w.(*os.File)
	return f, ok && term.IsTerminal(int(f.Fd()))
}

// New: a T for w, styled if w is a terminal that supports it.
func New(w io.Writer) *T {
	f, tt := isTerm(w)
	if !Env(tt, os.Getenv) {
		t := Plain(w)
		t.live = tt && os.Getenv("TERM") != "dumb"
		return t
	}
	p := colorprofile.Detect(w, os.Environ())
	if p < colorprofile.ANSI {
		p = colorprofile.ANSI // forced on, or NO_COLOR-free but unknown TERM
	}
	width := 0
	if tt {
		if c, _, err := term.GetSize(int(f.Fd())); err == nil {
			width = c
		}
	}
	return styled(w, p, Dark(), width, tt)
}

// Plain: never styled.
func Plain(w io.Writer) *T { return &T{w: w, raw: w, S: Palette(true)} }

// Styled: always styled, for tests and previews (ANSI 256 colours).
func Styled(w io.Writer, dark bool, width int) *T {
	return styled(w, colorprofile.ANSI256, dark, width, false)
}

func styled(w io.Writer, p colorprofile.Profile, dark bool, width int, live bool) *T {
	cw := &colorprofile.Writer{Forward: w, Profile: p}
	return &T{w: cw, raw: w, color: true, live: live, width: width, S: Palette(dark)}
}

// For: w itself when it is a T, else New(w). Packages that take an io.Writer use it so callers
// can hand them a styled or plain T.
func For(w io.Writer) *T {
	if t, ok := w.(*T); ok {
		return t
	}
	return New(w)
}

var (
	mu             sync.Mutex
	stdout, stderr *T
)

// Out and Err: os.Stdout and os.Stderr (made again if those are swapped).
func Out() *T {
	mu.Lock()
	defer mu.Unlock()
	if stdout == nil || stdout.raw != io.Writer(os.Stdout) {
		stdout = New(os.Stdout)
	}
	return stdout
}

func Err() *T {
	mu.Lock()
	defer mu.Unlock()
	if stderr == nil || stderr.raw != io.Writer(os.Stderr) {
		stderr = New(os.Stderr)
	}
	return stderr
}

func (t *T) Write(p []byte) (int, error) { return t.w.Write(p) }

// Raw: the underlying writer (for a child process's output).
func (t *T) Raw() io.Writer { return t.raw }

func (t *T) Color() bool { return t.color }
func (t *T) Live() bool  { return t.live }

// Width: the terminal's columns, 0 when unknown.
func (t *T) Width() int { return t.width }

func (t *T) Printf(f string, v ...any) { fmt.Fprintf(t, f, v...) }
func (t *T) Println(v ...any)          { fmt.Fprintln(t, v...) }
func (t *T) Print(v ...any)            { fmt.Fprint(t, v...) }

var darkOnce sync.Once
var dark = true

// Dark: whether the terminal's background is dark: COLORFGBG when set, else the terminal is
// asked (only when stdin and stdout are both terminals); dark when unknown.
func Dark() bool {
	darkOnce.Do(func() {
		if v := os.Getenv("COLORFGBG"); v != "" {
			p := strings.Split(v, ";")
			if n, err := strconv.Atoi(p[len(p)-1]); err == nil {
				dark = n < 7 || n == 8
				return
			}
		}
		if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
			dark = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
		}
	})
	return dark
}

// Styles: the palette, for a dark or a light background.
type Styles struct {
	OK, Warn, Bad, Muted, Accent, Bold, Heading, Faint lipgloss.Style
	Bar                                                color.Color
}

func Palette(dark bool) Styles {
	c := lipgloss.LightDark(dark)
	fg := func(light, dk string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(c(lipgloss.Color(light), lipgloss.Color(dk)))
	}
	return Styles{
		OK:      fg("#00875F", "#5FD787"),
		Warn:    fg("#AF5F00", "#FFAF00"),
		Bad:     fg("#D70000", "#FF5F5F"),
		Muted:   fg("#6C6C6C", "#8A8A8A"),
		Accent:  fg("#005FD7", "#5FAFFF"),
		Bold:    lipgloss.NewStyle().Bold(true),
		Heading: fg("#005FD7", "#5FAFFF").Bold(true),
		Faint:   lipgloss.NewStyle().Faint(true),
		Bar:     c(lipgloss.Color("#005FD7"), lipgloss.Color("#5FAFFF")),
	}
}

func (t *T) r(s lipgloss.Style, v string) string {
	if !t.color || v == "" {
		return v
	}
	return s.Render(v)
}

func (t *T) OK(s string) string      { return t.r(t.S.OK, s) }
func (t *T) Warn(s string) string    { return t.r(t.S.Warn, s) }
func (t *T) Bad(s string) string     { return t.r(t.S.Bad, s) }
func (t *T) Muted(s string) string   { return t.r(t.S.Muted, s) }
func (t *T) Accent(s string) string  { return t.r(t.S.Accent, s) }
func (t *T) Bold(s string) string    { return t.r(t.S.Bold, s) }
func (t *T) Heading(s string) string { return t.r(t.S.Heading, s) }

// Marks for a result line; empty when plain, so plain lines read as they always did.
const (
	MarkOK   = "✓"
	MarkBad  = "✗"
	MarkWarn = "!"
	MarkInfo = "•"
)

// Mark: "✓ " in green (and so on) when styled, "" when plain.
func (t *T) Mark(kind string) string {
	if !t.color {
		return ""
	}
	switch kind {
	case MarkOK:
		return t.OK(kind) + " "
	case MarkBad:
		return t.Bad(kind) + " "
	case MarkWarn:
		return t.Warn(kind) + " "
	}
	return t.Muted(kind) + " "
}

// Done, Failed, Warned, Info: one result line, marked when styled.
func (t *T) Done(f string, v ...any)   { t.Println(t.Mark(MarkOK) + fmt.Sprintf(f, v...)) }
func (t *T) Failed(f string, v ...any) { t.Println(t.Mark(MarkBad) + fmt.Sprintf(f, v...)) }
func (t *T) Warned(f string, v ...any) { t.Println(t.Mark(MarkWarn) + fmt.Sprintf(f, v...)) }
func (t *T) Info(f string, v ...any)   { t.Println(t.Mark(MarkInfo) + fmt.Sprintf(f, v...)) }

// Badge: a state word coloured by what it means (running, stopped, failed, draining, ...).
func (t *T) Badge(s string) string {
	switch s {
	case "running", "active", "reachable", "enabled", "on", "ok", "passed", "verified":
		return t.OK(s)
	case "failed", "unreachable", "error":
		return t.Bad(s)
	case "draining", "activating", "deactivating", "holding", "not claiming", "not checked", "waiting":
		return t.Warn(s)
	}
	return t.Muted(s)
}

// Dot: a coloured ● for a state (styled only).
func (t *T) Dot(s string) string {
	if !t.color {
		return ""
	}
	switch s {
	case "active", "running":
		return t.OK("●") + " "
	case "failed":
		return t.Bad("●") + " "
	case "activating", "deactivating", "draining":
		return t.Warn("●") + " "
	}
	return t.Muted("○") + " "
}

// Width of s on screen, escape codes left out.
func Width(s string) int { return ansi.StringWidth(s) }

// Strip removes escape codes.
func Strip(s string) string { return ansi.Strip(s) }

// Pad s with spaces to n columns.
func Pad(s string, n int) string {
	if w := Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// Truncate s to n columns with an ellipsis (n <= 0: unchanged).
func Truncate(s string, n int) string {
	if n <= 0 || Width(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}

// Columns aligns rows of cells (already styled or not) with two spaces between columns, each line
// prefixed by indent. A row's last cell is never padded.
func Columns(indent string, rows [][]string) string {
	var ws []int
	for _, r := range rows {
		for i, c := range r {
			if i >= len(ws) {
				ws = append(ws, 0)
			}
			if i < len(r)-1 {
				ws[i] = max(ws[i], Width(c))
			}
		}
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(indent)
		for i, c := range r {
			if i < len(r)-1 {
				b.WriteString(Pad(c, ws[i]) + "  ")
			} else {
				b.WriteString(c)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Table: Columns with a header row (muted and bold when styled).
func (t *T) Table(indent string, head []string, rows [][]string) string {
	h := make([]string, len(head))
	for i, c := range head {
		h[i] = t.r(t.S.Muted.Bold(true), c)
	}
	return Columns(indent, append([][]string{h}, rows...))
}

// Rule: a muted heading line ── title ─────, as wide as the terminal (at most 72), styled only.
func (t *T) Rule(title string) string {
	if !t.color {
		return title
	}
	w := t.width
	if w <= 0 || w > 72 {
		w = 72
	}
	s := "── " + title + " "
	return t.Muted(s + strings.Repeat("─", max(w-Width(s), 2)))
}

// Bytes: 512 B, 3.4 GB.
func Bytes(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

// Dur: 45s, 3m12s, 2h05m, 3d04h.
func Dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 72*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}

// Ago: "12s ago", "never" for the zero time.
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return Dur(now.Sub(t)) + " ago"
}

// Plural: "1 step", "3 steps".
func Plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Bar: a progress bar of n cells for pct (0-100), styled only ("" when plain).
func (t *T) Bar(pct, n int) string {
	if !t.color || pct < 0 {
		return ""
	}
	f := min(max(pct, 0), 100) * n / 100
	return lipgloss.NewStyle().Foreground(t.S.Bar).Render(strings.Repeat("█", f)) + t.Muted(strings.Repeat("░", n-f))
}

// Prompt: "q [y/N] ", with a "?" mark when styled.
func Prompt(t *T, q string) string {
	if !t.color {
		return q + " [y/N] "
	}
	return t.Accent("?") + " " + t.Bold(q) + " " + t.Muted("[y/N]") + " "
}

// Spin runs f with a spinner and msg on the line, when t is a live terminal; it clears the line
// after. f must not print to t.
func Spin(t *T, msg string, f func()) {
	if !t.live || !t.color {
		f()
		return
	}
	done := make(chan struct{})
	go func() {
		fr := spinner.MiniDot.Frames
		tk := time.NewTicker(spinner.MiniDot.FPS)
		defer tk.Stop()
		for i := 0; ; i++ {
			fmt.Fprintf(t.w, "\r%s %s\x1b[K", t.Accent(fr[i%len(fr)]), t.Muted(msg))
			select {
			case <-done:
				fmt.Fprint(t.w, "\r\x1b[K")
				close(done)
				return
			case <-tk.C:
			}
		}
	}()
	f()
	done <- struct{}{}
	<-done
}
