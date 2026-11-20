// Package download serves the agent installer and the agent binaries (KONZEPT §6, ADR 0012):
// GET /install.sh and GET /downloads/sermon-agent-linux-<arch>[.minisig].
//
// The installer is rendered once at startup with the release public key and the init files. The
// key is compiled into the server (not configurable at runtime), so the script only accepts
// binaries signed by the release key. Binaries and signatures come from a directory of the
// release (SERMON_AGENT_DOWNLOADS_DIR); only the fixed file names are served.
package download

import (
	"bytes"
	_ "embed"
	"encoding/base64"
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
)

// ErrInvalidKey means the release key is not a minisign Ed25519 public key.
var ErrInvalidKey = errors.New("release key must be a minisign Ed25519 public key (base64, 42 bytes, \"Ed\")")

var fileName = regexp.MustCompile(`^sermon-agent-linux-(amd64|arm64)(\.minisig)?$`)

// Handler serves the installer and the binaries.
type Handler struct {
	script []byte
	files  fs.FS // nil: no binaries
}

// New renders the installer. releaseKey may be empty (the script then refuses to install);
// dir may be empty (no binaries).
func New(releaseKey, dir string) (*Handler, error) {
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
	script = strings.Replace(script, "@SERMON_SYSTEMD_UNIT@", strings.TrimRight(systemdUnit, "\n"), 1)
	script = strings.Replace(script, "@SERMON_OPENRC_SERVICE@", strings.TrimRight(openrcService, "\n"), 1)
	h := &Handler{script: []byte(script)}
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
