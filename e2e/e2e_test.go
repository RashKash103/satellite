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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/RashKash103/satellite/internal/cli"
	"github.com/RashKash103/satellite/internal/conf"
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
		if args, ok := h.authorized(k); ok {
			return &ssh.Permissions{Extensions: map[string]string{"gate": args}}, nil
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if n, ok := h.keys[string(k.Marshal())]; ok {
			return &ssh.Permissions{Extensions: map[string]string{"gate": n}}, nil
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

// authorized: as sshd, a line for the key in the hub's authorized_keys decides the gate's arguments
// (what follows sat-ssh-gate in its forced command). Without one, the key's binding in h.keys does.
func (h *hub) authorized(k ssh.PublicKey) (string, bool) {
	b, _ := os.ReadFile(filepath.Join(h.home, ".ssh", "authorized_keys"))
	for len(b) > 0 {
		pk, _, opts, rest, err := ssh.ParseAuthorizedKey(b)
		if err != nil {
			return "", false
		}
		b = rest
		if !bytes.Equal(pk.Marshal(), k.Marshal()) {
			continue
		}
		for _, o := range opts {
			if c, ok := strings.CutPrefix(o, "command="); ok {
				w := strings.Fields(strings.Trim(c, `"`))
				if len(w) > 0 && strings.HasSuffix(w[0], "sat-ssh-gate") {
					return strings.Join(w[1:], " "), true
				}
			}
		}
	}
	return "", false
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
	gargs := append([]string{h.gate}, strings.Fields(sc.Permissions.Extensions["gate"])...)
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
				cmd := exec.Command("bash", gargs...)
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
	return gateBefore(t, "[ \"$1\" = --protocol ] && { "+protocol+"; }")
}

// gateBefore: a copy of the gate beside a sat-hub that runs script before the real one.
func gateBefore(t *testing.T, script string) string {
	d := t.TempDir()
	b, _ := os.ReadFile(realGate())
	os.WriteFile(filepath.Join(d, "sat-ssh-gate"), b, 0o755)
	os.WriteFile(filepath.Join(d, "sat-hub"), []byte(fmt.Sprintf("#!/usr/bin/env bash\n%s\nexec %s \"$@\"\n", script, filepath.Join(root(), "hub", "sat-hub"))), 0o755)
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
case "$2" in is-active) [ -n "$FAKE_ACTIVE" ] && { echo active; exit 0; }; echo inactive; exit 3 ;; is-enabled) echo "${FAKE_ENABLED:-disabled}"; [ -n "$FAKE_ENABLED" ] ;; esac
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
	os.WriteFile(filepath.Join(base, "config"), []byte(fmt.Sprintf("HUB=sat-hub-127.0.0.1\nHUB_IP=127.0.0.1\nHUB_PORT=%s\nHUB_USER=hub\nSAT_NAME=%s   # bound to this node's key\nSLOTS=2\nMIN_FREE_GB=0\nMIN_FREE_PCT=0\n%s", port, name, config)), 0o644)
	return n
}

// unbind: the key reaches the hub only through a line in its authorized_keys.
func (n *node) unbind() {
	k, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(n.pub))
	n.h.mu.Lock()
	delete(n.h.keys, string(k.Marshal()))
	n.h.mu.Unlock()
}

// call runs one command on the hub with the node's key, as the node's own calls do.
func (n *node) call(cmd string) (string, int) {
	n.t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(read(filepath.Join(n.home, ".ssh", "sat_ed25519"))))
	if err != nil {
		n.t.Fatal(err)
	}
	c, err := ssh.Dial("tcp", n.h.addr, &ssh.ClientConfig{User: "hub", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(n.h.host)})
	if err != nil {
		return err.Error(), 255
	}
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		n.t.Fatal(err)
	}
	defer s.Close()
	out, err := s.CombinedOutput(cmd)
	var x *ssh.ExitError
	if errors.As(err, &x) {
		return string(out), x.ExitStatus()
	}
	if err != nil {
		return string(out) + err.Error(), 255
	}
	return string(out), 0
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

// written: hello's ok hook has finished writing the done file (it creates it, then writes it).
func written(p string) bool { return strings.Contains(read(p), " (by ") }

