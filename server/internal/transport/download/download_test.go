package download_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/transport/download"
)

func releaseKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(append([]byte("Ed12345678"), public...))
}

func serve(t *testing.T, h *download.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

func TestInstallScript_IsRenderedWithKeyAndInitFiles(t *testing.T) {
	key := releaseKey(t)
	h, err := download.New(key, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(t, h, http.MethodGet, "/install.sh")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("GET /install.sh = %d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(body, "RELEASE_KEY='"+key+"'") || strings.Contains(body, "@SERMON_") {
		t.Error("placeholders left or key missing")
	}
	unit, _ := os.ReadFile("../../../../packaging/systemd/sermon-agent.service")
	service, _ := os.ReadFile("../../../../packaging/openrc/sermon-agent")
	if !strings.Contains(body, strings.TrimRight(string(unit), "\n")) || !strings.Contains(body, strings.TrimRight(string(service), "\n")) {
		t.Error("the script must contain the init files of packaging/")
	}
}

func TestAssets_MatchPackaging(t *testing.T) {
	for asset, source := range map[string]string{
		"assets/install.sh":           "../../../../packaging/install.sh",
		"assets/sermon-agent.service": "../../../../packaging/systemd/sermon-agent.service",
		"assets/sermon-agent.openrc":  "../../../../packaging/openrc/sermon-agent",
	} {
		a, errA := os.ReadFile(asset)
		b, errB := os.ReadFile(source)
		if errA != nil || errB != nil || string(a) != string(b) {
			t.Errorf("%s differs from %s – run just gen", asset, source)
		}
	}
}

func TestNew_RejectsMalformedKeys(t *testing.T) {
	for _, key := range []string{"not base64!", base64.StdEncoding.EncodeToString([]byte("short")), base64.StdEncoding.EncodeToString(make([]byte, 42))} {
		if _, err := download.New(key, ""); !errors.Is(err, download.ErrInvalidKey) {
			t.Errorf("New(%q) = %v", key, err)
		}
	}
	h, err := download.New("", "")
	if err != nil || !strings.Contains(serve(t, h, http.MethodGet, "/install.sh").Body.String(), "RELEASE_KEY='@SERMON_RELEASE_KEY@'") {
		t.Errorf("without key the placeholder stays (the script refuses to install): %v", err)
	}
}

func TestDownloads_ServeOnlyTheAgentFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"sermon-agent-linux-amd64": "binary", "sermon-agent-linux-amd64.minisig": "signature", "secret.txt": "no",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "sermon-agent-linux-arm64")); err != nil {
		t.Fatal(err)
	}
	h, err := download.New(releaseKey(t), dir)
	if err != nil {
		t.Fatal(err)
	}

	if rec := serve(t, h, http.MethodGet, "/downloads/sermon-agent-linux-amd64"); rec.Code != http.StatusOK || rec.Body.String() != "binary" ||
		rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("binary = %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := serve(t, h, http.MethodGet, "/downloads/sermon-agent-linux-amd64.minisig"); rec.Code != http.StatusOK || rec.Body.String() != "signature" {
		t.Errorf("signature = %d", rec.Code)
	}
	for _, path := range []string{
		"/downloads/secret.txt", "/downloads/..%2fsecret.txt", "/downloads/sermon-agent-linux-arm64",
		"/downloads/sermon-agent-linux-riscv64",
	} {
		if rec := serve(t, h, http.MethodGet, path); rec.Code == http.StatusOK {
			t.Errorf("%s served: %q", path, rec.Body.String())
		}
	}
	if rec := serve(t, h, http.MethodPost, "/install.sh"); rec.Code == http.StatusOK {
		t.Errorf("POST /install.sh = %d", rec.Code)
	}
}
