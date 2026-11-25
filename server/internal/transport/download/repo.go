package download

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
)

// The package repositories (M4-20, ADR 0036): GET /repo/{apt,rpm,apk,keys}/… from a directory
// built by scripts/build-repos.sh (SERMON_REPO_DIR), read-only below it. No listings, no hidden
// files, no other top-level directories.

var (
	repoAreas   = map[string]bool{"apt": true, "rpm": true, "apk": true, "keys": true}
	repoSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]{0,127}$`)
)

const repoMaxDepth = 8

// Repository serves the package repositories.
type Repository struct {
	files fs.FS // nil: none configured
}

// NewRepository serves dir; an empty dir serves nothing (404).
func NewRepository(dir string) (*Repository, error) {
	if dir == "" {
		return &Repository{}, nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open package repositories: %w", err)
	}
	return &Repository{files: root.FS()}, nil
}

// Register mounts GET /repo/….
func (r *Repository) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /repo/{path...}", r.serve)
}

// repoPath reports whether p names a file the repositories may serve.
func repoPath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) < 2 || len(parts) > repoMaxDepth || !repoAreas[parts[0]] || !fs.ValidPath(p) {
		return false
	}
	for _, part := range parts {
		if !repoSegment.MatchString(part) {
			return false
		}
	}
	return true
}

func (r *Repository) serve(w http.ResponseWriter, req *http.Request) {
	headers(w)
	name := req.PathValue("path")
	if r.files == nil || !repoPath(name) {
		http.NotFound(w, req)
		return
	}
	file, err := r.files.Open(name)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	content, ok := file.(io.ReadSeeker)
	if err != nil || !info.Mode().IsRegular() || !ok {
		http.NotFound(w, req)
		return
	}
	switch path.Ext(name) {
	case ".asc", ".pub", ".gpg", ".xml", "":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// Indexes change with every release; the package managers check their signatures.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, req, path.Base(name), info.ModTime(), content)
}