func TestJobsPassFailCrashDrain(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "")
	h.queue("good", "1000")
	h.queue("bad", "oops")
	h.queue("crashy", "crash")
	os.WriteFile(n.state("hold"), nil, 0o644) // hello's sat/ready holds the lanes back while it exists
	a := n.agent()
	d := h.hello
	time.Sleep(3 * time.Second)
	if es, _ := os.ReadDir(d + "/queue"); len(es) != 3 {
		t.Fatal("claimed although sat/ready said not now")
	}
	os.Remove(n.state("hold"))
	waitFor(t, "three jobs to finish", 60*time.Second, func() bool {
		return written(d+"/done/good") && written(d+"/done/crashy") && exists(d+"/failed/bad")
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
	var pid, pg int
	fmt.Sscan(read(n.state("pid-long")), &pid)
	st := read(fmt.Sprintf("/proc/%d/stat", pid))
	fmt.Sscan(st[strings.LastIndexByte(st, ')')+2:], new(string), new(int), &pg) // state ppid pgrp
	if pg == 0 || syscall.Kill(-pg, 0) != nil {
		t.Fatalf("the job's process group: %q", st)
	}
	os.Remove(filepath.Join(h.conf, "leases", "hello", "long")) // as if reaped and taken by another node
	waitFor(t, "the lease loss", 20*time.Second, func() bool { return strings.Contains(a.out.String(), "lease on long lost: stopping it") })
	waitFor(t, "the job's process group to go", 10*time.Second, func() bool { return syscall.Kill(-pg, 0) != nil && syscall.Kill(pid, 0) != nil })
	waitFor(t, "the lane to be free", 10*time.Second, func() bool { return !exists(n.state("slot1.claim")) })
}

func TestDiskFloor(t *testing.T) {
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, "nodeA", "MIN_FREE_PCT=100\n") // later lines win: keep the whole disk free
	h.queue("j1", "10")
	a := n.agent()
	waitFor(t, "the refusal", 20*time.Second, func() bool { return strings.Contains(a.out.String(), "not taking work:") })
	if !exists(n.state("cleaned")) || !strings.Contains(a.out.String(), "low on disk: ran sat/clean") {
		t.Error("sat/clean did not run first")
	}
	if !exists(h.hello + "/queue/j1") {
		t.Error("claimed with no room")
	}
	if strings.Count(a.out.String(), "not taking work:") != 1 {
		t.Error("said it more than once")
	}
	out, _ := n.sat("hello", "status")
	if !strings.Contains(out, "(not taking work: low on room)") || !strings.Contains(out, "keeps ") {
		t.Errorf("status:\n%s", out)
	}
}

