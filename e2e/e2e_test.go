// End to end: the real bash hub (hub/sat-hub, hub/sat-ssh-gate, examples/hello) behind an
// in-process SSH server that runs the gate for each command, as sshd does for a node key (the key
// decides the node name), and the Go node, with its own HOME, against it. systemctl is a fake that
// logs what it is asked: the test never touches this machine's services.
package e2e

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/cli"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// The test binary doubles as sat (and sat-call) when the agent runs it.
func TestMain(m *testing.M) {
	if os.Getenv("SAT_E2E_EXEC") == "1" {
		os.Exit(cli.Main("dev", os.Args))
	}
	os.Exit(m.Run())
}

func root() string {
	_, f, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(f))
}

type hub struct {
	t                      *testing.T
	dir, home, conf, hello string
	gate                   string // the gate the server runs
	addr                   string
	host                   ssh.PublicKey
	mu                     sync.Mutex
	keys                   map[string]string // marshalled public key -> node name
}

// newHub sets up a hub with the hello project. hooks is the hello example to use (a copy, when a
// test changes its bundle); gate is the gate to run (a copy, when a test changes sat-hub).
func newHub(t *testing.T, hooks, gate string) *hub {
	d := t.TempDir()
	h := &hub{t: t, dir: d, home: filepath.Join(d, "hubhome"), conf: filepath.Join(d, "conf"), hello: filepath.Join(d, "hello"), gate: gate, keys: map[string]string{}}
	os.MkdirAll(filepath.Join(h.conf, "projects"), 0o755)
	os.MkdirAll(filepath.Join(h.hello, "queue"), 0o755)
	os.MkdirAll(h.home, 0o755)
	os.WriteFile(filepath.Join(h.conf, "projects", "hello"), []byte("HOOKS="+hooks+"\nLEASE=600\n"), 0o644)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	h.host = signer.PublicKey()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if n, ok := h.keys[string(k.Marshal())]; ok {
			return &ssh.Permissions{Extensions: map[string]string{"node": n}}, nil
		}
		return nil, errors.New("unknown key")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	h.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serve(c, cfg)
		}
	}()
	return h
}

func (h *hub) env(extra ...string) []string {
	return append([]string{"HOME=" + h.home, "SAT_CONF=" + h.conf, "HELLO_DIR=" + h.hello, "PATH=" + os.Getenv("PATH")}, extra...)
}

func (h *hub) serve(c net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	node := sc.Permissions.Extensions["node"]
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for r := range creqs {
				if r.Type != "exec" {
					r.Reply(false, nil)
					continue
				}
				var p struct{ Command string }
				ssh.Unmarshal(r.Payload, &p)
				r.Reply(true, nil)
				cmd := exec.Command("bash", h.gate, node)
				cmd.Env = h.env("SSH_ORIGINAL_COMMAND=" + p.Command)
				in, _ := cmd.StdinPipe()
				cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
				code := 0
				if err := cmd.Start(); err != nil {
					code = 127
				} else {
					go func() { io.Copy(in, ch); in.Close() }()
					if err := cmd.Wait(); err != nil {
						code = 1
						var x *exec.ExitError
						if errors.As(err, &x) {
							code = x.ExitCode()
						}
					}
				}
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
				return
			}
		}()
	}
}

func (h *hub) queue(id, v string) {
	os.WriteFile(filepath.Join(h.hello, "queue", id), []byte(v+"\n"), 0o644)
}

func (h *hub) leases() []string {
	es, _ := os.ReadDir(filepath.Join(h.conf, "leases", "hello"))
	var l []string
	for _, e := range es {
		if !strings.HasPrefix(e.Name(), ".") {
			l = append(l, e.Name())
		}
	}
	return l
}

// helloCopy: a copy of examples/hello whose bundle files are replaced or added.
func helloCopy(t *testing.T, files map[string]string) string {
	d := filepath.Join(t.TempDir(), "hello")
	if out, err := exec.Command("cp", "-a", filepath.Join(root(), "examples", "hello"), d).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	for f, body := range files {
		os.WriteFile(filepath.Join(d, "bundle", f), []byte(body), 0o755)
	}
	return filepath.Join(d, "hub-hooks")
}

