package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
)

// plainValue: written as is; anything else goes in single quotes.
var plainValue = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]*$`)

// configCmd: sat PROJECT config [KEY=VALUE | -KEY]: list, set or unset a key of the project's config.
// A running service applies it within seconds (SLOTS opens or closes lanes), except the keys that
// shape its connection to the hub, which wait for the next start.
func configCmd(p *conf.Project, a []string) int {
	f := filepath.Join(p.Base, "config")
	b, err := os.ReadFile(f)
	if err != nil {
		return fail(err.Error())
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	t := ui.Out()
	if len(a) == 0 {
		if !t.Color() {
			fmt.Print(string(b))
			return 0
		}
		configTable(t, p.Name, tilde(f), lines)
		return 0
	}
	usage := "usage: sat PROJECT config [KEY=VALUE | -KEY]"
	if len(a) != 1 {
		return fail(usage)
	}
	key, val, set := strings.Cut(a[0], "=")
	if !set {
		key = strings.TrimPrefix(a[0], "-")
		if !strings.HasPrefix(a[0], "-") {
			return fail(usage)
		}
	}
	if !envKey.MatchString(key) {
		return fail("not a key name: " + key)
	}
	var out []string
	found := false
	for _, l := range lines {
		if setsKey(l, key) {
			if !found && set { // keep its place in the file
				out = append(out, key+"="+configValue(val))
			}
			found = true
			continue
		}
		out = append(out, l)
	}
	if set && !found {
		out = append(out, key+"="+configValue(val))
	} else if !set && !found {
		t.Info("%s's config sets no %s", p.Name, key)
		return 0
	}
	if err := conf.WriteAtomic(f, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		return fail(err.Error())
	}
	what := map[bool]string{true: "set " + t.Bold(key), false: "unset " + t.Bold(key)}[set]
	switch {
	case agent.RestartOnly(key):
		t.Done("%s for %s: it applies when the service starts again %s", what, p.Name, t.Muted("(sat "+p.Name+" stop && sat "+p.Name+" start)"))
	case active(p.Unit()):
		t.Done("%s for %s: the running service applies it within seconds", what, p.Name)
	default:
		t.Done("%s for %s: it applies when the service starts", what, p.Name)
	}
	return 0
}

func configValue(v string) string {
	if plainValue.MatchString(v) {
		return v
	}
	return shq(v)
}

// configTable: the config as KEY and value (as written), restart-only keys marked, other lines muted.
func configTable(t *ui.T, name, file string, lines []string) {
	t.Println(t.Bold(name) + t.Muted(" config · "+file))
	var rows [][]string
	flush := func() {
		if len(rows) > 0 {
			t.Print(ui.Columns("  ", rows))
			rows = nil
		}
	}
	for _, l := range lines {
		k, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "=")
		if ok && envKey.MatchString(k) {
			note := ""
			if agent.RestartOnly(k) {
				note = t.Muted("applies at the next start")
			}
			rows = append(rows, []string{t.Accent(k), v, note})
			continue
		}
		flush()
		t.Println("  " + t.Muted(l))
	}
	flush()
	t.Println(t.Muted("  A running service applies a change within seconds, except the keys marked."))
}
