//go:build integration

package ntfy_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/ntfy"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// The image of the development environment (deploy/dev/compose.yaml), pinned by digest.
func ntfyImage(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	content, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "..", "deploy", "dev", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`image:\s*(binwiederhier/ntfy:\S+)`).FindSubmatch(content)
	if m == nil {
		t.Fatal("no ntfy image in deploy/dev/compose.yaml")
	}
	return string(m[1])
}

// A real ntfy server accepts what the notifier publishes and shows title, priority and link.
func TestNtfy_AgainstTheRealServer(t *testing.T) {
	ctx := context.Background()
	c, err := testcontainers.Run(ctx, ntfyImage(t), testcontainers.WithCmd("serve"), testcontainers.WithExposedPorts("80/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/v1/health").WithPort("80/tcp")))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "80/tcp")
	if err != nil {
		t.Fatal(err)
	}
	server := "http://" + host + ":" + port.Port()
	p, err := safehttp.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")})
	if err != nil {
		t.Fatal(err)
	}
	cfg := notify.Config{Settings: map[string]string{"server": server}, Secrets: map[string]string{"topic": "sermon-it-4f2b"}}
	if err := ntfy.New(safehttp.New(safehttp.Options{Policy: p})).Send(ctx, cfg, alertMessage(notify.Firing, notify.Critical)); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/sermon-it-4f2b/json?poll=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []struct {
		Title, Message, Click string
		Priority              int
		Tags                  []string
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var e struct {
			Event, Title, Message, Click string
			Priority                     int
			Tags                         []string
		}
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil && e.Event == "message" {
			got = append(got, struct {
				Title, Message, Click string
				Priority              int
				Tags                  []string
			}{e.Title, e.Message, e.Click, e.Priority, e.Tags})
		}
	}
	if len(got) != 1 || got[0].Title != "KRITISCH: Hohe CPU-Auslastung – web-01" || got[0].Priority != 5 || len(got[0].Tags) != 1 ||
		got[0].Click != "https://sermon.example/nodes/0193f5a0-0000-7000-8000-000000000001" {
		t.Errorf("ntfy shows %+v", got)
	}
}
