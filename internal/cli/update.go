package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/selfupdate"
)

// fetchVerified downloads the release's binary beside ~/.sat/bin/sat and checks it: the sha256
// against checksums.txt, then the build attestation when gh can.
func fetchVerified(ctx context.Context, rel *selfupdate.Release, say func(string, ...any)) (string, error) {
	path, err := selfupdate.Download(ctx, rel, conf.Exe())
	if err != nil {
		return "", err
	}
	say("checksum: matches the release's checksums.txt")
	checked, why, err := selfupdate.Attest(ctx, path)
	switch {
	case err != nil:
		os.Remove(path)
		return "", err
	case checked:
		say("build attestation: verified (built from %s by its release workflow)", selfupdate.Repo)
	default:
		say("build attestation: not checked (%s); the checksum only shows the download is what the release lists", why)
	}
	return path, nil
}

func running() []string {
	var r []string
	for _, n := range conf.List() {
		if active("sat@" + n + ".service") {
			r = append(r, n)
		}
	}
	return r
}

func selfUpdate(a []string) int {
	yes, a := flag(a, "--yes", "-y")
	if len(a) > 0 {
		return fail("usage: sat self-update [--yes]")
	}
	if _, err := os.Stat(conf.Exe()); err != nil {
		return fail("sat is not installed in ~/.sat/bin (sat setup does that)")
	}
	ctx := context.Background()
	pin := conf.Node()["PIN"]
	rel, err := selfupdate.Fetch(ctx, pin)
	if err != nil {
		return fail("could not get the release: " + err.Error())
	}
	selfupdate.Available(ctx, Version, true)
	if !selfupdate.Wanted(Version, rel.Tag, pin) {
		fmt.Printf("sat %s is current (release: %s%s)\n", Version, rel.Tag, map[bool]string{true: ", pinned in ~/.sat/config", false: ""}[pin != ""])
		return 0
	}
	fmt.Printf("sat %s (you have %s)\n\n%s\n\n", rel.Tag, Version, rel.Body)
	run := running()
	q := "Install it?"
	if len(run) > 0 {
		q = fmt.Sprintf("Install it? Running services (%v) are drained first, then restarted.", run)
	}
	if !yes && !ask(q) {
		return 1
	}
	path, err := fetchVerified(ctx, rel, func(f string, v ...any) { fmt.Printf(f+"\n", v...) })
	if err != nil {
		return fail("not updated: " + err.Error())
	}
	defer os.Remove(path)
	for _, n := range run {
		os.WriteFile(filepath.Join(conf.Dir(n), "state", "drain"), nil, 0o644)
		fmt.Printf("draining %s: waiting for its running jobs to finish\n", n)
	}
	for t := 0; len(running()) > 0; t++ {
		if t > 0 && t%30 == 0 {
			fmt.Printf("still draining: %v\n", running())
		}
		time.Sleep(2 * time.Second)
	}
	if err := selfupdate.Swap(path, conf.Exe()); err != nil {
		return fail("not updated: " + err.Error())
	}
	exec.Command(conf.Exe(), "_unit").Run()
	fmt.Printf("sat is now %s\n", rel.Tag)
	for _, n := range run {
		c := exec.Command(conf.Exe(), n, "start")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		c.Run()
	}
	return 0
}

// autoUpdate (AUTO_UPDATE=on in ~/.sat/config): from the agent, between jobs. Without it the
// agent only notes, at most daily, that a release exists. One agent updates; the others see the
// binary change and restart between jobs too.
func autoUpdate(ctx context.Context, logf func(string, ...any)) bool {
	tag, err := selfupdate.Available(ctx, Version, false)
	if err != nil || tag == "" || conf.Node()["AUTO_UPDATE"] != "on" {
		return false
	}
	l, err := os.OpenFile(filepath.Join(conf.Root(), "update.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer l.Close()
	if syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return false
	}
	if out, _ := exec.Command(conf.Exe(), "version").Output(); string(out) == "sat "+tag+"\n" {
		return false // another agent did it
	}
	rel, err := selfupdate.Fetch(ctx, conf.Node()["PIN"])
	if err != nil || rel.Tag != tag {
		return false
	}
	logf("updating to sat %s (AUTO_UPDATE=on)", tag)
	path, err := fetchVerified(ctx, rel, logf)
	if err == nil {
		err = selfupdate.Swap(path, conf.Exe())
	}
	if err != nil {
		os.Remove(path)
		logf("update to %s failed: %v", tag, err)
		return false
	}
	exec.Command(conf.Exe(), "_unit").Run()
	logf("updated to %s: restarting once the running jobs finish", tag)
	return true
}
