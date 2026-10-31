// Package selfupdate: new versions of sat come from this repository's GitHub releases, never from a
// hub. A release carries sat_linux_<arch>, checksums.txt (sha256) and a build attestation. By
// default a node only notes that one exists (checked at most daily, cached in ~/.sat/update.json).
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/RashKash103/satellite/internal/conf"
)

const Repo = "RashKash103/satellite"

// API is GitHub's; tests point SAT_RELEASES_API at their own server.
func API() string {
	if a := os.Getenv("SAT_RELEASES_API"); a != "" {
		return strings.TrimRight(a, "/")
	}
	return "https://api.github.com"
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type Release struct {
	Tag    string  `json:"tag_name"`
	Name   string  `json:"name"`
	Body   string  `json:"body"`
	Assets []Asset `json:"assets"`
}

func AssetName() string { return "sat_linux_" + runtime.GOARCH }

var client = &http.Client{Timeout: 2 * time.Minute}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	r, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if r.StatusCode != 200 {
		r.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, r.Status)
	}
	return r, nil
}

// Fetch: the release tagged tag, or the latest one when tag is empty.
func Fetch(ctx context.Context, tag string) (*Release, error) {
	u := API() + "/repos/" + Repo + "/releases/latest"
	if tag != "" {
		u = API() + "/repos/" + Repo + "/releases/tags/" + tag
	}
	r, err := get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	var rel Release
	if err := json.NewDecoder(r.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.Tag == "" {
		return nil, errors.New("the release has no tag")
	}
	return &rel, nil
}

func (r *Release) asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// parse "v1.2.3" -> [1 2 3]; ok false for anything else (a dev build).
func parse(v string) ([3]int, bool) {
	var n [3]int
	p := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(p) != 3 {
		return n, false
	}
	for i := range p {
		x, err := strconv.Atoi(strings.SplitN(p[i], "-", 2)[0])
		if err != nil {
			return n, false
		}
		n[i] = x
	}
	return n, true
}

// Newer: b is a later version than a. A dev build is older than any release.
func Newer(a, b string) bool {
	x, okx := parse(a)
	y, oky := parse(b)
	if !oky {
		return false
	}
	if !okx {
		return true
	}
	for i := range 3 {
		if x[i] != y[i] {
			return y[i] > x[i]
		}
	}
	return false
}

// Wanted: whether to move from current to tag; with a pin, only to the pin.
func Wanted(current, tag, pin string) bool {
	if pin != "" {
		return tag == pin && current != pin
	}
	return Newer(current, tag)
}

type cache struct {
	Checked time.Time `json:"checked"`
	Tag     string    `json:"tag"`
	Err     string    `json:"error,omitempty"`
}

func cachePath() string { return filepath.Join(conf.Root(), "update.json") }

// Available: the version this node could move to, or "". It asks GitHub at most once a day
// (force: now); the answer is cached for status and the live view.
func Available(ctx context.Context, current string, force bool) (string, error) {
	pin := conf.Node()["PIN"]
	var c cache
	if b, err := os.ReadFile(cachePath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if force || time.Since(c.Checked) > 24*time.Hour {
		c = cache{Checked: time.Now()}
		rel, err := Fetch(ctx, pin)
		if err != nil {
			c.Err = err.Error()
		} else {
			c.Tag = rel.Tag
		}
		b, _ := json.Marshal(c)
		conf.WriteAtomic(cachePath(), b, 0o644)
	}
	if c.Err != "" {
		return "", errors.New(c.Err)
	}
	if Wanted(current, c.Tag, pin) {
		return c.Tag, nil
	}
	return "", nil
}

// Cached: what the last check found, without asking.
func Cached(current string) string {
	var c cache
	if b, err := os.ReadFile(cachePath()); err == nil && json.Unmarshal(b, &c) == nil && Wanted(current, c.Tag, conf.Node()["PIN"]) {
		return c.Tag
	}
	return ""
}

// Sums parses a checksums file: "HEX  NAME" lines.
func Sums(s string) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 {
			m[strings.TrimPrefix(f[1], "*")] = strings.ToLower(f[0])
		}
	}
	return m
}

// Download fetches the release's binary for this machine beside dest and checks it against the
// release's checksums.txt. It returns the downloaded file's path.
func Download(ctx context.Context, rel *Release, dest string) (string, error) {
	bin, sums := rel.asset(AssetName()), rel.asset("checksums.txt")
	if bin == nil || sums == nil {
		return "", fmt.Errorf("release %s has no %s or no checksums.txt", rel.Tag, AssetName())
	}
	r, err := get(ctx, sums.URL)
	if err != nil {
		return "", err
	}
	sb, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body.Close()
	if err != nil {
		return "", err
	}
	want := Sums(string(sb))[bin.Name]
	if want == "" {
		return "", fmt.Errorf("checksums.txt lists no %s", bin.Name)
	}
	r, err = get(ctx, bin.URL)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	f, err := os.CreateTemp(filepath.Dir(dest), ".sat.new.*")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, 200<<20))
	f.Close()
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = fmt.Errorf("%s does not match its sha256 in checksums.txt", bin.Name)
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o755)
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Attest runs `gh attestation verify` on the file. ok: verified; checked false: gh is missing or
// not logged in, so it couldn't be checked.
func Attest(ctx context.Context, path string) (checked bool, why string, err error) {
	if _, e := exec.LookPath("gh"); e != nil {
		return false, "gh is not installed", nil
	}
	if exec.CommandContext(ctx, "gh", "auth", "status").Run() != nil {
		return false, "gh is not logged in", nil
	}
	out, e := exec.CommandContext(ctx, "gh", "attestation", "verify", path, "--repo", Repo).CombinedOutput()
	if e != nil {
		return true, "", fmt.Errorf("gh attestation verify: %s", strings.TrimSpace(string(out)))
	}
	return true, "", nil
}

// Swap puts the new binary in place of dest, atomically.
func Swap(newPath, dest string) error { return os.Rename(newPath, dest) }