func TestDiskNearlyFullStopsTheJob(t *testing.T) {
	job := "#!/usr/bin/env bash\necho $$ > \"$SAT_BASE/state/pid-$1\"\nsleep 300 & wait\n"
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": job}), realGate())
	n := newNode(t, h, "nodeA", "SLOTS=1\n")
	h.queue("long", "1")
	a := n.agent()
	waitFor(t, "the job to start", 20*time.Second, func() bool { return exists(n.state("pid-long")) })
	f, _ := os.OpenFile(filepath.Join(n.base(), "config"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("MIN_FREE_GB=100000000\n")
	f.Close()
	waitFor(t, "the job to be stopped", 20*time.Second, func() bool { return strings.Contains(a.out.String(), "disk nearly full") })
	waitFor(t, "the job back in the hub's queue", 20*time.Second, func() bool { return exists(h.hello + "/queue/long") })
	if len(h.leases()) != 0 {
		t.Error("lease left")
	}
}

func TestEnvReachesJobs(t *testing.T) {
	job := "#!/usr/bin/env bash\necho \"foo=$FOO bar=$BAR slots=$SLOTS path=${PATH%%:*}\" > \"$SAT_RESULT\"\n"
	h := newHub(t, helloCopy(t, map[string]string{"sat/job": job}), realGate())
	n := newNode(t, h, "nodeA", "")
	os.WriteFile(filepath.Join(n.base(), "env"), []byte("export FOO=\"$HOME/.x\"\n"), 0o644)
	if out, err := n.sat("hello", "env", "BAR=a b"); err != nil || !strings.Contains(out, "next job") {
		t.Fatal(out, err)
	}
	if out, _ := n.sat("hello", "env"); !strings.Contains(out, "export BAR='a b'") || !strings.Contains(out, "FOO=") {
		t.Fatal(out)
	}
	h.queue("j1", "1")
	n.agent()
	waitFor(t, "the job", 30*time.Second, func() bool { return written(h.hello + "/done/j1") })
	want := "foo=" + n.home + "/.x bar=a b slots=2 path=" + n.home + "/.sat/bin"
	if s := read(h.hello + "/done/j1"); !strings.Contains(s, want) {
		t.Errorf("job saw %q, want %q", s, want)
	}
}

// Several projects on one node are its modes: each its own lanes, env and service, one key.
func TestTwoProjectsOneNode(t *testing.T) {
	name := conf.ShortHost()
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, name, "SLOTS=1\n")
	other := filepath.Join(h.dir, "hello2")
	os.MkdirAll(other+"/queue", 0o755)
	w := filepath.Join(h.dir, "hooks2")
	os.WriteFile(w, []byte("#!/bin/sh\nHELLO_DIR="+other+" exec "+hooks()+" \"$@\"\n"), 0o755)
	os.WriteFile(filepath.Join(h.conf, "projects", "hello2"), []byte("HOOKS="+w+"\nLEASE=600\n"), 0o644)
	_, port, _ := net.SplitHostPort(h.addr)
	os.WriteFile(filepath.Join(n.home, ".sat", "config"), []byte("HUB_PORT="+port+"\n"), 0o644)
	key := read(filepath.Join(n.home, ".ssh", "sat_ed25519"))
	out, err := n.sat("setup", "127.0.0.1", "hello2", "hub")
	if err != nil {
		t.Fatalf("second setup: %v\n%s", err, out)
	}
	if read(filepath.Join(n.home, ".ssh", "sat_ed25519")) != key || strings.Count(read(filepath.Join(n.home, ".ssh", "config")), "Host sat-hub-127.0.0.1") != 1 {
		t.Error("the second project did not reuse the key and alias")
	}
	c2 := filepath.Join(n.home, ".sat", "projects", "hello2", "config")
	if c := read(c2); !strings.Contains(c, "SLOTS=2") || !strings.Contains(c, "SAT_NAME="+name) {
		t.Errorf("hello2 config:\n%s", c)
	}
	f, _ := os.OpenFile(c2, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("MIN_FREE_GB=0\nMIN_FREE_PCT=0\n")
	f.Close()
	h.queue("a1", "5")
	os.WriteFile(other+"/queue/b1", []byte("6\n"), 0o644)
	n.agent()
	r := &running{out: &syncBuf{}, done: make(chan error, 1)}
	r.cmd = exec.Command(filepath.Join(n.home, ".sat", "bin", "sat"), "agent", "hello2")
	r.cmd.Env, r.cmd.Stdout, r.cmd.Stderr = n.env(), r.out, r.out
	r.cmd.Start()
	t.Cleanup(func() { r.cmd.Process.Signal(syscall.SIGTERM); r.cmd.Wait() })
	waitFor(t, "a job from each project", 30*time.Second, func() bool { return exists(h.hello+"/done/a1") && exists(other+"/done/b1") })
	waitFor(t, "both agents' state", 10*time.Second, func() bool {
		return strings.Contains(read(filepath.Join(n.home, ".sat", "projects", "hello2", "state", "agent.json")), `"slot": 2`)
	})
	if s := read(n.state("agent.json")); strings.Contains(s, `"slot": 2`) {
		t.Error("hello took hello2's SLOTS")
	}
	out, _ = n.sat("status")
	if !strings.Contains(out, "hello:") || !strings.Contains(out, "hello2:") {
		t.Errorf("status of all projects:\n%s", out)
	}
	if out, _ := n.sat("check"); !strings.Contains(out, "which project?") {
		t.Errorf("a project command without a project: %s", out)
	}
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
	if !strings.Contains(a.out.String(), "this node is missing what hello needs, so not starting: apt sat-test-pkg") || !exists(h.hello+"/queue/j1") {
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

// A key line may list the projects it serves: other projects' sat-hub, repositories and directories
// stay shut to it, a second `sat setup` adds its project to the line (through the user's own login,
// a shim here), and `sat uninstall PROJECT` has the hub drop it again; the last uninstall removes the
// line. Other lines, a legacy one among them, are never touched.
func TestKeysLimitedToProjects(t *testing.T) {
	name := conf.ShortHost()
	h := newHub(t, hooks(), realGate())
	n := newNode(t, h, name, "SLOTS=1\n")
	n.unbind()
	pub := n.pub + " sat@" + name
	os.WriteFile(filepath.Join(n.home, ".ssh", "sat_ed25519.pub"), []byte(pub+"\n"), 0o644)
	other := filepath.Join(h.dir, "hello2")
	os.MkdirAll(other+"/queue", 0o755)
	repo := filepath.Join(h.dir, "repo2.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", repo).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	w := filepath.Join(h.dir, "hooks2")
	os.WriteFile(w, []byte("#!/bin/sh\nHELLO_DIR="+other+" exec "+hooks()+" \"$@\"\n"), 0o755)
	os.WriteFile(filepath.Join(h.conf, "projects", "hello2"), []byte("HOOKS="+w+"\nLEASE=600\nGIT_REPOS="+repo+"\nRSYNC_DIRS="+other+"\n"), 0o644)
	gl := filepath.Join(h.home, ".local", "bin")
	os.MkdirAll(gl, 0o755)
	os.Symlink(realGate(), filepath.Join(gl, "sat-ssh-gate"))
	ak := filepath.Join(h.home, ".ssh", "authorized_keys")
	os.MkdirAll(filepath.Dir(ak), 0o700)
	others := "ssh-ed25519 AAAAC3own someone@laptop\ncommand=\"/x/sat-ssh-gate nodeB\",no-pty ssh-ed25519 AAAAC3b sat@nodeB\n"
	opts := ",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty "
	os.WriteFile(ak, []byte(others+"command=\""+gl+"/sat-ssh-gate "+name+" hello\""+opts+pub+"\n"), 0o600)
	shut := func(cmd string) {
		t.Helper()
		if out, rc := n.call(cmd); rc == 0 || !strings.Contains(out, "refused") {
			t.Errorf("%s with a key for hello: exit %d\n%s", cmd, rc, out)
		}
	}
	open := func(cmd string) {
		t.Helper()
		if out, _ := n.call(cmd); strings.Contains(out, "refused") {
			t.Errorf("%s refused:\n%s", cmd, out)
		}
	}
	if out, rc := n.call("sat-hub hello status"); rc != 0 {
		t.Fatalf("its own project: exit %d\n%s", rc, out)
	}
	shut("sat-hub hello2 status")
	shut("git-upload-pack '" + repo + "'")
	shut("rsync --server --sender -logDtpre.iLsfxCIvu . " + other)
	if l := read(filepath.Join(h.conf, "gate.log")); !strings.Contains(l, "refused ("+name+", key for hello): sat-hub hello2 status") {
		t.Errorf("gate.log:\n%s", l)
	}

	// sat setup for hello2: its key can't reach hello2, so it adds hello2 through the user's login.
	shim := filepath.Join(n.home, "shim")
	os.MkdirAll(shim, 0o755)
	os.WriteFile(filepath.Join(shim, "ssh"), []byte(fmt.Sprintf(`#!/usr/bin/env bash
while [ $# -gt 0 ]; do case $1 in -o|-i|-p|-l) shift 2 ;; -*) shift ;; *) break ;; esac; done
shift; HOME=%s exec bash -c "$*"
`, h.home)), 0o755)
	n.extraEnv = []string{"PATH=" + shim + ":" + os.Getenv("PATH")}
	_, port, _ := net.SplitHostPort(h.addr)
	os.WriteFile(filepath.Join(n.home, ".sat", "config"), []byte("HUB_PORT="+port+"\n"), 0o644)
	out, err := n.sat("setup", "127.0.0.1", "hello2", "hub")
	if err != nil || !strings.Contains(out, "Adding the node key") {
		t.Fatalf("setup hello2: %v\n%s", err, out)
	}
	both := others + "command=\"" + gl + "/sat-ssh-gate " + name + " hello,hello2\"" + opts + pub + "\n"
	if read(ak) != both {
		t.Fatalf("authorized_keys after setup:\n%s", read(ak))
	}
	open("sat-hub hello2 status")
	open("git-upload-pack '" + repo + "'")
	out, _ = n.call("sat-hub --nodes")
	if !strings.Contains(out, "hello2") || !strings.Contains(out, "hello,hello2") || strings.Contains(out, "nodeB") {
		t.Errorf("its own rows:\n%s", out)
	}
	if out, _ = n.sat("setup", "127.0.0.1", "hello2", "hub"); read(ak) != both {
		t.Fatalf("a second setup changed the line:\n%s\n%s", read(ak), out)
	}

	out, err = n.sat("uninstall", "--yes", "hello2")
	if err != nil || !strings.Contains(out, "drop hello2 from this node's key") || !strings.Contains(out, "dropped hello2") {
		t.Fatalf("uninstall hello2: %v\n%s", err, out)
	}
	if want := others + "command=\"" + gl + "/sat-ssh-gate " + name + " hello\"" + opts + pub + "\n"; read(ak) != want {
		t.Fatalf("authorized_keys after uninstall hello2:\n%s", read(ak))
	}
	shut("sat-hub hello2 status")
	if out, err = n.sat("uninstall", "--yes"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if read(ak) != others {
		t.Errorf("authorized_keys after the last uninstall:\n%s", read(ak))
	}
}

// MAX_PER_NODE in the project file: a node with two lanes holds one job at a time; the idle lane
// gets nothing, which is not an error. sat-hub PROJECT nodes shows it (its key is bound by the test
// server, not by a line in authorized_keys: "none").
func TestHubLimitsJobsPerNode(t *testing.T) {
	job := "#!/usr/bin/env bash\ntouch \"$SAT_BASE/state/ran-$1\"\nsleep 4\n"
	hk := helloCopy(t, map[string]string{"sat/job": job})
	h := newHub(t, hk, realGate())
	f, _ := os.OpenFile(filepath.Join(h.conf, "projects", "hello"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("MAX_PER_NODE=1\n")
	f.Close()
	n := newNode(t, h, "nodeA", "SLOTS=2\n")
	h.queue("j1", "1")
	h.queue("j2", "1")
	a := n.agent()
	waitFor(t, "a job to start", 20*time.Second, func() bool { return len(h.leases()) > 0 })
	c := exec.Command("bash", filepath.Join(root(), "hub", "sat-hub"), "hello", "nodes")
	c.Env = h.env()
	out, _ := c.Output()
	if !regexp.MustCompile(`(?m)^hello +nodeA +\d+s +1 +none$`).Match(out) {
		t.Errorf("nodes:\n%s", out)
	}
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if l := h.leases(); len(l) > 1 {
			t.Fatalf("two leases at once: %v", l)
		}
	}
	waitFor(t, "both jobs to pass", 40*time.Second, func() bool { return exists(h.hello+"/done/j1") && exists(h.hello+"/done/j2") })
	if strings.Contains(a.out.String(), "unreachable") {
		t.Errorf("an empty claim was taken for an error:\n%s", a.out.String())
	}
}

// A hub older than project lists has no --nodes, and its --forget ignores a project and removes the
// whole line: `sat uninstall PROJECT` with other projects left on that hub must not ask it.
func TestUninstallOneProjectOldHub(t *testing.T) {
	h := newHub(t, hooks(), gateBefore(t, `case "$1" in --nodes) echo "no project --nodes" >&2; exit 2 ;; --forget) shift $#; set -- --forget ;; esac`))
	n := newNode(t, h, "nodeA", "")
	other := filepath.Join(n.home, ".sat", "projects", "hello2")
	os.MkdirAll(filepath.Join(other, "state"), 0o755)
	os.WriteFile(filepath.Join(other, "config"), []byte(read(filepath.Join(n.base(), "config"))), 0o644)
	os.WriteFile(filepath.Join(h.conf, "projects", "hello2"), []byte("HOOKS="+hooks()+"\nLEASE=600\n"), 0o644)
	ak := filepath.Join(h.home, ".ssh", "authorized_keys")
	os.MkdirAll(filepath.Dir(ak), 0o700)
	line := "command=\"/x/sat-ssh-gate nodeA hello,hello2\",no-pty " + n.pub + "\n"
	os.WriteFile(ak, []byte(line), 0o600)
	out, err := n.sat("uninstall", "--yes", "hello2")
	if err != nil || !strings.Contains(out, "predates keys limited to projects") {
		t.Fatalf("%v\n%s", err, out)
	}
	if read(ak) != line {
		t.Errorf("authorized_keys:\n%s", read(ak))
	}
}
