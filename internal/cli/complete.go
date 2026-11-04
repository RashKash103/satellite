package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/ui"
	"github.com/RashKash103/satellite/internal/view"
)

// Shell completion. The scripts are small and fixed: every candidate comes from the hidden
// `sat __complete SERVICE WORD... CURRENT`, which reads only this node's files (no network, no
// systemctl), so completion follows projects, views and env files as they change. Its output is
// groups: a line "#TAG DESCRIPTION", then "VALUE<tab>DESCRIPTION" lines. A value ending in = takes
// no space after it. The shell filters by what has been typed.

const zshCompletion = `#compdef sat sat-call
# zsh completion for sat, a Satellite node (docs/node.md#shell-completion).
#   sat completion zsh > "${fpath[1]}/_sat"     or: sat completion install
# Every candidate comes from ` + "`sat __complete`" + `, which reads only this node's files.

_sat() {
  local exe=sat line v d tag=values desc=value ret=1
  [[ $service == sat && $words[1] == */* ]] && exe=${~words[1]}
  local -a out cands nospace
  out=("${(@f)$($exe __complete $service "${(@)words[2,CURRENT]}" 2>/dev/null)}")
  for line in $out "#"; do
    if [[ $line == '#'* ]]; then
      (( $#cands )) && _describe -t "$tag" "$desc" cands && ret=0
      (( $#nospace )) && _describe -t "$tag" "$desc" nospace -S '' && ret=0
      cands=() nospace=()
      line=${line#\#}
      tag=${line%% *} desc=${line#* }
      continue
    fi
    [[ -n $line ]] || continue
    v=${line%%$'\t'*} d=
    [[ $line == *$'\t'* ]] && d=${line#*$'\t'}
    v=${v//:/\\:}
    if [[ $v == *= ]]; then nospace+=("$v${d:+:$d}"); else cands+=("$v${d:+:$d}"); fi
  done
  return ret
}

_sat "$@"
`

const bashCompletion = `# bash completion for sat, a Satellite node (docs/node.md#shell-completion).
#   sat completion bash > ~/.local/share/bash-completion/completions/sat
# Every candidate comes from ` + "`sat __complete`" + `, which reads only this node's files.

_sat() {
  local exe=sat svc=${1##*/} line v all=1 cur
  [[ $svc == sat && $1 == */* ]] && exe=$1
  local -a words
  read -ra words <<< "${COMP_LINE:0:COMP_POINT}"
  [[ ${COMP_LINE:0:COMP_POINT} =~ [[:space:]]$ ]] && words+=("")
  cur=${words[-1]}
  COMPREPLY=()
  [[ $cur == *=* ]] && return 0
  while IFS= read -r line; do
    [[ -z $line || $line == '#'* ]] && continue
    v=${line%%$'\t'*}
    [[ $v == "$cur"* ]] || continue
    COMPREPLY+=("$v")
    [[ $v == *= ]] || all=0
  done < <("$exe" __complete "$svc" "${words[@]:1}" 2>/dev/null)
  (( ${#COMPREPLY[@]} && all )) && compopt -o nospace 2>/dev/null
  return 0
}

complete -F _sat sat sat-call
`

// completionCmd: sat completion zsh|bash|install.
func completionCmd(a []string) int {
	const use = "usage: sat completion zsh|bash|install"
	if len(a) != 1 {
		return fail(use)
	}
	switch a[0] {
	case "zsh":
		fmt.Print(zshCompletion)
	case "bash":
		fmt.Print(bashCompletion)
	case "install":
		f := filepath.Join(conf.Root(), "completions", "_sat")
		if err := conf.WriteAtomic(f, []byte(zshCompletion), 0o644); err != nil {
			return fail(err.Error())
		}
		t := ui.Out()
		t.Done("wrote %s", tilde(f))
		t.Println("Add this line to ~/.zshrc (it is left to you), then start a new shell:")
		t.Println()
		t.Println("  " + t.Accent(completionLine))
		t.Println()
		t.Println(t.Muted("If your .zshrc already runs compinit (a framework may), put only the fpath part before it."))
	default:
		return fail(use)
	}
	return 0
}

const completionLine = `fpath=(~/.sat/completions $fpath); autoload -Uz compinit && compinit`

type cand struct{ v, d string }

type group struct {
	tag, desc string
	c         []cand
}

type completer struct{ gs []group }

func (c *completer) add(tag, desc string, cs ...cand) {
	if len(cs) > 0 {
		c.gs = append(c.gs, group{tag, desc, cs})
	}
}

func (c *completer) print() {
	for _, g := range c.gs {
		fmt.Printf("#%s %s\n", g.tag, g.desc)
		for _, x := range g.c {
			fmt.Printf("%s\t%s\n", x.v, oneLine(x.d))
		}
	}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		s = string(r[:59]) + "…"
	}
	return s
}

