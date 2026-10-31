package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/RashKash103/satellite/internal/conf"
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
	if len(a) == 0 {
		if len(lines) == 0 {
			fmt.Printf("%s has no env (%s)\n", p.Name, tilde(f))
		}
		for _, l := range lines {
			fmt.Println(l)
		}
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
		fmt.Printf("%s sets no %s\n", p.Name, key)
		return 0
	}
	data := strings.Join(out, "\n")
	if data != "" {
		data += "\n"
	}
	if err := conf.WriteAtomic(f, []byte(data), 0o644); err != nil {
		return fail(err.Error())
	}
	what := map[bool]string{true: "set " + key, false: "unset " + key}[set]
	fmt.Printf("%s for %s: it applies from the next job (running ones keep what they started with)\n", what, p.Name)
	return 0
}
