package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RashKash103/satellite/internal/conf"
)

func sshConfig() string { return filepath.Join(conf.Home(), ".ssh", "config") }

// addAlias appends `Host ALIAS` for the hub, unless the file has one already. Jobs use it for git
// and rsync through the system ssh.
func addAlias(file, alias, ip, user, key string) error {
	b, _ := os.ReadFile(file)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == "Host "+alias {
			return nil
		}
	}
	s := string(b)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	s += fmt.Sprintf("Host %s\n  HostName %s\n  User %s\n  IdentityFile %s\n  IdentitiesOnly yes\n", alias, ip, user, key)
	return conf.WriteAtomic(file, []byte(s), 0o600)
}

// removeAlias drops the `Host ALIAS` block (up to the next Host or Match line).
func removeAlias(file, alias string) (bool, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return false, nil
	}
	var out []string
	in, found := false, false
	for _, l := range strings.SplitAfter(string(b), "\n") {
		t := strings.TrimSpace(l)
		if f := strings.Fields(t); len(f) > 0 && (strings.EqualFold(f[0], "Host") || strings.EqualFold(f[0], "Match")) {
			in = t == "Host "+alias
			found = found || in
		}
		if !in {
			out = append(out, l)
		}
	}
	if !found {
		return false, nil
	}
	return true, conf.WriteAtomic(file, []byte(strings.Join(out, "")), 0o600)
}
