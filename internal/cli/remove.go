package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/hub"
)

// uninstall [PROJECT]: one project, or (none named, or the last one) everything sat put here.
func uninstall(a []string) int {
	yes, a := flag(a, "--yes", "-y")
	all := conf.List()
	var targets []string
	switch {
	case len(a) > 1:
		return fail("usage: sat uninstall [--yes] [PROJECT]")
	case len(a) == 1 && !slices.Contains(all, a[0]):
		return fail("no project " + a[0] + " on this node")
	case len(a) == 1:
		targets = a
	default:
		targets = all
	}
	last := len(targets) == len(all)
	var ps []*conf.Project
	keep := map[string]bool{} // hubs other projects still use
	for _, n := range all {
		p, err := conf.Load(n)
		if err != nil {
			continue
		}
		if slices.Contains(targets, n) {
			ps = append(ps, p)
		} else {
			keep[p.Conf["HUB"]] = true
		}
	}
	var hubs []*conf.Project // one project per hub this node leaves
	seen := map[string]bool{}
	for _, p := range ps {
		h := p.Conf["HUB"]
		if !keep[h] && !seen[h] {
			seen[h] = true
			hubs = append(hubs, p)
		}
	}
	ours := func(p string) bool { t, _ := os.Readlink(p); return t == conf.Exe() }

	fmt.Println("This will:")
	for _, p := range ps {
		fmt.Printf("- stop and disable %s, handing its running jobs back\n", p.Unit())
		fmt.Printf("- remove %s (%s)\n", tilde(p.Base), human(du(p.Base)))
	}
	for _, p := range hubs {
		fmt.Printf("- ask the hub %s to forget this node's key (sat-hub --forget)\n", p.Conf["HUB"])
		fmt.Printf("- remove the SSH alias %s from ~/.ssh/config\n", p.Conf["HUB"])
	}
	if last {
		if _, err := os.Stat(conf.Root()); err == nil {
			fmt.Printf("- remove %s: the sat binary, sat-call and the node settings\n", tilde(conf.Root()))
		}
		if _, err := os.Stat(unitPath()); err == nil {
			fmt.Printf("- remove the unit %s\n", tilde(unitPath()))
		}
		if ours(localSat()) {
			fmt.Printf("- remove %s (a link to the binary)\n", tilde(localSat()))
		}
		if _, err := os.Stat(conf.Key()); err == nil {
			fmt.Printf("- remove the node key %s and %s.pub\n", tilde(conf.Key()), tilde(conf.Key()))
		}
	}
	fmt.Println("It removes no packages.")
	if b, err := os.ReadFile(installLog()); err == nil && len(b) > 0 {
		fmt.Println("`sat install` ran these; anything they installed is yours to keep or remove:")
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			fmt.Println("  " + l)
		}
	}
	if len(ps) == 0 && !last {
		return 0
	}
	if !yes && !ask("Go ahead?") {
		return 1
	}
	for _, p := range ps {
		sc("stop", p.Unit()).Run() // its ExecStop hands the jobs back
		c := hub.For(p)
		agent.Release(context.Background(), p, c, os.Stderr) // in case it wasn't running under systemd
		c.Close()
		sc("disable", p.Unit()).Run()
	}
	for _, p := range hubs {
		c := hub.For(p)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, rc, err := hub.Out(ctx, c, "--forget")
		cancel()
		c.Close()
		if err == nil && rc == 0 {
			fmt.Printf("hub %s: %s\n", p.Conf["HUB"], strings.TrimSpace(out))
		} else {
			fmt.Printf("hub %s did not forget the key (%v): remove its line (it ends in sat@%s) from the hub's ~/.ssh/authorized_keys yourself\n", p.Conf["HUB"], errOr(err, rc), p.NodeName())
		}
		if _, err := removeAlias(sshConfig(), p.Conf["HUB"]); err != nil {
			fmt.Println("~/.ssh/config:", err)
		}
	}
	for _, p := range ps {
		if err := os.RemoveAll(p.Base); err != nil {
			fmt.Println(err)
		}
	}
	if last {
		os.Remove(unitPath())
		sc("daemon-reload").Run()
		if ours(localSat()) {
			os.Remove(localSat())
		}
		os.Remove(conf.Key())
		os.Remove(conf.Key() + ".pub")
		os.RemoveAll(conf.Root())
	}
	fmt.Println("done")
	return 0
}

