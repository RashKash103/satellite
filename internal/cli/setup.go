package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/agent"
	"github.com/RashKash103/satellite/internal/bundle"
	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/hub"
	"github.com/RashKash103/satellite/internal/plan"
	"golang.org/x/crypto/ssh"
)

// Unit is sat@.service, one instance per project.
const Unit = `[Unit]
Description=Satellite node for %i: runs the hub's jobs while this machine is on
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=%h/.sat/bin/sat agent %i
# Hand jobs in progress back to the hub before systemd stops the processes.
ExecStop=%h/.sat/bin/sat %i _release
KillMode=control-group
TimeoutStopSec=90
Restart=on-failure
RestartSec=60
Nice=5

[Install]
WantedBy=default.target
`

func unitPath() string { return filepath.Join(conf.UnitDir(), "sat@.service") }

func installUnit() int {
	if b, err := os.ReadFile(unitPath()); err != nil || string(b) != Unit {
		if err := conf.WriteAtomic(unitPath(), []byte(Unit), 0o644); err != nil {
			return fail(err.Error())
		}
	}
	sc("daemon-reload").Run()
	return 0
}

// The bash node's files start with these lines; anything else is not ours to remove.
var bashHeaders = map[string]string{
	"sat":       "# sat [PROJECT] COMMAND: control this node",
	"sat-agent": "# sat-agent PROJECT: the node's service",
	"sat-call":  "# sat-call [PROJECT] COMMAND",
	"sat-lib":   "# sat-lib: sourced by sat-setup and sat",
}

func bashOurs(path string) bool {
	h, ok := bashHeaders[filepath.Base(path)]
	f, err := os.Open(path)
	if !ok || err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 512)
	n, _ := io.ReadFull(f, b)
	return bytes.Contains(b[:n], []byte("\n"+h))
}

func localSat() string { return filepath.Join(conf.Home(), ".local", "bin", "sat") }

// installSelf puts this binary at ~/.sat/bin/sat, sat-call beside it, the unit, and a
// ~/.local/bin/sat link when that name is free (or the bash node's script).
func installSelf() error {
	if err := os.MkdirAll(conf.Bin(), 0o755); err != nil {
		return err
	}
	me, _ := filepath.EvalSymlinks(exe())
	dst := conf.Exe()
	a, err1 := os.Stat(me)
	b, err2 := os.Stat(dst)
	if err1 != nil {
		return err1
	}
	if err2 != nil || !os.SameFile(a, b) {
		data, err := os.ReadFile(me)
		if err != nil {
			return err
		}
		if err := conf.WriteAtomic(dst, data, 0o755); err != nil {
			return err
		}
	}
	sc := filepath.Join(conf.Bin(), "sat-call")
	if t, _ := os.Readlink(sc); t != "sat" {
		os.Remove(sc)
		if err := os.Symlink("sat", sc); err != nil {
			return err
		}
	}
	if installUnit() != 0 {
		return fmt.Errorf("could not write %s", unitPath())
	}
	l := localSat()
	fi, err := os.Lstat(l)
	switch {
	case err != nil, fi.Mode()&os.ModeSymlink != 0, bashOurs(l):
		os.MkdirAll(filepath.Dir(l), 0o755)
		os.Remove(l)
		os.Symlink(dst, l)
	}
	return nil
}

// ensureKey makes ~/.ssh/sat_ed25519 if there is none.
func ensureKey(name string) error {
	k := conf.Key()
	if _, err := os.Stat(k); err == nil {
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "sat@"+name)
	if err != nil {
		return err
	}
	sp, _ := ssh.NewPublicKey(pub)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " sat@" + name + "\n"
	if err := os.WriteFile(k, pem.EncodeToMemory(blk), 0o600); err != nil {
		return err
	}
	return os.WriteFile(k+".pub", []byte(line), 0o644)
}

func say(s string) { fmt.Printf("\n== %s\n", s) }

