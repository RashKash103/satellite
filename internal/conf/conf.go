// Package conf reads the node's KEY=VALUE files and knows where everything lives:
//
//	~/.sat/bin/sat, ~/.sat/bin/sat-call     the binary, and the name jobs call it by
//	~/.sat/config                           node-wide: AUTO_UPDATE, PIN, TRANSPORT
//	~/.sat/projects/PROJECT/                config, env, bundle/, state/ and the project's own files
//
// The files stay shell-sourceable: jobs source the config to get HUB.
package conf

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// NameOK: a plain name, as the hub accepts for projects, nodes and job IDs.
func NameOK(s string) bool { return nameRe.MatchString(s) && s != "." && s != ".." }

func Home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func Root() string     { return filepath.Join(Home(), ".sat") }
func Bin() string      { return filepath.Join(Root(), "bin") }
func Exe() string      { return filepath.Join(Bin(), "sat") }
func Projects() string { return filepath.Join(Root(), "projects") }
func Key() string      { return filepath.Join(Home(), ".ssh", "sat_ed25519") }
func UnitDir() string  { return filepath.Join(Home(), ".config", "systemd", "user") }

// Parse reads KEY=VALUE lines the way sh would for the simple cases: an optional `export`,
// single or double quotes, $VAR and ${VAR} (from earlier lines, then lookup), # comments.
// Anything else on a line is ignored.
func Parse(path string, lookup func(string) string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	get := func(k string) string {
		if v, ok := m[k]; ok {
			return v
		}
		if lookup != nil {
			return lookup(k)
		}
		return ""
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := ParseLine(sc.Text(), get)
		if ok {
			m[k] = v
		}
	}
	return m, sc.Err()
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func ParseLine(line string, get func(string) string) (string, string, bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "export ")
	k, rest, ok := strings.Cut(s, "=")
	if !ok || !keyRe.MatchString(k) {
		return "", "", false
	}
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c == ' ' || c == '\t':
			return k, b.String(), true // the rest is a comment or another command
		case c == '\'':
			j := strings.IndexByte(rest[i+1:], '\'')
			if j < 0 {
				return "", "", false
			}
			b.WriteString(rest[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			j := strings.IndexByte(rest[i+1:], '"')
			if j < 0 {
				return "", "", false
			}
			b.WriteString(os.Expand(rest[i+1:i+1+j], get))
			i += j + 1
		case c == '$':
			n := varLen(rest[i+1:])
			b.WriteString(os.Expand(rest[i:i+1+n], get))
			i += n
		case c == '\\' && i+1 < len(rest):
			b.WriteByte(rest[i+1])
			i++
		default:
			b.WriteByte(c)
		}
	}
	return k, b.String(), true
}

// varLen: how much of s (after a $) names the variable: {NAME} or NAME.
func varLen(s string) int {
	if strings.HasPrefix(s, "{") {
		if j := strings.IndexByte(s, '}'); j > 0 {
			return j + 1
		}
		return 0
	}
	n := 0
	for n < len(s) && (s[n] == '_' || s[n] >= 'A' && s[n] <= 'Z' || s[n] >= 'a' && s[n] <= 'z' || n > 0 && s[n] >= '0' && s[n] <= '9') {
		n++
	}
	return n
}

// Node is ~/.sat/config (missing: empty).
func Node() map[string]string {
	m, err := Parse(filepath.Join(Root(), "config"), nil)
	if err != nil {
		return map[string]string{}
	}
	return m
}

type Project struct {
	Name, Base, Bundle, State string
	Conf                      map[string]string
}

func (p *Project) Get(k, def string) string {
	if v := p.Conf[k]; v != "" {
		return v
	}
	if v := Node()[k]; v != "" {
		return v
	}
	return def
}

// NodeName: SAT_NAME, else the short hostname.
func (p *Project) NodeName() string {
	if n := p.Conf["SAT_NAME"]; n != "" {
		return n
	}
	return ShortHost()
}

func (p *Project) Unit() string { return "sat@" + p.Name + ".service" }

func ShortHost() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	return h
}

func Dir(name string) string { return filepath.Join(Projects(), name) }

func Load(name string) (*Project, error) {
	if !NameOK(name) {
		return nil, fmt.Errorf("not a project name: %q", name)
	}
	b := Dir(name)
	m, err := Parse(filepath.Join(b, "config"), nil)
	if err != nil {
		return nil, fmt.Errorf("no project %s on this node (%v)", name, err)
	}
	return &Project{Name: name, Base: b, Bundle: filepath.Join(b, "bundle"), State: filepath.Join(b, "state"), Conf: m}, nil
}

// List: the projects set up on this node.
func List() []string {
	es, _ := os.ReadDir(Projects())
	var out []string
	for _, e := range es {
		if _, err := os.Stat(filepath.Join(Projects(), e.Name(), "config")); err == nil && NameOK(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// WriteAtomic writes a file beside its target and renames it into place.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
