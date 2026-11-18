// Package spa serves the embedded single-page web UI with strict security headers.
//
// All UI resources are served from the binary itself – no CDNs or third-party origins
// (KONZEPT D27). The Content-Security-Policy therefore only allows 'self'; the hashes
// of inline bootstrap scripts are computed from index.html at startup, so no
// 'unsafe-inline' is needed. Static style attributes that ship in our own bundles (e.g. the
// SvelteKit route announcer) are allowed by their exact hash via 'unsafe-hashes'.
package spa

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// ErrNoIndex is returned by New when the UI has not been built into the file system.
var ErrNoIndex = errors.New("index.html not found – web UI not built (run `just build-web`)")

const (
	indexFile      = "index.html"
	immutablePath  = "_app/immutable/"
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheRevalid   = "no-cache"
)

// Handler serves the SPA: existing files as-is, client-side routes via index.html.
type Handler struct {
	files fs.FS
	index []byte
	csp   string
}

// New prepares a Handler for the built UI in files.
func New(files fs.FS) (*Handler, error) {
	index, err := fs.ReadFile(files, indexFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoIndex, err)
	}
	styleHashes, err := StyleAttributeHashes(files)
	if err != nil {
		return nil, err
	}
	csp := contentSecurityPolicy(InlineScriptHashes(index), styleHashes)
	return &Handler{files: files, index: index, csp: csp}, nil
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setSecurityHeaders(w.Header())

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == indexFile {
		h.serveIndex(w, r)
		return
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}

	if info, err := fs.Stat(h.files, name); err == nil && !info.IsDir() {
		if strings.HasPrefix(name, immutablePath) {
			w.Header().Set("Cache-Control", cacheImmutable)
		} else {
			w.Header().Set("Cache-Control", cacheRevalid)
		}
		//nolint:gosec // G703: name is cleaned, checked with fs.ValidPath, and files is a read-only embed.FS of the UI build.
		http.ServeFileFS(w, r, h.files, name)
		return
	}

	// Paths without a file extension are client-side routes of the SPA.
	if path.Ext(name) == "" {
		h.serveIndex(w, r)
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", cacheRevalid)
	http.ServeContent(w, r, indexFile, time.Time{}, bytes.NewReader(h.index))
}

func (h *Handler) setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", h.csp)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "same-origin")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
}

func contentSecurityPolicy(scriptHashes, styleAttributeHashes []string) string {
	scriptSrc := strings.Join(append([]string{"'self'"}, scriptHashes...), " ")
	styleSrc := "'self'"
	if len(styleAttributeHashes) > 0 {
		styleSrc += " 'unsafe-hashes' " + strings.Join(styleAttributeHashes, " ")
	}
	return strings.Join([]string{
		"default-src 'self'",
		"script-src " + scriptSrc,
		"style-src " + styleSrc,
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
}

var (
	scriptTag      = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
	styleAttribute = regexp.MustCompile(`style="([^"\\]*)"`)
)

// InlineScriptHashes returns CSP hash sources ('sha256-…') for all inline scripts in html.
// Scripts with a src attribute are covered by 'self' and skipped.
func InlineScriptHashes(html []byte) []string {
	var hashes []string
	for _, match := range scriptTag.FindAllSubmatch(html, -1) {
		attrs, body := match[1], match[2]
		if bytes.Contains(attrs, []byte("src=")) {
			continue
		}
		hashes = append(hashes, hashSource(body))
	}
	return hashes
}

// StyleAttributeHashes returns CSP hash sources for all static style="…" attributes found in
// the JavaScript bundles of files (deduplicated, in order of appearance).
func StyleAttributeHashes(files fs.FS) ([]string, error) {
	seen := map[string]bool{}
	var hashes []string
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Ext(name) != ".js" {
			return nil
		}
		content, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		for _, match := range styleAttribute.FindAllSubmatch(content, -1) {
			source := hashSource(match[1])
			if !seen[source] {
				seen[source] = true
				hashes = append(hashes, source)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan UI bundles for style attributes: %w", err)
	}
	return hashes, nil
}

func hashSource(content []byte) string {
	sum := sha256.Sum256(content)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}