// completeCmd: sat __complete SERVICE WORD... CURRENT (the words after the command's name).
func completeCmd(a []string) int {
	if len(a) == 0 {
		return 0
	}
	svc, words := a[0], a[1:]
	if len(words) == 0 {
		words = []string{""}
	}
	c := &completer{}
	if svc == "sat-call" {
		c.satCall(words)
	} else {
		c.sat(words)
	}
	c.print()
	return 0
}

func projectCands() []cand {
	var cs []cand
	for _, n := range conf.List() {
		d := "project"
		if p, err := conf.Load(n); err == nil {
			d += ", hub " + p.Get("HUB_IP", p.Get("HUB", "?"))
			if s := p.Get("SLOTS", "1"); s != "1" {
				d += ", " + s + " lanes"
			}
		}
		cs = append(cs, cand{n, d})
	}
	return cs
}

func cmdCands(node bool) []cand {
	var cs []cand
	for _, h := range helps {
		if h.node == node {
			cs = append(cs, cand{h.name, h.short})
		}
	}
	return cs
}

// positional: the words that aren't flags (nor the value of a flag that takes one).
func positional(a []string, valued ...string) []string {
	var r []string
	for i := 0; i < len(a); i++ {
		switch {
		case slices.Contains(valued, a[i]):
			i++
		case strings.HasPrefix(a[i], "-") && len(a[i]) > 1:
		default:
			r = append(r, a[i])
		}
	}
	return r
}

func (c *completer) flags(cur string, others bool, fs ...cand) {
	if strings.HasPrefix(cur, "-") || !others {
		c.add("options", "option", fs...)
	}
}

var (
	yesFlags   = []cand{{"--yes", "don't ask"}, {"-y", "don't ask"}}
	watchFlags = []cand{{"-w", "keep it live"}, {"--watch", "keep it live"}}
	helpFlag   = cand{"--help", "this command's help"}
)

func (c *completer) sat(words []string) {
	prev, cur := words[:len(words)-1], words[len(words)-1]
	ps := conf.List()
	if len(prev) == 0 {
		c.add("commands", "project command", cmdCands(false)...)
		c.add("node-commands", "node command", cmdCands(true)...)
		c.add("projects", "project", projectCands()...)
		return
	}
	if slices.Contains(ps, prev[0]) {
		if len(prev) == 1 {
			c.add("commands", "project command", cmdCands(false)...)
			return
		}
		c.projectArgs(prev[0], prev[1], prev[2:], cur, false)
		return
	}
	cmd, rest := prev[0], prev[1:]
	h := helpFor(cmd)
	switch {
	case h == nil:
	case h.node:
		c.nodeArgs(cmd, rest, cur)
	case len(rest) > 0 && slices.Contains(ps, rest[0]):
		c.projectArgs(rest[0], cmd, rest[1:], cur, false) // sat CMD PROJECT ...
	default:
		name := ""
		if len(ps) == 1 {
			name = ps[0]
		}
		c.projectArgs(name, cmd, rest, cur, len(ps) > 1 || cmd != "view")
	}
}

// projectArgs: what follows `sat PROJECT CMD` (name may be "": not known yet). orProject: a
// project may come next (sat CMD PROJECT).
func (c *completer) projectArgs(name, cmd string, a []string, cur string, orProject bool) {
	var p *conf.Project
	if name != "" {
		p, _ = conf.Load(name)
	}
	pos := positional(a, "--lane")
	if orProject && len(a) == 0 {
		c.add("projects", "project", projectCands()...)
	}
	switch cmd {
	case "autostart":
		if len(pos) == 0 {
			c.add("values", "autostart", cand{"on", "start at boot"}, cand{"off", "don't start at boot"})
		}
	case "schedule":
		switch len(pos) {
		case 0:
			c.add("values", "schedule", cand{"start", "drain from a time"}, cand{"end", "be stopped by a time"}, cand{"off", "remove the schedule"})
		case 2:
			if pos[0] == "start" || pos[0] == "end" {
				c.add("values", "repeat", cand{"daily", "every day"}, cand{"once", "the next time only"})
			}
		}
	case "alerts":
		c.flags(cur, len(c.gs) > 0, append(watchFlags, helpFlag)...)
	case "view":
		if len(a) > 0 && a[len(a)-1] == "--lane" {
			c.add("lanes", "lane", laneCands(p)...)
			return
		}
		if len(pos) == 0 && p != nil {
			var vs []cand
			for _, v := range view.List(p) {
				d := v.Title
				if v.Err != nil {
					d = "(bad header)"
				} else if v.Scope == view.ScopeLane {
					d += " (lane)"
				}
				vs = append(vs, cand{v.Name, d})
			}
			c.add("views", "view", vs...)
		}
		c.flags(cur, len(c.gs) > 0, append(append([]cand{{"--lane", "the lane a lane view looks at"}}, watchFlags...), helpFlag)...)
	case "env":
		if len(a) == 0 && p != nil {
			var unset, set []cand
			for _, kv := range envKeys(p) {
				unset = append(unset, cand{"-" + kv.v, "unset (now " + kv.d + ")"})
				set = append(set, cand{kv.v + "=", "set (now " + kv.d + ")"})
			}
			c.add("keys", "set a key", set...)
			c.add("unset", "unset a key", unset...)
		}
	case "config":
		if len(a) == 0 && p != nil {
			var unset, set []cand
			for _, kv := range configKeys(p) {
				unset = append(unset, cand{"-" + kv.v, "unset (now " + kv.d + ")"})
				set = append(set, cand{kv.v + "=", "set (now " + kv.d + ")"})
			}
			c.add("keys", "set a key", set...)
			c.add("unset", "unset a key", unset...)
		}
	default:
		c.flags(cur, len(c.gs) > 0, helpFlag)
	}
}

