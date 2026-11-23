// Package lokitest starts a throwaway Loki for integration tests (Testcontainers), with the image
// and the configuration of the production compose file (deploy/compose), so tests check what is
// deployed.
package lokitest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	once     sync.Once
	shared   *url.URL
	startErr error
)

// Shared starts one Loki per test binary (on first use) and returns its URL.
func Shared(t testing.TB) *url.URL {
	t.Helper()
	once.Do(func() { shared, _, startErr = start(context.Background()) })
	if startErr != nil {
		t.Fatalf("start test Loki: %v", startErr)
	}
	return shared
}

// Start starts a Loki of its own with flags on top of the production configuration (e.g. a
// compactor that runs every few seconds) and stops it at the end of the test.
func Start(t testing.TB, flags ...string) *url.URL {
	t.Helper()
	u, container, err := start(context.Background(), flags...)
	if err != nil {
		t.Fatalf("start test Loki: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	return u
}

var composeImage = regexp.MustCompile(`image:\s*(grafana/loki:\S+)`)

func start(ctx context.Context, flags ...string) (*url.URL, testcontainers.Container, error) {
	_, file, _, _ := runtime.Caller(0)
	deploy := filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "deploy", "compose")
	content, err := os.ReadFile(filepath.Join(deploy, "compose.yaml")) //nolint:gosec // G304: a file of the repository, next to this one
	if err != nil {
		return nil, nil, fmt.Errorf("read compose file: %w", err)
	}
	match := composeImage.FindSubmatch(content)
	if match == nil {
		return nil, nil, fmt.Errorf("no grafana/loki image in %s", deploy)
	}
	container, err := testcontainers.Run(ctx, string(match[1]),
		testcontainers.WithCmd(append([]string{"-config.file=/etc/loki/loki.yaml"}, flags...)...),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath: filepath.Join(deploy, "loki.yaml"), ContainerFilePath: "/etc/loki/loki.yaml", FileMode: 0o644,
		}),
		testcontainers.WithExposedPorts("3100/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/ready").WithPort("3100/tcp").WithStartupTimeout(2*time.Minute)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("run Loki: %w", err)
	}
	endpoint, err := container.PortEndpoint(ctx, "3100/tcp", "http")
	if err != nil {
		return nil, container, fmt.Errorf("loki endpoint: %w", err)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, container, fmt.Errorf("loki endpoint: %w", err)
	}
	return u, container, nil
}
