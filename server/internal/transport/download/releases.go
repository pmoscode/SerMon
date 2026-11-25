package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/pmoscode/SerMon/server/internal/platform/minisign"
)

// Bounds of a release (ADR 0036).
const (
	MaxBinary    = 64 << 20
	MaxSignature = 4 << 10
	// RescanEvery is how often the catalog reads the directory again; a request for an unknown
	// version rescans at most every rescanUnknown.
	RescanEvery   = 5 * time.Minute
	rescanUnknown = 10 * time.Second
)

// Channels and their directories below SERMON_AGENT_DOWNLOADS_DIR.
var channels = []struct{ name, dir string }{{"stable", ""}, {"beta", "beta/"}}

// Architectures of the releases.
var architectures = []string{"amd64", "arm64"}

var semver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// Release is an agent binary with a valid signature of the release key.
type Release struct {
	Channel, Version, Arch string
	Size                   int64
	Signature              []byte
	name                   string // below the root
	modified               time.Time
}

// Releases knows the signed agent binaries of the downloads directory (ADR 0036): stable is the
// server's own version in the directory itself, beta an optional newer one in beta/. Only what
// the release key signed for its file name counts.
type Releases struct {
	root          *os.Root
	key           minisign.PublicKey
	serverVersion string
	logger        *slog.Logger
	now           func() time.Time

	mu      sync.Mutex
	known   map[string]Release // by name
	scanned time.Time
}

// NewReleases returns the catalog of dir; without a directory or key it knows no release.
func NewReleases(dir, releaseKey, serverVersion string, logger *slog.Logger) (*Releases, error) {
	r := &Releases{serverVersion: serverVersion, logger: logger, now: time.Now, known: map[string]Release{}}
	if dir == "" || releaseKey == "" {
		return r, nil
	}
	key, err := minisign.ParseKey(releaseKey)
	if err != nil {
		return nil, ErrInvalidKey
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open agent downloads: %w", err)
	}
	r.root, r.key = root, key
	r.Scan()
	return r, nil
}

// Scan reads the directory again; unchanged files keep their checked state.
func (r *Releases) Scan() {
	if r.root == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanned = r.now()
	found := map[string]Release{}
	for _, c := range channels {
		for _, arch := range architectures {
			file := "sermon-agent-linux-" + arch
			name := c.dir + file
			info, err := r.root.Stat(name)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if old, ok := r.known[name]; ok && old.Size == info.Size() && old.modified.Equal(info.ModTime()) {
				found[name] = old
				continue
			}
			rel, err := r.check(name, file, info)
			if err != nil {
				r.logger.Warn("agent release ignored", "file", name, "error", err)
				continue
			}
			rel.Channel, rel.Arch = c.name, arch
			if c.name == "stable" && rel.Version != r.serverVersion {
				r.logger.Warn("agent release ignored: stable is the server's version", "file", name, "version", rel.Version, "server", r.serverVersion)
				continue
			}
			found[name] = rel
		}
	}
	r.known = found
}

var errRelease = errors.New("not a release of the release key")

func (r *Releases) check(name, file string, info fs.FileInfo) (Release, error) {
	if info.Size() > MaxBinary {
		return Release{}, fmt.Errorf("%w: larger than %d bytes", errRelease, MaxBinary)
	}
	sig, err := r.read(name+".minisig", MaxSignature)
	if err != nil {
		return Release{}, err
	}
	data, err := r.read(name, MaxBinary)
	if err != nil {
		return Release{}, err
	}
	signed, err := minisign.Verify(r.key, sig, data)
	if err != nil || signed.File != file || !semver.MatchString(signed.Version) {
		return Release{}, errRelease
	}
	return Release{Version: signed.Version, Size: int64(len(data)), Signature: sig, name: name, modified: info.ModTime()}, nil
}

func (r *Releases) read(name string, limit int64) ([]byte, error) {
	f, err := r.root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s larger than %d bytes", errRelease, name, limit)
	}
	return data, nil
}

// Find returns the release version for arch; an unknown version rescans first (at most every 10 s).
func (r *Releases) Find(version, arch string) (Release, bool) {
	if rel, ok := r.find(version, arch); ok {
		return rel, true
	}
	r.mu.Lock()
	stale := r.now().Sub(r.scanned) >= rescanUnknown
	r.mu.Unlock()
	if stale {
		r.Scan()
	}
	return r.find(version, arch)
}

func (r *Releases) find(version, arch string) (Release, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rel := range r.known {
		if rel.Version == version && rel.Arch == arch {
			return rel, true
		}
	}
	return Release{}, false
}

// Latest returns the release of channel for arch.
func (r *Releases) Latest(channel, arch string) (Release, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rel := range r.known {
		if rel.Channel == channel && rel.Arch == arch {
			return rel, true
		}
	}
	return Release{}, false
}

// Open opens the binary of rel; the caller closes it. The helper checks the signature again.
func (r *Releases) Open(rel Release) (io.ReadCloser, error) {
	if r.root == nil {
		return nil, fs.ErrNotExist
	}
	f, err := r.root.Open(rel.name)
	if err != nil {
		return nil, fmt.Errorf("open release: %w", err)
	}
	return f, nil
}

// Run rescans every RescanEvery until ctx ends.
func (r *Releases) Run(ctx context.Context) error {
	tick := time.NewTicker(RescanEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			r.Scan()
		}
	}
}
