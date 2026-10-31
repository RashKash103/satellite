// Package hub runs `sat-hub PROJECT CMD ARGS` on a project's hub, as sat-ssh-gate expects it.
// Two transports: native (one SSH connection held open per hub, a session per call, reconnecting
// with backoff) and exec (the system ssh with the ~/.ssh/config alias, as the bash node did).
package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Protocol is the version of the node-hub conversation this node speaks (`sat-hub --protocol`).
const Protocol = "1"

// Caller runs sat-hub ARGS on the hub. A transport failure returns 255 and an error.
type Caller interface {
	Call(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) (int, error)
	Gen() uint64 // changes with every new connection (always 0 for exec)
	Name() string
	Close() error
}

// For picks the project's transport: TRANSPORT=exec, or native when HUB_IP is known.
func For(p *conf.Project) Caller {
	ip := p.Conf["HUB_IP"]
	if p.Get("TRANSPORT", "native") == "exec" || ip == "" {
		return &Exec{Alias: p.Conf["HUB"]}
	}
	port := p.Get("HUB_PORT", "22")
	user := p.Conf["HUB_USER"]
	if user == "" {
		user = os.Getenv("USER")
	}
	return &Native{Addr: net.JoinHostPort(ip, port), User: user, Key: conf.Key(), Alias: p.Conf["HUB"]}
}

// Out runs a call and returns its stdout.
func Out(ctx context.Context, c Caller, args ...string) (string, int, error) {
	var b strings.Builder
	var e strings.Builder
	rc, err := c.Call(ctx, nil, &b, &e, args...)
	if err == nil && rc != 0 && e.Len() > 0 {
		err = fmt.Errorf("exit %d: %s", rc, strings.TrimSpace(e.String()))
	}
	return b.String(), rc, err
}

// CheckProtocol asks the hub which protocol it speaks. A hub older than the Go node answers
// nothing (its sat-hub has no --protocol).
func CheckProtocol(ctx context.Context, c Caller) (string, error) {
	out, rc, err := Out(ctx, c, "--protocol")
	if rc == 255 {
		return "", err
	}
	v := strings.TrimSpace(out)
	if rc != 0 || v == "" {
		return "", ProtocolError{"the hub's sat-hub does not state a protocol (it predates protocol " + Protocol + ": update the hub's satellite checkout)"}
	}
	if v != Protocol {
		return v, ProtocolError{"the hub speaks protocol " + v + ", this node speaks " + Protocol + ": update whichever is older"}
	}
	return v, nil
}

// ProtocolError: the hub answered, in a protocol this node doesn't speak.
type ProtocolError struct{ Msg string }

func (e ProtocolError) Error() string { return e.Msg }

// Exec runs the system ssh.
type Exec struct{ Alias string }

func (e *Exec) Name() string { return e.Alias }
func (e *Exec) Gen() uint64  { return 0 }
func (e *Exec) Close() error { return nil }
func (e *Exec) Call(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) (int, error) {
	if e.Alias == "" {
		return 255, errors.New("no HUB in the project's config")
	}
	a := append([]string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30", e.Alias, "sat-hub"}, args...)
	cmd := exec.CommandContext(ctx, "ssh", a...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	var x *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &x) && x.ExitCode() != 255 && x.ExitCode() >= 0:
		return x.ExitCode(), nil
	}
	return 255, fmt.Errorf("ssh %s: %v", e.Alias, err)
}

// Native holds one connection open, with keepalives; a dead one is dropped and redialled.
type Native struct {
	Addr, User, Key, Alias string

	mu    sync.Mutex
	c     *ssh.Client
	gen   uint64
	fails int
	next  time.Time
	last  error
}

func (n *Native) Name() string {
	if n.Alias != "" {
		return n.Alias
	}
	return n.Addr
}

func (n *Native) Gen() uint64 { n.mu.Lock(); defer n.mu.Unlock(); return n.gen }

func (n *Native) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.c != nil {
		n.c.Close()
		n.c = nil
	}
	return nil
}

func (n *Native) drop(c *ssh.Client) {
	n.mu.Lock()
	if n.c == c {
		n.c = nil
	}
	n.mu.Unlock()
	c.Close()
}

func knownHostsFiles() []string {
	var fs []string
	for _, f := range []string{filepath.Join(conf.Home(), ".ssh", "known_hosts"), "/etc/ssh/ssh_known_hosts"} {
		if _, err := os.Stat(f); err == nil {
			fs = append(fs, f)
		}
	}
	return fs
}

func (n *Native) dial() (*ssh.Client, error) {
	pem, err := os.ReadFile(n.Key)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", n.Key, err)
	}
	files := knownHostsFiles()
	if len(files) == 0 {
		return nil, errors.New("no known_hosts: log in to the hub once with ssh to accept its host key")
	}
	hk, err := knownhosts.New(files...)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{User: n.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hk, Timeout: 15 * time.Second}
	c, err := ssh.Dial("tcp", n.Addr, cfg)
	var ke *knownhosts.KeyError
	if errors.As(err, &ke) && len(ke.Want) > 0 {
		// The hub offered a key type we have no line for; ask for the ones known_hosts holds.
		for _, w := range ke.Want {
			t := w.Key.Type()
			if t == ssh.KeyAlgoRSA {
				cfg.HostKeyAlgorithms = append(cfg.HostKeyAlgorithms, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
			}
			cfg.HostKeyAlgorithms = append(cfg.HostKeyAlgorithms, t)
		}
		c, err = ssh.Dial("tcp", n.Addr, cfg)
	}
	return c, err
}

func (n *Native) client() (*ssh.Client, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.c != nil {
		return n.c, nil
	}
	if time.Now().Before(n.next) {
		return nil, n.last
	}
	c, err := n.dial()
	if err != nil {
		n.fails++
		n.next = time.Now().Add(min(time.Second<<min(n.fails, 6), 60*time.Second))
		n.last = fmt.Errorf("%s: %v", n.Name(), err)
		return nil, n.last
	}
	n.fails, n.c = 0, c
	n.gen++
	go n.keepalive(c)
	return c, nil
}

func (n *Native) keepalive(c *ssh.Client) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		r := make(chan error, 1)
		go func() { _, _, err := c.SendRequest("keepalive@openssh.com", true, nil); r <- err }()
		select {
		case err := <-r:
			if err == nil {
				continue
			}
		case <-time.After(30 * time.Second):
		}
		n.drop(c)
		return
	}
}

func (n *Native) Call(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) (int, error) {
	var s *ssh.Session
	for try := 0; ; try++ {
		c, err := n.client()
		if err != nil {
			return 255, err
		}
		if s, err = c.NewSession(); err == nil {
			break
		}
		n.drop(c) // a stale connection: one fresh attempt
		if try > 0 {
			return 255, err
		}
	}
	defer s.Close()
	s.Stdin, s.Stdout, s.Stderr = stdin, stdout, stderr
	done := make(chan error, 1)
	go func() { done <- s.Run(strings.Join(append([]string{"sat-hub"}, args...), " ")) }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		s.Close()
		return 255, ctx.Err()
	}
	var x *ssh.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &x):
		return x.ExitStatus(), nil
	}
	return 255, fmt.Errorf("%s: %v", n.Name(), err)
}