func realGate() string { return filepath.Join(root(), "hub", "sat-ssh-gate") }
func hooks() string    { return filepath.Join(root(), "examples", "hello", "hub-hooks") }

// gateWith: a copy of the gate beside a sat-hub that answers --protocol with script.
func gateWith(t *testing.T, protocol string) string {
	d := t.TempDir()
	b, _ := os.ReadFile(realGate())
	os.WriteFile(filepath.Join(d, "sat-ssh-gate"), b, 0o755)
	os.WriteFile(filepath.Join(d, "sat-hub"), []byte(fmt.Sprintf("#!/usr/bin/env bash\n[ \"$1\" = --protocol ] && { %s; }\nexec %s \"$@\"\n", protocol, filepath.Join(root(), "hub", "sat-hub"))), 0o755)
	return filepath.Join(d, "sat-ssh-gate")
}

type node struct {
	t          *testing.T
	h          *hub
	home, name string
	pub        string
	extraEnv   []string
	sysLog     string
}

const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$SYSTEMCTL_LOG"
case "$2" in is-active) echo inactive; exit 3 ;; is-enabled) echo "${FAKE_ENABLED:-disabled}"; [ -n "$FAKE_ENABLED" ] ;; esac
exit 0
`

// newNode: a node for the hello project, with its key bound to name on the hub.
func newNode(t *testing.T, h *hub, name, config string) *node {
	n := &node{t: t, h: h, home: filepath.Join(t.TempDir(), "home"), name: name}
	ssh_ := filepath.Join(n.home, ".ssh")
	os.MkdirAll(ssh_, 0o700)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	blk, _ := ssh.MarshalPrivateKey(priv, "sat@"+name)
	os.WriteFile(filepath.Join(ssh_, "sat_ed25519"), pem.EncodeToMemory(blk), 0o600)
	sp, _ := ssh.NewPublicKey(pub)
	n.pub = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	h.mu.Lock()
	h.keys[string(sp.Marshal())] = name
	h.mu.Unlock()
	os.WriteFile(filepath.Join(ssh_, "known_hosts"), []byte(knownhosts.Line([]string{knownhosts.Normalize(h.addr)}, h.host)+"\n"), 0o600)
	os.WriteFile(filepath.Join(ssh_, "config"), []byte("Host other\n  HostName 192.0.2.1\nHost sat-hub-127.0.0.1\n  HostName 127.0.0.1\n  User hub\n"), 0o600)
	bin := filepath.Join(n.home, ".sat", "bin")
	os.MkdirAll(bin, 0o755)
	me, _ := os.Executable()
	b, _ := os.ReadFile(me)
	os.WriteFile(filepath.Join(bin, "sat"), b, 0o755)
	os.Symlink("sat", filepath.Join(bin, "sat-call"))
	fake := filepath.Join(n.home, "systemctl")
	os.WriteFile(fake, []byte(fakeSystemctl), 0o755)
	n.sysLog = filepath.Join(n.home, "systemctl.log")
	base := filepath.Join(n.home, ".sat", "projects", "hello")
	os.MkdirAll(filepath.Join(base, "state"), 0o755)
	_, port, _ := net.SplitHostPort(h.addr)
	os.WriteFile(filepath.Join(base, "config"), []byte(fmt.Sprintf("HUB=sat-hub-127.0.0.1\nHUB_IP=127.0.0.1\nHUB_PORT=%s\nHUB_USER=hub\nSAT_NAME=%s   # bound to this node's key\nSLOTS=2\n%s", port, name, config)), 0o644)
	return n
}

func (n *node) base() string          { return filepath.Join(n.home, ".sat", "projects", "hello") }
func (n *node) state(f string) string { return filepath.Join(n.base(), "state", f) }

func (n *node) env() []string {
	return append([]string{"HOME=" + n.home, "USER=" + n.name, "PATH=" + os.Getenv("PATH"), "SAT_E2E_EXEC=1", "SAT_TEST_FAST=1",
		"SAT_SYSTEMCTL=" + filepath.Join(n.home, "systemctl"), "SYSTEMCTL_LOG=" + n.sysLog, "SAT_JOURNALCTL=true", "SAT_RELEASES_API=http://127.0.0.1:1"}, n.extraEnv...)
}

func (n *node) sat(args ...string) (string, error) {
	c := exec.Command(filepath.Join(n.home, ".sat", "bin", "sat"), args...)
	c.Env = n.env()
	out, err := c.CombinedOutput()
	return string(out), err
}

type running struct {
	cmd  *exec.Cmd
	out  *syncBuf
	done chan error
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func (n *node) agent() *running {
	r := &running{out: &syncBuf{}, done: make(chan error, 1)}
	r.cmd = exec.Command(filepath.Join(n.home, ".sat", "bin", "sat"), "agent", "hello")
	r.cmd.Env = n.env()
	r.cmd.Stdout, r.cmd.Stderr = r.out, r.out
	if err := r.cmd.Start(); err != nil {
		n.t.Fatal(err)
	}
	go func() { r.done <- r.cmd.Wait() }()
	n.t.Cleanup(func() {
		r.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			r.cmd.Process.Kill()
		}
		if n.t.Failed() {
			n.t.Logf("agent log:\n%s", r.out.String())
		}
	})
	return r
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for !f() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func read(p string) string { b, _ := os.ReadFile(p); return string(b) }

func TestJobsPassFailCrashDrain(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "")
	h.queue("good", "1000")
	h.queue("bad", "oops")
	h.queue("crashy", "crash")
	a := n.agent()
	d := h.hello
	waitFor(t, "three jobs to finish", 60*time.Second, func() bool {
		return exists(d+"/done/good") && exists(d+"/done/crashy") && exists(d+"/failed/bad")
	})
	if s := read(d + "/done/good"); !strings.Contains(s, "sha256 ") || !strings.Contains(s, "(by nodeA)") {
		t.Errorf("passing job's result: %q", s)
	}
	if s := read(d + "/failed/bad"); !strings.Contains(s, "failed on nodeA: not a number") || !strings.Contains(s, "the job held: oops") {
		t.Errorf("failing job's reason and summary: %q", s)
	}
	if !strings.Contains(a.out.String(), "crashy: exit 3, handing it back") {
		t.Error("the crashed job was not handed back")
	}
	waitFor(t, "no leases left", 10*time.Second, func() bool { return len(h.leases()) == 0 })
	if !strings.Contains(read(n.state("agent.json")), `"protocol": "1"`) {
		t.Error("agent.json lacks the hub's protocol")
	}
	if !strings.Contains(read(n.state("agent.log")), "nodeA") {
		t.Error("no agent.log")
	}
	out, _ := n.sat("status")
	for _, w := range []string{"hub sat-hub-127.0.0.1: reachable, protocol 1", "disk: "} {
		if !strings.Contains(out, w) {
			t.Errorf("status lacks %q:\n%s", w, out)
		}
	}
	os.WriteFile(n.state("drain"), nil, 0o644)
	select {
	case err := <-a.done:
		if err != nil {
			t.Errorf("drained agent exited with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("drain did not stop the agent")
	}
	if exists(n.state("drain")) {
		t.Error("the drain flag was left behind")
	}
	if !strings.Contains(a.out.String(), "drained: stopping") {
		t.Error("no drain message")
	}
}

func TestLostLeaseKillsTheJob(t *testing.T) {
	job := "#!/usr/bin/env bash\necho $$ > \"$SAT_BASE/state/pid-$1\"\necho \"40% halfway there\" >> \"$SAT_PROGRESS\"\nsleep 300 & wait\n"
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": job}), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=1\n")
	h.queue("long", "1")
	a := n.agent()
	waitFor(t, "the job to start", 20*time.Second, func() bool { return exists(n.state("pid-long")) })
	waitFor(t, "progress in agent.json", 10*time.Second, func() bool {
		s := read(n.state("agent.json"))
		return strings.Contains(s, `"percent": 40`) && strings.Contains(s, `"progress": "halfway there"`)
	})
	var pid int
	fmt.Sscan(read(n.state("pid-long")), &pid)
	os.Remove(filepath.Join(h.conf, "leases", "hello", "long")) // as if reaped and taken by another node
	waitFor(t, "the lease loss", 20*time.Second, func() bool { return strings.Contains(a.out.String(), "lease on long lost: stopping it") })
	waitFor(t, "the job's process group to go", 10*time.Second, func() bool { return syscall.Kill(-pid, 0) != nil })
	waitFor(t, "the lane to be free", 10*time.Second, func() bool { return !exists(n.state("slot1.claim")) })
}

func TestProtocolMismatch(t *testing.T) {
	for name, proto := range map[string]string{"newer hub": "echo 2; exit 0", "older hub": "echo 'no project --protocol' >&2; exit 2"} {
		t.Run(name, func(t *testing.T) {
			h := newHub(t, hooks(), gateWith(t, proto))
			n := newNode(t, h, "nodeA", "")
			h.queue("j1", "10")
			a := n.agent()
			waitFor(t, "the refusal", 20*time.Second, func() bool { return strings.Contains(a.out.String(), "not claiming:") })
			time.Sleep(2 * time.Second)
			if !exists(h.hello + "/queue/j1") {
				t.Fatal("claimed from a hub with another protocol")
			}
			out, _ := n.sat("status")
			if !strings.Contains(out, "reachable, but not claiming") {
				t.Errorf("status:\n%s", out)
			}
		})
	}
}

func TestRequirementGate(t *testing.T) {
	h := newHub(t, helloCopy(t, map[string]string{"sat/requirements": "#!/bin/sh\necho 'apt sat-test-pkg'\n"}), realGate())
	n := newNode(t, h, "nodeA", "")
	h.queue("j1", "10")
	a := n.agent()
	select {
	case err := <-a.done:
		if err != nil {
			t.Errorf("exit %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the agent started although something is missing")
	}
	if !strings.Contains(a.out.String(), "this node is missing what hello needs") || !exists(h.hello+"/queue/j1") {
		t.Fatal(a.out.String())
	}
	out, err := n.sat("hello", "check")
	if err == nil || !strings.Contains(out, "sudo apt-get install --no-remove sat-test-pkg") {
		t.Errorf("check: %v\n%s", err, out)
	}
	out, _ = n.sat("hello", "start")
	if !strings.Contains(out, "missing") || strings.Contains(read(n.sysLog), "start sat@hello") {
		t.Errorf("start went ahead:\n%s", out)
	}
	out, _ = n.sat("hello", "install")
	if !strings.Contains(out, "Not a terminal: nothing was run") && !strings.Contains(out, "apt can't do this") && !strings.Contains(out, "no apt-get") {
		t.Errorf("install without a terminal:\n%s", out)
	}
}

func TestExecTransport(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "TRANSPORT=exec\n")
	// The system ssh, as test/run fakes it: options and host dropped, the rest to the gate.
	shim := filepath.Join(n.home, "shim")
	os.MkdirAll(shim, 0o755)
	os.WriteFile(filepath.Join(shim, "ssh"), []byte(fmt.Sprintf(`#!/usr/bin/env bash
