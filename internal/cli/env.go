package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// setsKey: the line assigns KEY (KEY=..., export KEY=...).
func setsKey(line, key string) bool {
	l := strings.TrimPrefix(strings.TrimSpace(line), "export ")
	return strings.HasPrefix(l, key+"=")
}

// envCmd: sat PROJECT env [KEY=VALUE | -KEY]: list, set or unset the project's env file, which
// every script and job of the project sources (with shell semantics) when it starts.
func envCmd(p *conf.Project, a []string) int {
	f := filepath.Join(p.Base, "env")
	b, _ := os.ReadFile(f)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(b) == 0 {
		lines = nil
	}
	t := ui.Out()
	if len(a) == 0 {
		if len(lines) == 0 {
			t.Info("%s has no env (%s)", p.Name, tilde(f))
			return 0
		}
		if !t.Color() {
			for _, l := range lines {
				fmt.Println(l)
			}
			return 0
		}
		envTable(t, p.Name, tilde(f), lines)
		return 0
	}
	if len(a) != 1 {
		return fail("usage: sat PROJECT env [KEY=VALUE | -KEY]")
	}
	key, val, set := strings.Cut(a[0], "=")
	if !set {
		key = strings.TrimPrefix(a[0], "-")
		if !strings.HasPrefix(a[0], "-") {
			return fail("usage: sat PROJECT env [KEY=VALUE | -KEY]")
		}
	}
	if !envKey.MatchString(key) {
		return fail("not a variable name: " + key)
	}
	var out []string
	found := false
	for _, l := range lines {
		if setsKey(l, key) {
			found = true
			continue
		}
		out = append(out, l)
	}
	if set {
		out = append(out, "export "+key+"="+shq(val))
	} else if !found {
		t.Info("%s sets no %s", p.Name, key)
		return 0
	}
	data := strings.Join(out, "\n")
	if data != "" {
		data += "\n"
	}
	if err := conf.WriteAtomic(f, []byte(data), 0o644); err != nil {
		return fail(err.Error())
	}
	what := map[bool]string{true: "set " + t.Bold(key), false: "unset " + t.Bold(key)}[set]
	t.Done("%s for %s: it applies from the next job %s", what, p.Name, t.Muted("(running ones keep what they started with)"))
	return 0
}

// envTable: the env file as KEY and value (as written: shell syntax), other lines muted.
func envTable(t *ui.T, name, file string, lines []string) {
	t.Println(t.Bold(name) + t.Muted(" env · "+file))
	var rows [][]string
	flush := func() {
		if len(rows) > 0 {
			t.Print(ui.Columns("  ", rows))
			rows = nil
		}
	}
	for _, l := range lines {
		x := strings.TrimSpace(l)
		k, v, ok := strings.Cut(strings.TrimPrefix(x, "export "), "=")
		if ok && envKey.MatchString(k) {
			rows = append(rows, []string{t.Accent(k), v})
			continue
		}
		flush()
		t.Println("  " + t.Muted(l))
	}
	flush()
}
