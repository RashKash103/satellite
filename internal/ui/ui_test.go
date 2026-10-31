package ui

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestEnv(t *testing.T) {
	term := map[string]string{"TERM": "xterm-256color"}
	for _, c := range []struct {
		tty  bool
		env  map[string]string
		want bool
	}{
		{true, term, true},
		{false, term, false}, // a pipe or a file
		{true, map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, false},
		{true, map[string]string{"TERM": "xterm", "NO_COLOR": "yes", "CLICOLOR_FORCE": "1"}, false}, // NO_COLOR wins
		{true, map[string]string{"TERM": "dumb"}, false},
		{true, map[string]string{}, false}, // no TERM at all
		{false, map[string]string{"CLICOLOR_FORCE": "1"}, true},
		{false, map[string]string{"FORCE_COLOR": "1"}, true},
		{false, map[string]string{"FORCE_COLOR": "0"}, false},
		{true, map[string]string{"TERM": "xterm", "CLICOLOR": "0"}, false},
	} {
		if got := Env(c.tty, env(c.env)); got != c.want {
			t.Errorf("tty %v %v: %v", c.tty, c.env, got)
		}
	}
}

func TestPlainHasNoEscapes(t *testing.T) {
	var b strings.Builder
	p := Plain(&b)
	p.Println(p.OK("a"), p.Bad("b"), p.Warn("c"), p.Muted("d"), p.Accent("e"), p.Heading("f"), p.Badge("running"), p.Dot("active")+p.Mark(MarkOK)+p.Bar(40, 10), p.Rule("x"))
	p.Done("done")
	p.Print(p.Markdown("# notes\n- **a** `b`\n"))
	p.Print(p.Table("", []string{"A", "B"}, [][]string{{"x", "y"}}))
	p.Print(Prompt(p, "Go ahead?"))
	if strings.ContainsRune(b.String(), 0x1b) || strings.ContainsAny(b.String(), "✓●│─█") {
		t.Fatalf("%q", b.String())
	}
	if !strings.Contains(b.String(), "done\n") || !strings.Contains(b.String(), "# notes\n- **a** `b`") || !strings.HasSuffix(b.String(), "Go ahead? [y/N] ") {
		t.Fatalf("%q", b.String())
	}
}

func TestStyled(t *testing.T) {
	for _, dark := range []bool{true, false} {
		var b strings.Builder
		s := Styled(&b, dark, 60)
		s.Println(s.OK("ok"))
		if !strings.Contains(b.String(), "\x1b[") || Strip(b.String()) != "ok\n" {
			t.Errorf("dark %v: %q", dark, b.String())
		}
	}
	if Palette(true).OK.GetForeground() == Palette(false).OK.GetForeground() {
		t.Error("the same green on light and dark backgrounds")
	}
}

func TestColumns(t *testing.T) {
	var b strings.Builder
	s := Styled(&b, true, 0)
	got := Strip(Columns("  ", [][]string{{s.Bold("a"), "1", "x"}, {"bbb", "22", "y"}}))
	if got != "  a    1   x\n  bbb  22  y\n" {
		t.Fatalf("%q", got)
	}
}

func TestUnits(t *testing.T) {
	for d, w := range map[time.Duration]string{45 * time.Second: "45s", 192 * time.Second: "3m12s", 125 * time.Minute: "2h05m", 76 * time.Hour: "3d04h", -time.Second: "0s"} {
		if g := Dur(d); g != w {
			t.Errorf("%v: %s", d, g)
		}
	}
	if Bytes(512) != "512 B" || Bytes(3<<30+400<<20) != "3.4 GB" {
		t.Error(Bytes(512), Bytes(3<<30+400<<20))
	}
	now := time.Now()
	if Ago(now.Add(-12*time.Second), now) != "12s ago" || Ago(time.Time{}, now) != "never" {
		t.Error(Ago(now.Add(-12*time.Second), now))
	}
	if Plural(1, "step", "steps") != "1 step" || Plural(3, "step", "steps") != "3 steps" {
		t.Error("plural")
	}
}

func TestMarkdown(t *testing.T) {
	var b strings.Builder
	s := Styled(&b, true, 40)
	got := Strip(s.Markdown("## Changes\n- **faster** status, see [docs](https://example.com/d) and `sat status`, with a long line that wraps\n"))
	want := "Changes\n  • faster status, see docs\n    (https://example.com/d) and sat\n    status, with a long line that wraps"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestTruncate(t *testing.T) {
	if g := Truncate("abcdef", 4); Width(g) != 4 || !strings.HasSuffix(g, "…") {
		t.Fatal(g)
	}
	if Truncate("abc", 0) != "abc" {
		t.Fatal("0 should leave it")
	}
}
