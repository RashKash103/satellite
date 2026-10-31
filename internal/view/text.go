package view

import (
	"io"
	"strings"
	"sync"
)

// Clean makes one line of a view's output safe to show: what a carriage return overwrote is
// dropped, tabs become spaces, and escape sequences go, except colour (SGR) ones when color is
// set. Cursor movement or screen clearing could break whatever shows the line.
func Clean(l string, color bool) string {
	l = strings.TrimRight(l, "\r")
	if i := strings.LastIndexByte(l, '\r'); i >= 0 {
		l = l[i+1:]
	}
	var b strings.Builder
	col := 0
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch {
		case c == 0x1b:
			n, sgr := escLen(l[i:])
			if sgr && color {
				b.WriteString(l[i : i+n])
			}
			i += n - 1
		case c == '\t':
			k := 8 - col%8
			b.WriteString(strings.Repeat(" ", k))
			col += k
		case c < 0x20 || c == 0x7f:
		default:
			b.WriteByte(c)
			if c < 0x80 || c >= 0xc0 {
				col++
			}
		}
	}
	return b.String()
}

// escLen: the length of the escape sequence at the start of s, and whether it sets colours.
func escLen(s string) (int, bool) {
	if len(s) < 2 {
		return len(s), false
	}
	switch s[1] {
	case '[': // CSI: parameters, intermediates, a final byte
		j := 2
		for j < len(s) && s[j] >= 0x30 && s[j] <= 0x3f {
			j++
		}
		for j < len(s) && s[j] >= 0x20 && s[j] <= 0x2f {
			j++
		}
		if j < len(s) && s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1, s[j] == 'm' && !strings.ContainsAny(s[2:j], "<=>?")
		}
		return j, false
	case ']', 'P', '_', '^': // OSC and other strings: to BEL or ST
		for j := 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1, false
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, false
			}
		}
		return len(s), false
	}
	return 2, false
}

// Lines: a writer that hands each complete line (cleaned) to f, and the rest on Flush.
type Lines struct {
	mu    sync.Mutex
	Color bool
	F     func(string)
	part  string
}

func (w *Lines) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.part += string(p)
	for {
		i := strings.IndexByte(w.part, '\n')
		if i < 0 {
			break
		}
		w.F(Clean(w.part[:i], w.Color))
		w.part = w.part[i+1:]
	}
	if len(w.part) > 1<<20 { // a line with no end: show what there is
		w.F(Clean(w.part, w.Color))
		w.part = ""
	}
	return len(p), nil
}

// Flush hands on a last line with no newline.
func (w *Lines) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.part != "" {
		w.F(Clean(w.part, w.Color))
		w.part = ""
	}
}

// Writer: lines cleaned for out (colour kept when color), as they come.
func Writer(out io.Writer, color bool) *Lines {
	return &Lines{Color: color, F: func(l string) { io.WriteString(out, l+"\n") }}
}