// The hub builds the authorized_keys line itself, so its forced command is its own path to
// sat-ssh-gate; the node's name is bound to the key there. An older line for the same key is
// replaced, and an empty or malformed key is refused (never filter on an empty key).
const addKey = `g=$HOME/.local/bin/sat-ssh-gate; [ -x "$g" ] || { echo "the hub has no $g: run hub/install in its satellite checkout" >&2; exit 3; }
     read -r k; kb=$(echo "$k" | cut -d" " -f2); a=$HOME/.ssh/authorized_keys
     case "$kb" in AAAA?*) ;; *) echo "not a public key: $k" >&2; exit 4 ;; esac
     mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch "$a" && { grep -vF "$kb" "$a"; echo "command=\"$g $n\",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty $k"; } > "$a.sat-new" \
       && chmod 600 "$a.sat-new" && mv "$a.sat-new" "$a"`

func reach(c hub.Caller, proj string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, rc, err := hub.Out(ctx, c, proj, "status")
	if err == nil && rc != 0 {
		err = fmt.Errorf("exit %d", rc)
	}
	return err
}

func bashLayout() bool {
	fs, _ := filepath.Glob(filepath.Join(conf.Home(), "sat", "*", "config"))
	_, err := os.Stat(filepath.Join(conf.Home(), "sat", "bin", "sat-agent"))
	return len(fs) > 0 || err == nil
}