while [ $# -gt 0 ]; do case $1 in -o|-i|-p|-l) shift 2 ;; -*) shift ;; *) break ;; esac; done
shift; HOME=%s SAT_CONF=%s HELLO_DIR=%s SSH_ORIGINAL_COMMAND="$*" exec %s nodeA
`, h.home, h.conf, h.hello, realGate())), 0o755)
	n.extraEnv = []string{"PATH=" + shim + ":" + os.Getenv("PATH")}
	h.queue("good", "100")
	n.agent()
	waitFor(t, "the job to pass", 30*time.Second, func() bool { return exists(h.hello + "/done/good") })
}

func TestUninstallForgetsTheKey(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "")
	ak := filepath.Join(h.home, ".ssh", "authorized_keys")
	os.MkdirAll(filepath.Dir(ak), 0o700)
	others := "ssh-ed25519 AAAAC3own someone@laptop\ncommand=\"/x/sat-ssh-gate nodeB\",no-pty ssh-ed25519 AAAAC3b sat@nodeB\n"
	os.WriteFile(ak, []byte(others+"command=\"/x/sat-ssh-gate nodeA\",no-pty "+n.pub+"\n"), 0o600)
	os.MkdirAll(filepath.Join(n.home, ".sat"), 0o755)
	os.WriteFile(filepath.Join(n.home, ".sat", "installed.log"), []byte("2026-01-01 00:00 hello: sudo apt-get install --no-remove x\n"), 0o644)
	out, err := n.sat("uninstall")
	if err == nil || !strings.Contains(out, "nothing was done") || !exists(n.base()) {
		t.Fatalf("uninstall went ahead without a confirmation:\n%s", out)
	}
	for _, w := range []string{"stop and disable sat@hello.service", "forget this node's key", "remove the node key", "It removes no packages", "apt-get install --no-remove x"} {
		if !strings.Contains(out, w) {
			t.Errorf("the list lacks %q:\n%s", w, out)
		}
	}
	out, err = n.sat("uninstall", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if read(ak) != others {
		t.Errorf("authorized_keys:\n%s", read(ak))
	}
	for _, p := range []string{".sat", ".ssh/sat_ed25519"} {
		if exists(filepath.Join(n.home, p)) {
			t.Errorf("%s left", p)
		}
	}
	if c := read(filepath.Join(n.home, ".ssh", "config")); strings.Contains(c, "sat-hub-127.0.0.1") || !strings.Contains(c, "Host other") {
		t.Errorf("ssh config:\n%s", c)
	}
	if l := read(n.sysLog); !strings.Contains(l, "stop sat@hello.service") || !strings.Contains(l, "disable sat@hello.service") {
		t.Errorf("systemctl calls: %s", l)
	}
}

func TestMigrate(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "")
	n.extraEnv = []string{"FAKE_ENABLED=enabled"}
	old := filepath.Join(n.home, "sat")
	os.MkdirAll(filepath.Join(old, "bin"), 0o755)
	if err := os.Rename(n.base(), filepath.Join(old, "hello")); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(old, "hello", "bundle", "sat"), 0o755)
	os.WriteFile(filepath.Join(old, "hello", "bundle", "sat", "moved"), []byte("#!/bin/sh\necho \"$1 -> $2\" > \"$2/state/moved\"\n"), 0o755)
	for _, f := range []string{"sat-agent", "sat-call", "sat-lib"} {
		b, _ := os.ReadFile(filepath.Join(root(), "node", f))
		os.WriteFile(filepath.Join(old, "bin", f), b, 0o755)
	}
	os.WriteFile(filepath.Join(old, "bin", "mine"), []byte("not ours\n"), 0o755)
	lb := filepath.Join(n.home, ".local", "bin")
	os.MkdirAll(lb, 0o755)
	b, _ := os.ReadFile(filepath.Join(root(), "node", "sat"))
	os.WriteFile(filepath.Join(lb, "sat"), b, 0o755)
	out, err := n.sat("migrate", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if m := read(n.state("moved")); m != old+"/hello -> "+n.base()+"\n" {
		t.Errorf("sat/moved: %q\n%s", m, out)
	}
	for _, f := range []string{"sat-agent", "sat-call", "sat-lib"} {
		if exists(filepath.Join(old, "bin", f)) {
			t.Errorf("%s left", f)
		}
	}
	if !exists(filepath.Join(old, "bin", "mine")) {
		t.Error("removed a file that isn't ours")
	}
	if l, _ := os.Readlink(filepath.Join(lb, "sat")); l != filepath.Join(n.home, ".sat", "bin", "sat") {
		t.Errorf("~/.local/bin/sat: %q", l)
	}
	if u := read(filepath.Join(n.home, ".config", "systemd", "user", "sat@.service")); !strings.Contains(u, "ExecStart=%h/.sat/bin/sat agent %i") {
		t.Errorf("unit:\n%s", u)
	}
	if !strings.Contains(read(n.sysLog), "enable sat@hello.service") {
		t.Error("autostart not restored")
	}
	if out, _ := n.sat("hello", "status"); !strings.Contains(out, "reachable, protocol 1") {
		t.Errorf("status after the move:\n%s", out)
	}
}
