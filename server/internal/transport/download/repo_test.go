package download_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/transport/download"
)

// M4-20: the repositories below their four areas, files only; nothing outside the directory,
// no listings, no hidden files, no links out of it.
func TestRepository_ServesTheRepositoriesAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	for name, data := range map[string]string{
		"apt/dists/stable/InRelease":            "signed index",
		"apk/aarch64/sermon-agent-1.0.0-r1.apk": "apk",
		"keys/sermon.asc":                       "key",
		"keys/sermon-0123456789abcdef.rsa.pub":  "apk key",
		"keys/SHA256SUMS":                       "sums",
		"keys/SHA256SUMS.minisig":               "signature",
		"rpm/repodata/repomd.xml":               "<repomd/>",
		".work/keys/gpg.asc":                    "private!",
		"apt/.hidden":                           "hidden",
		"other/file":                            "other",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "keys", "link")); err != nil {
		t.Fatal(err)
	}
	repo, err := download.NewRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	repo.Register(mux)
	get := func(p string) (int, string, http.Header) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, p, nil))
		body, _ := io.ReadAll(rec.Result().Body)
		return rec.Code, string(body), rec.Result().Header
	}
	for p, want := range map[string]string{
		"/repo/apt/dists/stable/InRelease":            "signed index",
		"/repo/apk/aarch64/sermon-agent-1.0.0-r1.apk": "apk",
		"/repo/keys/sermon.asc":                       "key",
		"/repo/keys/sermon-0123456789abcdef.rsa.pub":  "apk key",
		"/repo/keys/SHA256SUMS":                       "sums",
		"/repo/keys/SHA256SUMS.minisig":               "signature",
		"/repo/rpm/repodata/repomd.xml":               "<repomd/>",
	} {
		code, body, h := get(p)
		if code != http.StatusOK || body != want || h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s = %d %q", p, code, body)
		}
	}
	if _, _, h := get("/repo/apk/aarch64/sermon-agent-1.0.0-r1.apk"); h.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("apk content type = %q", h.Get("Content-Type"))
	}
	for _, p := range []string{
		"/repo/.work/keys/gpg.asc", "/repo/apt/.hidden", "/repo/other/file", "/repo/apt", "/repo/apt/dists", "/repo/keys/link", "/repo/",
	} {
		if code, body, _ := get(p); code != http.StatusNotFound {
			t.Errorf("%s = %d %q, want 404", p, code, body)
		}
	}
	// The mux cleans these first (a redirect); none is served.
	for _, p := range []string{"/repo/keys/../.work/keys/gpg.asc", "/repo/keys/%2e%2e/.work/keys/gpg.asc", "/repo/keys//sermon.asc"} {
		if code, _, _ := get(p); code == http.StatusOK {
			t.Errorf("%s served", p)
		}
	}
	// Files directly below the directory are none of the repositories', even with an area's name.
	flat := t.TempDir()
	if err := os.WriteFile(filepath.Join(flat, "keys"), []byte("flat"), 0o644); err != nil {
		t.Fatal(err)
	}
	flatRepo, err := download.NewRepository(flat)
	if err != nil {
		t.Fatal(err)
	}
	flatMux := http.NewServeMux()
	flatRepo.Register(flatMux)
	rec := httptest.NewRecorder()
	flatMux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/repo/keys", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a file of the top level = %d", rec.Code)
	}
	empty, err := download.NewRepository("")
	if err != nil {
		t.Fatal(err)
	}
	mux = http.NewServeMux()
	empty.Register(mux)
	if code, _, _ := get("/repo/keys/sermon.asc"); code != http.StatusNotFound {
		t.Errorf("without a directory: %d", code)
	}
	if _, err := download.NewRepository(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing directory was taken")
	}
}