// setup: make this machine a node for PROJECT. Safe to rerun. It installs no software: it lists
// what is missing and the commands that would install it; with --install it shows them (apt ones
// simulated, never removing anything), asks, and runs them. Nothing starts until `sat start`.
func setup(a []string) int {
	inst, a := flag(a, "--install")
	if len(a) < 2 || len(a) > 3 {
		return fail("usage: sat setup [--install] HUB_IP PROJECT [HUB_USER]")
	}
	ip, proj, user := a[0], a[1], os.Getenv("USER")
	if len(a) == 3 {
		user = a[2]
	}
	name := conf.ShortHost()
	if !conf.NameOK(name) {
		return fail("this hostname (" + name + ") can't be a node name: letters, digits, . _ - only")
	}
	if !conf.NameOK(proj) || !conf.NameOK(user) || !conf.NameOK(ip) && !strings.Contains(ip, ":") {
		return fail("usage: sat setup [--install] HUB_IP PROJECT [HUB_USER]")
	}
	if bashLayout() {
		return fail("this node was set up by the bash scripts (~/sat): run `sat migrate` first")
	}
	alias := "sat-hub-" + ip
	base := conf.Dir(proj)
	p := &conf.Project{Name: proj, Base: base, Bundle: filepath.Join(base, "bundle"), State: filepath.Join(base, "state"),
		Conf: map[string]string{"HUB": alias, "HUB_IP": ip, "HUB_USER": user, "SAT_NAME": name}}
	if old, err := conf.Load(proj); err == nil {
		p.Conf = old.Conf
	}

	say("key for the hub")
	if _, err := exec.LookPath("ssh"); err != nil {
		return fail("install an OpenSSH client first")
	}
	os.MkdirAll(filepath.Join(conf.Home(), ".ssh"), 0o700)
	if err := ensureKey(name); err != nil {
		return fail("key: " + err.Error())
	}
	if err := addAlias(sshConfig(), alias, ip, user, conf.Key()); err != nil {
		return fail("~/.ssh/config: " + err.Error())
	}
	c := hub.For(p)
	defer c.Close()
	if reach(c, proj) != nil {
		fmt.Printf("Adding the node key to the hub's authorized_keys as node %s, through your key login as %s@%s.\n", name, user, ip)
		pub, _ := os.Open(conf.Key() + ".pub")
		cmd := exec.Command("ssh", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "ConnectTimeout=10",
			user+"@"+ip, "n="+name+"; "+addKey)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = pub, os.Stdout, os.Stderr
		err := cmd.Run()
		pub.Close()
		if err != nil {
			k, _ := os.ReadFile(conf.Key() + ".pub")
			fmt.Printf("Could not log in to %s@%s with a key. Either set up key login first (docs/node.md), or\n", user, ip)
			fmt.Println("add this line to ~/.ssh/authorized_keys on the hub yourself, then rerun:")
			fmt.Printf("command=\"/home/%s/.local/bin/sat-ssh-gate %s\",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty %s\n", user, name, strings.TrimSpace(string(k)))
			return 1
		}
		c.Close()
		if err := reach(c, proj); err != nil {
			// Native SSH can't get through where the system ssh can (an agent, a jump host, ...).
			p.Conf["TRANSPORT"] = "exec"
			c = hub.For(p)
			if err2 := reach(c, proj); err2 != nil {
				return fail(fmt.Sprintf("the hub refuses (is %s set up there?): %v", proj, err))
			}
			fmt.Printf("(connecting through the system ssh: %v)\n", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if _, err := hub.CheckProtocol(ctx, c); err != nil {
		fmt.Printf("warning: %v. This node won't take jobs from it until that is fixed.\n", err)
	}
	cancel()
	fmt.Println("hub reachable")

	say("the sat command and service (in your home directory)")
	if err := installSelf(); err != nil {
		return fail("install: " + err.Error())
	}
	os.MkdirAll(p.State, 0o755)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	_, err := bundle.Sync(ctx, c, proj, base, nil)
	cancel()
	if err != nil {
		return fail("bundle: " + err.Error())
	}
	cf := filepath.Join(base, "config")
	if _, err := os.Stat(cf); err != nil {
		var b strings.Builder
		fmt.Fprintf(&b, "# %s on this node (read at each start and by each job).\nHUB=%s\nHUB_IP=%s\nHUB_USER=%s\n", proj, alias, ip, user)
		fmt.Fprintf(&b, "SAT_NAME=%s   # bound to this node's key on the hub: change it there too\n", name)
		if p.Conf["TRANSPORT"] == "exec" {
			b.WriteString("TRANSPORT=exec   # the system ssh, not sat's own connection\n")
		}
		if d, err := os.ReadFile(filepath.Join(p.Bundle, "sat", "defaults")); err == nil {
			b.Write(d)
		} else {
			b.WriteString("SLOTS=1\n")
		}
		os.WriteFile(cf, []byte(b.String()), 0o644)
	}
	if p, err = conf.Load(proj); err != nil {
		return fail(err.Error())
	}
	fmt.Printf("%s, %s\n", tilde(conf.Exe()), tilde(unitPath()))

	say("what " + proj + " needs on this node")
	env := agent.Env(p)
	if steps := plan.Of(p.Bundle, env); len(steps) > 0 {
		if inst {
			if !plan.Install(os.Stdout, os.Stdin, tty(), steps, logInstall(proj)) {
				fmt.Println("Rerun this when it is done.")
				return 1
			}
		} else {
			plan.Check(os.Stdout, steps, proj)
			fmt.Println("\nRerun sat setup once they are installed.")
			return 1
		}
		if steps = plan.Of(p.Bundle, env); len(steps) > 0 {
			fmt.Print("Still missing:\n" + plan.Show(steps))
			return 1
		}
	}
	fmt.Println("nothing missing")
	if _, err := os.Stat(filepath.Join(p.Bundle, "sat", "setup")); err == nil {
		say(proj + "'s own setup")
		if code := projectSetup(p); code != 0 {
			return code
		}
	}

	say("done")
	if t, _ := os.Readlink(localSat()); t != conf.Exe() {
		fmt.Println("(add ~/.sat/bin to PATH for the sat command)")
	} else if !strings.Contains(":"+os.Getenv("PATH")+":", ":"+filepath.Dir(localSat())+":") {
		fmt.Println("(add ~/.local/bin to PATH for the sat command)")
	}
	if out, _ := exec.Command("loginctl", "show-user", os.Getenv("USER"), "-p", "Linger").Output(); !strings.Contains(string(out), "yes") {
		fmt.Println("(optional: 'loginctl enable-linger' keeps the service running while you are logged out)")
	}
	fmt.Printf("  sat %s selftest      # the project's check of this node\n  sat %s start         # take jobs;  sat stop | drain | status | logs\n  sat %s autostart on  # optional: start at boot\n  sat                     # the live view\nSettings: %s\n", proj, proj, proj, tilde(cf))
	return 0
}