func laneCands(p *conf.Project) []cand {
	if p == nil {
		return nil
	}
	n, _ := strconv.Atoi(p.Get("SLOTS", "1"))
	jobs := map[int]string{}
	if st, err := agent.ReadState(p); err == nil && st.Live() {
		for _, l := range st.Lanes {
			if l.ID != "" {
				jobs[l.Slot] = l.ID
			}
		}
	}
	var cs []cand
	for i := 1; i <= max(n, 1); i++ {
		d := "idle"
		if j := jobs[i]; j != "" {
			d = "running " + j
		}
		cs = append(cs, cand{strconv.Itoa(i), d})
	}
	return cs
}

// configKeys: the keys the project's config sets, with their values as written.
func configKeys(p *conf.Project) []cand {
	b, _ := os.ReadFile(filepath.Join(p.Base, "config"))
	var cs []cand
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "=")
		if ok && envKey.MatchString(k) && !slices.ContainsFunc(cs, func(c cand) bool { return c.v == k }) {
			if i := strings.IndexAny(v, " \t"); i >= 0 {
				v = v[:i] // a comment after the value
			}
			cs = append(cs, cand{k, v})
		}
	}
	return cs
}

// envKeys: the keys the project's env file sets, with their values as written.
func envKeys(p *conf.Project) []cand {
	b, _ := os.ReadFile(filepath.Join(p.Base, "env"))
	var cs []cand
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "=")
		if ok && envKey.MatchString(k) && !slices.ContainsFunc(cs, func(c cand) bool { return c.v == k }) {
			cs = append(cs, cand{k, v})
		}
	}
	return cs
}

// hubValues: a config key's values across the projects (for setup: hubs already used).
func hubValues(k string) []cand {
	var cs []cand
	for _, n := range conf.List() {
		if p, err := conf.Load(n); err == nil {
			if v := p.Conf[k]; v != "" && !slices.ContainsFunc(cs, func(c cand) bool { return c.v == v }) {
				cs = append(cs, cand{v, "used by " + n})
			}
		}
	}
	return cs
}

func (c *completer) nodeArgs(cmd string, a []string, cur string) {
	pos := positional(a)
	switch cmd {
	case "setup":
		switch len(pos) {
		case 0:
			c.add("hubs", "hub", hubValues("HUB_IP")...)
		case 2:
			c.add("users", "hub user", hubValues("HUB_USER")...)
		}
		c.flags(cur, len(c.gs) > 0, cand{"--install", "install what is missing (shown, and asked first)"}, helpFlag)
	case "self-update", "migrate":
		c.flags(cur, false, append(yesFlags, helpFlag)...)
	case "uninstall":
		if len(pos) == 0 {
			c.add("projects", "project", projectCands()...)
		}
		c.flags(cur, len(c.gs) > 0, append(yesFlags, helpFlag)...)
	case "help":
		if len(pos) == 0 {
			var cs []cand
			for _, h := range helps {
				cs = append(cs, cand{h.name, h.short})
			}
			c.add("topics", "help topic", cs...)
		}
	case "completion":
		if len(pos) == 0 {
			c.add("values", "shell", cand{"zsh", "print the zsh completion"}, cand{"bash", "print the bash completion"},
				cand{"install", "write it to ~/.sat/completions/_sat"})
		}
	}
}

// sat-call [PROJECT] COMMAND ...: the hub commands a job uses.
func (c *completer) satCall(words []string) {
	prev := words[:len(words)-1]
	hubCmds := []cand{{"get", "fetch a file the project's hooks allow"}, {"put", "send a file (stdin) the hooks allow"},
		{"status", "the project's leases"}, {"nodes", "this node's row: last call, jobs held, its key's projects"}}
	switch {
	case os.Getenv("SAT_PROJECT") != "" && len(prev) == 0:
		c.add("commands", "hub command", hubCmds...)
	case os.Getenv("SAT_PROJECT") == "" && len(prev) == 0:
		c.add("projects", "project", projectCands()...)
	case os.Getenv("SAT_PROJECT") == "" && len(prev) == 1:
		c.add("commands", "hub command", hubCmds...)
	}
}
