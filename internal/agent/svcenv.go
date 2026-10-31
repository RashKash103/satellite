package agent

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/plan"
)

// ServiceEnv: the environment sat@PROJECT.service starts with, from the user manager
// (systemctl --user show-environment) plus what systemd sets for each service (HOME, USER, ...).
// A service reads no shell profile, so PATH additions made there (nvm, cargo, ~/.local/bin) are
// not in it. Checks run from a shell use it, so they see what the service will.
func ServiceEnv() ([]string, error) {
	out, err := exec.Command(plan.Systemctl(), "--user", "show-environment").Output()
	if err != nil {
		return nil, err
	}
	var env []string
	seen := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || k == "" {
			continue
		}
		env = append(env, k+"="+unescape(v))
		seen[k] = true
	}
	if len(env) == 0 {
		return nil, errors.New("the user manager gave no environment")
	}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "SHELL"} {
		if v, ok := os.LookupEnv(k); ok && !seen[k] {
			env = append(env, k+"="+v)
		}
	}
	return env, nil
}

// unescape a show-environment value: systemd prints values that need it as $'...' (C escapes).
func unescape(v string) string {
	if len(v) < 3 || !strings.HasPrefix(v, "$'") || !strings.HasSuffix(v, "'") {
		return v
	}
	s := v[2 : len(v)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'e', 'E':
			b.WriteByte(0x1b)
		case 'x':
			if i+2 < len(s) {
				if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(n))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		default:
			if c >= '0' && c <= '7' && i+2 < len(s) {
				if n, err := strconv.ParseUint(s[i:i+3], 8, 8); err == nil {
					b.WriteByte(byte(n))
					i += 2
					continue
				}
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

// CheckEnv: the environment to check a project's requirements in from a shell: the service's
// (ServiceEnv) when the user manager can say, else this shell's. svc tells which it is.
func CheckEnv(p *conf.Project) (env []string, svc bool) {
	base, err := ServiceEnv()
	if err != nil {
		return Env(p), false
	}
	return EnvFrom(base, p), true
}
