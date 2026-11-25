// Package download serves the agent installer and the binaries of agent and root helper (KONZEPT
// §6, ADR 0012, ADR 0035): GET /install.sh and GET /downloads/sermon-{agent,helper}-linux-<arch>[.minisig].
//
// The installer is rendered once at startup with the release public key, the server version and
// the init files. The key is compiled into the server (not configurable at runtime), so the script
// only accepts binaries signed by the release key for exactly this version. Its SHA-256 (hex, of
// the uncompressed bytes) goes out as X-Sermon-Script-SHA256: the UI shows it for servers without
// a publicly trusted certificate, the host checks the script with sha256sum before running it.
// (Not Repr-Digest: that would cover a compression a proxy adds.) Binaries and signatures come from a directory of the
// release (SERMON_AGENT_DOWNLOADS_DIR); only the fixed file names are served.
package download

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	//go:embed assets/install.sh
	installScript string
	//go:embed assets/sermon-agent.service
	systemdUnit string
	//go:embed assets/sermon-agent.openrc
	openrcService string
	//go:embed assets/sermon-helper.service
	helperUnit string
	//go:embed assets/sermon-helper.socket
	helperSocket string
	//go:embed assets/sermon-helper.openrc
	helperService string
	//go:embed assets/helper.toml
	helperConfig string
)

// ErrInvalidKey means the release key is not a minisign Ed25519 public key.
var ErrInvalidKey = errors.New("release key must be a minisign Ed25519 public key (base64, 42 bytes, \"Ed\")")

// ScriptDigestHeader carries the SHA-256 of install.sh (hex).
const ScriptDigestHeader = "X-Sermon-Script-SHA256"

// ErrInvalidVersion means the server version can't be written into the script safely.
var ErrInvalidVersion = errors.New("server version must consist of letters, digits and . + _ -")

var version = regexp.MustCompile(`^[0-9A-Za-z.+_-]+$`)

var fileName = regexp.MustCompile(`^sermon-(agent|helper)-linux-(amd64|arm64)(\.minisig)?$`)

// Handler serves the installer and the binaries.
type Handler struct {
	script []byte
	digest string // SHA-256 of script, hex
	files  fs.FS  // nil: no binaries
}

// New renders the installer for the server version. releaseKey may be empty (the script then
// refuses to install); dir may be empty (no binaries).
func New(releaseKey, serverVersion, dir string) (*Handler, error) {
	if !version.MatchString(serverVersion) {
		return nil, ErrInvalidVersion
	}
	if releaseKey != "" {
		raw, err := base64.StdEncoding.DecodeString(releaseKey)
		if err != nil || len(raw) != 42 || !bytes.HasPrefix(raw, []byte("Ed")) {
			return nil, ErrInvalidKey
		}
	}
	script := installScript
	if releaseKey != "" {
		script = strings.Replace(script, "@SERMON_RELEASE_KEY@", releaseKey, 1)
	}
	script = strings.Replace(script, "@SERMON_AGENT_VERSION@", serverVersion, 1)
	script = strings.Replace(script, "@SERMON_SYSTEMD_UNIT@", strings.TrimRight(systemdUnit, "\n"), 1)
	script = strings.Replace(script, "@SERMON_OPENRC_SERVICE@", strings.TrimRight(openrcService, "\n"), 1)
	script = strings.Replace(script, "@SERMON_HELPER_UNIT@", strings.TrimRight(helperUnit, "\n"), 1)
	script = strings.Replace(script, "@SERMON_HELPER_SOCKET@", strings.TrimRight(helperSocket, "\n"), 1)
	script = strings.Replace(script, "@SERMON_HELPER_SERVICE@", strings.TrimRight(helperService, "\n"), 1)
	script = strings.Replace(script, "@SERMON_HELPER_CONFIG@", strings.TrimRight(helperConfig, "\n"), 1)
	sum := sha256.Sum256([]byte(script))
	h := &Handler{script: []byte(script), digest: hex.EncodeToString(sum[:])}
	if dir != "" {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, fmt.Errorf("open agent downloads: %w", err)
		}
		h.files = root.FS()
	}
	return h, nil
}

// Register mounts the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /install.sh", h.serveScript)
	mux.HandleFunc("GET /downloads/{name}", h.serveFile)
}

func headers(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "same-origin")
}

// serveScript answers as text so that "view the script first" shows it in the browser.
func (h *Handler) serveScript(w http.ResponseWriter, r *http.Request) {
	headers(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set(ScriptDigestHeader, h.digest)
	http.ServeContent(w, r, "install.sh", time.Time{}, bytes.NewReader(h.script))
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request) {
	headers(w)
	name := r.PathValue("name")
	if h.files == nil || !fileName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	file, err := h.files.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	content, ok := file.(interface {
		Read(p []byte) (int, error)
		Seek(offset int64, whence int) (int64, error)
	})
	if err != nil || info.IsDir() || !ok {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".minisig") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, name, info.ModTime(), content)
}