func errOr(err error, rc int) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("exit %d", rc)
}

// migrate moves a node set up by the bash scripts (~/sat/PROJECT, ~/sat/bin, ~/.local/bin/sat
// and their unit) to ~/.sat. A bundle's sat/moved OLD NEW, if any, runs after its project moved.
func migrate(a []string) int {
	yes, a := flag(a, "--yes", "-y")
	if len(a) > 0 {
		return fail("usage: sat migrate [--yes]")
	}
	old := filepath.Join(conf.Home(), "sat")
	fs, _ := filepath.Glob(filepath.Join(old, "*", "config"))
	var names []string
	for _, f := range fs {
		if n := filepath.Base(filepath.Dir(f)); n != "bin" && conf.NameOK(n) {
			names = append(names, n)
		}
	}
	var scripts []string
	for _, f := range []string{"sat-agent", "sat-call", "sat-lib"} {
		if p := filepath.Join(old, "bin", f); bashOurs(p) {
			scripts = append(scripts, p)
		}
	}
	if bashOurs(localSat()) {
		scripts = append(scripts, localSat())
	}
	if len(names) == 0 && len(scripts) == 0 {
		fmt.Println("nothing to migrate: no ~/sat/PROJECT here")
		return 0
	}
	type st struct{ active, enabled bool }
	was := map[string]st{}
	fmt.Println("This will:")
	for _, n := range names {
		u := "sat@" + n + ".service"
		s := st{active(u), scOut("is-enabled", u) == "enabled"}
		was[n] = s
		if s.active {
			fmt.Printf("- stop %s (its running jobs go back to the hub), and start it again afterwards\n", u)
		}
		if _, err := os.Stat(conf.Dir(n)); err == nil {
			return fail(fmt.Sprintf("both %s and %s exist: move one aside first", tilde(filepath.Join(old, n)), tilde(conf.Dir(n))))
		}
		fmt.Printf("- move %s to %s, then run its bundle's sat/moved if it has one\n", tilde(filepath.Join(old, n)), tilde(conf.Dir(n)))
	}
	fmt.Printf("- install this binary as %s (with sat-call) and the new %s", tilde(conf.Exe()), tilde(unitPath()))
	if len(names) > 0 {
		fmt.Print(", keeping each project's autostart setting")
	}
	fmt.Println()
	for _, s := range scripts {
		if s == localSat() {
			fmt.Printf("- replace the bash %s with a link to the binary\n", tilde(s))
		} else {
			fmt.Printf("- remove the bash node's %s\n", tilde(s))
		}
	}
	if !yes && !ask("Go ahead?") {
		return 1
	}
	for _, n := range names {
		if was[n].active {
			// The bash unit's ExecStop (~/.local/bin/sat PROJECT _release) hands the jobs back.
			if err := sc("stop", "sat@"+n+".service").Run(); err != nil {
				return fail("could not stop sat@" + n + ".service: " + err.Error())
			}
		}
	}
	if err := installSelf(); err != nil {
		return fail("install: " + err.Error())
	}
	for _, s := range scripts {
		if s != localSat() {
			os.Remove(s)
		}
	}
	os.Remove(filepath.Join(old, "bin"))
	rc := 0
	for _, n := range names {
		from, to := filepath.Join(old, n), conf.Dir(n)
		os.MkdirAll(conf.Projects(), 0o755)
		if err := os.Rename(from, to); err != nil {
			if out, err := exec.Command("mv", from, to).CombinedOutput(); err != nil {
				fmt.Printf("could not move %s: %s\n", from, out)
				rc = 1
				continue
			}
		}
		fmt.Printf("moved %s to %s\n", tilde(from), tilde(to))
		p, err := conf.Load(n)
		if err != nil {
			fmt.Println(err)
			rc = 1
			continue
		}
		if m := filepath.Join(p.Bundle, "sat", "moved"); isExec(m) {
			if code(run(agent.Command(agent.Env(p), m, from, to))) != 0 {
				fmt.Printf("%s's sat/moved failed: check %s before starting it\n", n, tilde(to))
				rc = 1
				continue
			}
		}
		if was[n].enabled {
			sc("enable", p.Unit()).Run()
		}
		if was[n].active && start(p) != 0 {
			rc = 1
		}
	}
	os.Remove(old) // only if empty
	if rc == 0 {
		fmt.Println("migrated")
	}
	return rc
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode()&0o111 != 0
}
