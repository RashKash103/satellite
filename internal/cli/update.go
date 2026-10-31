package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/RashKash103/satellite/internal/conf"
	"github.com/RashKash103/satellite/internal/selfupdate"
	"github.com/RashKash103/satellite/internal/ui"
)

// fetchVerified downloads the release's binary beside ~/.sat/bin/sat and checks it: the sha256
// against checksums.txt, then the build attestation when gh can. say gets each result (ok false:
// not checked); t, when not nil, shows a spinner for the download.
func fetchVerified(ctx context.Context, rel *selfupdate.Release, t *ui.T, say func(ok bool, what, result string)) (string, error) {
	var path string
	var err error
	dl := func() { path, err = selfupdate.Download(ctx, rel, conf.Exe()) }
	if t != nil {
		spin(t, "downloading sat "+rel.Tag+" ("+selfupdate.AssetName()+")", dl)
	} else {
		dl()
	}
	if err != nil {
		return "", err
	}
	say(true, "checksum", "matches the release's checksums.txt")
	var checked bool
	var why string
	at := func() { checked, why, err = selfupdate.Attest(ctx, path) }
	if t != nil {
		spin(t, "checking the build attestation", at)
	} else {
		at()
	}
	switch {
	case err != nil:
		os.Remove(path)
		return "", err
	case checked:
		say(true, "build attestation", fmt.Sprintf("verified (built from %s by its release workflow)", selfupdate.Repo))
	default:
		say(false, "build attestation", fmt.Sprintf("not checked (%s); the checksum only shows the download is what the release lists", why))
	}
	return path, nil
}

// logSay: fetchVerified's results as the log lines they always were.
func logSay(logf func(string, ...any)) func(bool, string, string) {
	return func(_ bool, what, result string) { logf("%s: %s", what, result) }
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
	t := ui.Out()
	var rel *selfupdate.Release
	var err error
	spin(t, "asking GitHub for the release", func() {
		rel, err = selfupdate.Fetch(ctx, pin)
		if err == nil {
			selfupdate.Available(ctx, Version, true)
		}
	})
	if err != nil {
		return fail("could not get the release: " + err.Error())
	}
	if !selfupdate.Wanted(Version, rel.Tag, pin) {
		t.Done("sat %s is current %s", Version, t.Muted("(release: "+rel.Tag+map[bool]string{true: ", pinned in ~/.sat/config", false: ""}[pin != ""]+")"))
		return 0
	}
	if t.Color() {
		t.Println(t.Heading("sat "+rel.Tag) + t.Muted(" (you have "+Version+")"))
		t.Println(t.Rule("release notes"))
		t.Println(t.Markdown(strings.TrimSpace(rel.Body)))
		t.Println(t.Rule(""))
		t.Println()
	} else {
		fmt.Printf("sat %s (you have %s)\n\n%s\n\n", rel.Tag, Version, rel.Body)
	}
	run := running()
	q := "Install it?"
	if len(run) > 0 {
		q = fmt.Sprintf("Install it? Running services (%v) switch to it at once; their running jobs keep running.", run)
	}
	if !yes && !ask(q) {
		return 1
	}
	path, err := fetchVerified(ctx, rel, t, func(ok bool, what, result string) {
		if !t.Color() {
			t.Printf("%s: %s\n", what, result)
		} else if ok {
			t.Println(t.Mark(ui.MarkOK) + ui.Pad(what, 18) + t.OK(result))
		} else {
			t.Println(t.Mark(ui.MarkWarn) + ui.Pad(what, 18) + t.Warn(result))
		}
	})
	if err != nil {
		return fail("not updated: " + err.Error())
	}
	defer os.Remove(path)
	if err := selfupdate.Swap(path, conf.Exe()); err != nil {
		return fail("not updated: " + err.Error())
	}
	exec.Command(conf.Exe(), "_unit").Run()
	t.Done("sat is now %s", t.Bold(rel.Tag))
	if len(run) > 0 {
		t.Println(t.Mark(ui.MarkInfo) + fmt.Sprintf("running services (%s) restart on it within seconds and adopt their running jobs %s",
			strings.Join(run, " "), t.Muted("(sat status shows it)")))
	}
	return 0
}

// autoUpdate (AUTO_UPDATE=on in ~/.sat/config): from the agent. Without it the agent only notes, at
// most daily, that a release exists. One agent updates; it and the others see the binary change,
// re-execute and adopt their running jobs.
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
	path, err := fetchVerified(ctx, rel, nil, logSay(logf))
	if err == nil {
		err = selfupdate.Swap(path, conf.Exe())
	}
	if err != nil {
		os.Remove(path)
		logf("update to %s failed: %v", tag, err)
		return false
	}
	exec.Command(conf.Exe(), "_unit").Run()
	logf("updated to %s", tag)
	return true
}
