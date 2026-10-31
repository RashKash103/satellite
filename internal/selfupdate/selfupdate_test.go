package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"v0.1.0", "v0.2.0", true}, {"v0.2.0", "v0.1.9", false}, {"v1.0.0", "v1.0.0", false}, {"dev", "v0.1.0", true}, {"v0.1.0", "junk", false}, {"v1.2.3", "v1.10.0", true}} {
		if Newer(c.a, c.b) != c.want {
			t.Errorf("Newer(%s, %s) != %v", c.a, c.b, c.want)
		}
	}
	if !Wanted("v0.3.0", "v0.2.0", "v0.2.0") || Wanted("v0.2.0", "v0.3.0", "v0.2.0") {
		t.Error("pin")
	}
}

func TestSums(t *testing.T) {
	m := Sums("ABC  sat_linux_amd64\ndef *sat_linux_arm64\nbad\n")
	if m["sat_linux_amd64"] != "abc" || m["sat_linux_arm64"] != "def" || len(m) != 2 {
		t.Fatal(m)
	}
}

func server(t *testing.T, bin []byte, sum string) *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Release{Tag: "v9.0.0", Body: "notes", Assets: []Asset{{AssetName(), srv.URL + "/bin"}, {"checksums.txt", srv.URL + "/sums"}}})
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) { w.Write(bin) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(sum + "  " + AssetName() + "\n")) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDownload(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := []byte("#!/bin/sh\necho new\n")
	h := sha256.Sum256(bin)
	srv := server(t, bin, hex.EncodeToString(h[:]))
	t.Setenv("SAT_RELEASES_API", srv.URL)
	if v, err := Available(context.Background(), "v1.0.0", false); err != nil || v != "v9.0.0" {
		t.Fatal(v, err)
	}
	if Cached("v1.0.0") != "v9.0.0" || Cached("v9.0.0") != "" {
		t.Fatal("cache")
	}
	rel, err := Fetch(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "sat")
	p, err := Download(context.Background(), rel, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := Swap(p, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(bin) {
		t.Fatal("swapped content")
	}
}

func TestDownloadRefusesBadSum(t *testing.T) {
	srv := server(t, []byte("evil"), "00")
	t.Setenv("SAT_RELEASES_API", srv.URL)
	rel, _ := Fetch(context.Background(), "")
	d := t.TempDir()
	if _, err := Download(context.Background(), rel, filepath.Join(d, "sat")); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if es, _ := os.ReadDir(d); len(es) != 0 {
		t.Fatal("left a file")
	}
}
