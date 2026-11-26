package download_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/transport/download"
)

// signingKey is a key like release-sign.sh's: the public line and a signer.
type signingKey struct {
	line string
	priv ed25519.PrivateKey
}

func newSigningKey(t *testing.T) signingKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signingKey{line: base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), bytes.Repeat([]byte{9}, 8)...), pub...)), priv: priv}
}

func (k signingKey) sign(data []byte, file, version string) []byte {
	sig := ed25519.Sign(k.priv, data)
	trusted := "timestamp:1\tfile:" + file + "\thashed:0\tversion:" + version
	global := ed25519.Sign(k.priv, append(bytes.Clone(sig), trusted...))
	return []byte("untrusted comment: test\n" + base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), bytes.Repeat([]byte{9}, 8)...), sig...)) +
		"\ntrusted comment: " + trusted + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

func put(t *testing.T, dir, name string, data, sig []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".minisig"), sig, 0o644); err != nil {
		t.Fatal(err)
	}
}

// ADR 0036: stable is the server's version, beta the one in beta/; only binaries the release key
// signed for their name count; a new beta is found when asked for, without a restart.
func TestReleases_KnowTheSignedBinariesPerChannel(t *testing.T) {
	dir := t.TempDir()
	key := newSigningKey(t)
	other := newSigningKey(t)
	put(t, dir, "sermon-agent-linux-arm64", []byte("stable arm64"), key.sign([]byte("stable arm64"), "sermon-agent-linux-arm64", "1.0.0"))
	put(t, dir, "sermon-agent-linux-amd64", []byte("stable amd64"), key.sign([]byte("stable amd64"), "sermon-agent-linux-amd64", "0.9.0")) // not the server's
	put(t, dir, "beta/sermon-agent-linux-arm64", []byte("beta arm64"), key.sign([]byte("beta arm64"), "sermon-agent-linux-arm64", "1.1.0-rc.1"))
	put(t, dir, "beta/sermon-agent-linux-amd64", []byte("beta amd64"), other.sign([]byte("beta amd64"), "sermon-agent-linux-amd64", "1.1.0-rc.1"))
	releases, err := download.NewReleases(dir, key.line, "1.0.0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	stable, ok := releases.Latest("stable", "arm64")
	if !ok || stable.Version != "1.0.0" || stable.Size != 12 {
		t.Errorf("stable = %+v, %v", stable, ok)
	}
	if beta, ok := releases.Find("1.1.0-rc.1", "arm64"); !ok || beta.Channel != "beta" || !bytes.Contains(beta.Signature, []byte("version:1.1.0-rc.1")) {
		t.Errorf("beta = %+v, %v", beta, ok)
	} else {
		f, err := releases.Open(beta)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(f)
		_ = f.Close()
		if string(data) != "beta arm64" {
			t.Errorf("opened %q", data)
		}
	}
	for _, c := range []struct{ version, arch string }{{"0.9.0", "amd64"}, {"1.1.0-rc.1", "amd64"}, {"1.0.0", "amd64"}} {
		if rel, ok := releases.Find(c.version, c.arch); ok {
			t.Errorf("%s %s found: %+v", c.version, c.arch, rel)
		}
	}
	// A signature for another file, a changed binary.
	put(t, dir, "beta/sermon-agent-linux-amd64", []byte("beta amd64"), key.sign([]byte("beta amd64"), "sermon-helper-linux-amd64", "1.2.0"))
	if err := os.WriteFile(filepath.Join(dir, "beta/sermon-agent-linux-arm64"), []byte("changed!!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "beta/sermon-agent-linux-arm64"), future, future)
	releases.Scan()
	if _, ok := releases.Find("1.1.0-rc.1", "arm64"); ok {
		t.Error("a changed binary is still known")
	}
	if _, ok := releases.Find("1.2.0", "amd64"); ok {
		t.Error("the helper's signature counts for the agent")
	}
	// A new beta, found on request.
	put(t, dir, "beta/sermon-agent-linux-amd64", []byte("beta 2"), key.sign([]byte("beta 2"), "sermon-agent-linux-amd64", "1.2.0"))
	if rel, ok := releases.Find("1.2.0", "amd64"); ok {
		t.Errorf("rescanned within 10 s: %+v", rel)
	}
	releases.Scan()
	if rel, ok := releases.Find("1.2.0", "amd64"); !ok || rel.Channel != "beta" {
		t.Errorf("new beta = %+v, %v", rel, ok)
	}
	// Without key or directory: nothing.
	empty, err := download.NewReleases("", key.line, "1.0.0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := empty.Latest("stable", "arm64"); ok {
		t.Error("an empty catalog knows a release")
	}
	if _, err := download.NewReleases(dir, "nonsense", "1.0.0", slog.New(slog.DiscardHandler)); err == nil {
		t.Error("an invalid key was taken")
	}
}

func open(t *testing.T, releases *download.Releases, rel download.Release) string {
	t.Helper()
	f, err := releases.Open(rel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// M4-31: a release brings the root helper of the same channel, signed by the release key for its
// name and with the agent's version; any other helper is left out, the agent release stays.
func TestReleases_BringTheHelperOfTheSameVersion(t *testing.T) {
	dir := t.TempDir()
	key := newSigningKey(t)
	other := newSigningKey(t)
	put(t, dir, "sermon-agent-linux-arm64", []byte("agent arm64"), key.sign([]byte("agent arm64"), "sermon-agent-linux-arm64", "1.0.0"))
	put(t, dir, "sermon-helper-linux-arm64", []byte("helper arm64"), key.sign([]byte("helper arm64"), "sermon-helper-linux-arm64", "1.0.0"))
	put(t, dir, "sermon-agent-linux-amd64", []byte("agent amd64"), key.sign([]byte("agent amd64"), "sermon-agent-linux-amd64", "1.0.0"))
	put(t, dir, "sermon-helper-linux-amd64", []byte("helper amd64"), key.sign([]byte("helper amd64"), "sermon-helper-linux-amd64", "0.9.0")) // another version
	put(t, dir, "beta/sermon-agent-linux-arm64", []byte("beta agent"), key.sign([]byte("beta agent"), "sermon-agent-linux-arm64", "1.1.0-rc.1"))
	put(t, dir, "beta/sermon-helper-linux-arm64", []byte("beta helper"), other.sign([]byte("beta helper"), "sermon-helper-linux-arm64", "1.1.0-rc.1")) // another key
	put(t, dir, "beta/sermon-agent-linux-amd64", []byte("beta agent"), key.sign([]byte("beta agent"), "sermon-agent-linux-amd64", "1.1.0-rc.1"))
	put(t, dir, "beta/sermon-helper-linux-amd64", []byte("beta helper"), key.sign([]byte("beta helper"), "sermon-agent-linux-amd64", "1.1.0-rc.1")) // the agent's name
	releases, err := download.NewReleases(dir, key.line, "1.0.0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	rel, ok := releases.Find("1.0.0", "arm64")
	if !ok || rel.Helper == nil {
		t.Fatalf("release = %+v, %v", rel, ok)
	}
	if h := *rel.Helper; h.Version != "1.0.0" || h.Arch != "arm64" || h.Size != 12 || !bytes.Contains(h.Signature, []byte("file:sermon-helper-linux-arm64")) {
		t.Errorf("helper = %+v", h)
	}
	if got := open(t, releases, *rel.Helper); got != "helper arm64" {
		t.Errorf("opened %q", got)
	}
	if got := open(t, releases, rel); got != "agent arm64" {
		t.Errorf("opened the agent as %q", got)
	}
	for _, c := range []struct{ version, arch string }{{"1.0.0", "amd64"}, {"1.1.0-rc.1", "arm64"}, {"1.1.0-rc.1", "amd64"}} {
		rel, ok := releases.Find(c.version, c.arch)
		if !ok || rel.Helper != nil {
			t.Errorf("%s %s = %+v, %v", c.version, c.arch, rel, ok)
		}
	}
	// A helper signed later is found by the next scan; a changed one is dropped.
	put(t, dir, "sermon-helper-linux-amd64", []byte("helper amd64"), key.sign([]byte("helper amd64"), "sermon-helper-linux-amd64", "1.0.0"))
	releases.Scan()
	if rel, _ := releases.Find("1.0.0", "amd64"); rel.Helper == nil {
		t.Error("the new helper is not found")
	}
	if err := os.WriteFile(filepath.Join(dir, "sermon-helper-linux-arm64"), []byte("changed helper"), 0o644); err != nil {
		t.Fatal(err)
	}
	releases.Scan()
	if rel, ok := releases.Find("1.0.0", "arm64"); !ok || rel.Helper != nil {
		t.Errorf("after the helper changed: %+v, %v", rel, ok)
	}
	// Bounds as for the agent.
	big := bytes.Repeat([]byte{0}, download.MaxBinary+1)
	put(t, dir, "sermon-helper-linux-amd64", big, key.sign(big, "sermon-helper-linux-amd64", "1.0.0"))
	releases.Scan()
	if rel, _ := releases.Find("1.0.0", "amd64"); rel.Helper != nil {
		t.Error("a helper larger than MaxBinary")
	}
}

// Review M4-31: beta is never behind stable – without a newer beta, beta offers stable.
func TestReleases_BetaFollowsANewerStable(t *testing.T) {
	dir := t.TempDir()
	key := newSigningKey(t)
	put(t, dir, "sermon-agent-linux-arm64", []byte("stable"), key.sign([]byte("stable"), "sermon-agent-linux-arm64", "1.1.0"))
	put(t, dir, "beta/sermon-agent-linux-arm64", []byte("beta"), key.sign([]byte("beta"), "sermon-agent-linux-arm64", "1.1.0-rc.2"))
	put(t, dir, "sermon-agent-linux-amd64", []byte("stable"), key.sign([]byte("stable"), "sermon-agent-linux-amd64", "1.1.0"))
	releases, err := download.NewReleases(dir, key.line, "1.1.0", slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"arm64", "amd64"} {
		if rel, ok := releases.Latest("beta", arch); !ok || rel.Version != "1.1.0" || rel.Channel != "stable" {
			t.Errorf("beta %s = %+v, %v", arch, rel, ok)
		}
	}
	// As new is not newer.
	put(t, dir, "beta/sermon-agent-linux-amd64", []byte("beta"), key.sign([]byte("beta"), "sermon-agent-linux-amd64", "1.1.0"))
	put(t, dir, "beta/sermon-agent-linux-arm64", []byte("beta 2"), key.sign([]byte("beta 2"), "sermon-agent-linux-arm64", "1.2.0-rc.1"))
	releases.Scan()
	if rel, ok := releases.Latest("beta", "amd64"); !ok || rel.Channel != "stable" {
		t.Errorf("beta as new as stable = %+v, %v", rel, ok)
	}
	if rel, ok := releases.Latest("beta", "arm64"); !ok || rel.Version != "1.2.0-rc.1" || rel.Channel != "beta" {
		t.Errorf("newer beta = %+v, %v", rel, ok)
	}
	if rel, ok := releases.Latest("stable", "arm64"); !ok || rel.Version != "1.1.0" {
		t.Errorf("stable = %+v, %v", rel, ok)
	}
	// Without stable, beta stays.
	if err := os.Remove(filepath.Join(dir, "sermon-agent-linux-arm64")); err != nil {
		t.Fatal(err)
	}
	releases.Scan()
	if rel, ok := releases.Latest("beta", "arm64"); !ok || rel.Version != "1.2.0-rc.1" {
		t.Errorf("beta without stable = %+v, %v", rel, ok)
	}
	if _, ok := releases.Latest("nightly", "arm64"); ok {
		t.Error("an unknown channel")
	}
}
