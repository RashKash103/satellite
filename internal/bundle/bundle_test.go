package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type file struct {
	name, body string
	mode       int64
	kind       byte
}

func tgz(t *testing.T, fs ...file) []byte {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	for _, f := range fs {
		h := &tar.Header{Name: f.name, Mode: f.mode, Typeflag: f.kind, Size: int64(len(f.body))}
		if f.kind == tar.TypeSymlink || f.kind == tar.TypeLink {
			h.Linkname, h.Size = f.body, 0
		}
		if f.kind == tar.TypeDir {
			h.Size = 0
		}
		w.WriteHeader(h)
		if f.kind == tar.TypeReg {
			w.Write([]byte(f.body))
		}
	}
	w.Close()
	z.Close()
	return b.Bytes()
}

func TestExtractRefusesEscapes(t *testing.T) {
	for _, f := range []file{{"../x", "a", 0o644, tar.TypeReg}, {"/etc/x", "a", 0o644, tar.TypeReg}, {"l", "../../x", 0o644, tar.TypeLink}} {
		if err := Extract(tgz(t, f), t.TempDir()); err == nil {
			t.Errorf("%s extracted", f.name)
		}
	}
}

// fake serves a bundle.
type fake struct{ data []byte }

func (f *fake) Call(_ context.Context, _ io.Reader, out, _ io.Writer, args ...string) (int, error) {
	out.Write(f.data)
	return 0, nil
}
func (f *fake) Gen() uint64  { return 0 }
func (f *fake) Name() string { return "fake" }
func (f *fake) Close() error { return nil }

func TestSync(t *testing.T) {
	base := t.TempDir()
	v1 := tgz(t, file{"./", "", 0o755, tar.TypeDir}, file{"./sat/", "", 0o755, tar.TypeDir}, file{"./sat/job", "#!/bin/sh\n", 0o755, tar.TypeReg}, file{"./sat/l", "job", 0, tar.TypeSymlink})
	f := &fake{v1}
	if ch, err := Sync(context.Background(), f, "p", base, nil); err != nil || !ch {
		t.Fatal(ch, err)
	}
	os.MkdirAll(filepath.Join(base, "bundle", "state"), 0o755)
	os.WriteFile(filepath.Join(base, "bundle", "state", "keep"), []byte("x"), 0o644)
	if ch, err := Sync(context.Background(), f, "p", base, nil); err != nil || ch {
		t.Fatal("unchanged bundle swapped", ch, err)
	}
	f.data = tgz(t, file{"sat/job", "#!/bin/sh\nexit 0\n", 0o755, tar.TypeReg})
	locked := false
	if ch, err := Sync(context.Background(), f, "p", base, func() func() { locked = true; return func() {} }); err != nil || !ch || !locked {
		t.Fatal(ch, err, locked)
	}
	if b, _ := os.ReadFile(filepath.Join(base, "bundle", "state", "keep")); string(b) != "x" {
		t.Fatal("bundle/state not carried over")
	}
	if _, err := os.Stat(filepath.Join(base, "bundle", "sat", "l")); err == nil {
		t.Fatal("old file left")
	}
	fi, _ := os.Stat(filepath.Join(base, "bundle", "sat", "job"))
	if fi.Mode()&0o111 == 0 {
		t.Fatal("job not executable")
	}
	f.data = tgz(t, file{"x", "", 0o644, tar.TypeReg})
	if _, err := Sync(context.Background(), f, "p", base, nil); err == nil {
		t.Fatal("bundle without sat/job accepted")
	}
	if _, err := os.Stat(filepath.Join(base, "bundle", "sat", "job")); err != nil {
		t.Fatal("a bad bundle replaced the good one")
	}
}
