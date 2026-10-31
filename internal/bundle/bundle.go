// Package bundle keeps a project's bundle in step with the hub's (`sat-hub PROJECT bundle`, a
// tar.gz). It is unpacked beside and swapped in whole, and only when it changed, so a running job
// never reads a half-written file. A bundle/state directory, if a project keeps one, is carried over.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/RashKash103/satellite/internal/hub"
)

// Extract unpacks a tar.gz into dir. Names leaving dir, devices and the like are refused.
func Extract(data []byte, dir string) error {
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	tr := tar.NewReader(z)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(h.Name)
		if name == "." {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("bundle entry outside the bundle: %s", h.Name)
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(h.Mode) & 0o777
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			os.Chmod(p, mode|0o700)
		case tar.TypeReg:
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			os.Chmod(p, mode)
		case tar.TypeSymlink:
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		case tar.TypeLink:
			t := filepath.Clean(h.Linkname)
			if filepath.IsAbs(t) || strings.HasPrefix(t, "../") || t == ".." {
				return fmt.Errorf("bundle link outside the bundle: %s", h.Linkname)
			}
			os.Remove(p)
			if err := os.Link(filepath.Join(dir, t), p); err != nil {
				return err
			}
		}
	}
}

type entry struct {
	mode fs.FileMode
	data string // content, or a symlink's target
}

func tree(dir string) (map[string]entry, error) {
	m := map[string]entry{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "state" && d.IsDir() {
			return filepath.SkipDir
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{mode: fi.Mode()}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			e.data, err = os.Readlink(p)
		case fi.Mode().IsRegular():
			var b []byte
			b, err = os.ReadFile(p)
			e.data = string(b)
		case fi.IsDir():
			e.mode = fs.ModeDir
		}
		m[rel] = e
		return err
	})
	return m, err
}

// Same: the two trees hold the same files, modes and links (a top-level state/ aside).
func Same(a, b string) bool {
	ta, err := tree(a)
	if err != nil {
		return false
	}
	tb, err := tree(b)
	if err != nil || len(ta) != len(tb) {
		return false
	}
	for k, v := range ta {
		if tb[k] != v {
			return false
		}
	}
	return true
}

// Sync fetches the project's bundle into BASE/bundle. changed: it was swapped in.
// lock, if not nil, is held around the swap.
func Sync(ctx context.Context, c hub.Caller, project, base string, lock func() func()) (changed bool, err error) {
	var out, errb bytes.Buffer
	rc, err := c.Call(ctx, nil, &out, &errb, project, "bundle")
	if err != nil {
		return false, err
	}
	if rc != 0 {
		return false, fmt.Errorf("the hub's bundle hook failed (exit %d): %s", rc, strings.TrimSpace(errb.String()))
	}
	cur, nw, old := filepath.Join(base, "bundle"), filepath.Join(base, "bundle.new"), filepath.Join(base, "bundle.old")
	os.RemoveAll(nw)
	if err := os.MkdirAll(nw, 0o755); err != nil {
		return false, err
	}
	if err := Extract(out.Bytes(), nw); err != nil {
		os.RemoveAll(nw)
		return false, err
	}
	if _, err := os.Stat(filepath.Join(nw, "sat", "job")); err != nil {
		os.RemoveAll(nw)
		return false, errors.New("the hub's bundle has no sat/job")
	}
	if _, err := os.Stat(cur); err == nil && Same(nw, cur) {
		os.RemoveAll(nw)
		return false, nil
	}
	if lock != nil {
		defer lock()()
	}
	if _, err := os.Stat(filepath.Join(cur, "state")); err == nil {
		os.RemoveAll(filepath.Join(nw, "state"))
		if err := os.Rename(filepath.Join(cur, "state"), filepath.Join(nw, "state")); err != nil {
			return false, err
		}
	}
	os.RemoveAll(old)
	if _, err := os.Stat(cur); err == nil {
		if err := os.Rename(cur, old); err != nil {
			return false, err
		}
	}
	if err := os.Rename(nw, cur); err != nil {
		return false, err
	}
	os.RemoveAll(old)
	return true, nil
}
