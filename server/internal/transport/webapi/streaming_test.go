package webapi

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// A live stream must outlast the server's write timeout (against slow clients); other requests
// keep it.
func TestStreamingDeadline_LiftsTheWriteTimeoutOnlyForLiveStreams(t *testing.T) {
	stream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for range 4 {
			_, _ = w.Write([]byte("tick\n"))
			_ = http.NewResponseController(w).Flush()
			time.Sleep(150 * time.Millisecond)
		}
	})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: streamingDeadline(stream), WriteTimeout: 200 * time.Millisecond, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	read := func(path string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+ln.Addr().String()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		return len(body)
	}

	if got := read("/sermon.web.v1.MetricsService/WatchMetrics"); got != 4*len("tick\n") {
		t.Errorf("live stream got %d bytes, want all 4 ticks", got)
	}
	if got := read("/sermon.web.v1.LogService/TailLogs"); got != 4*len("tick\n") {
		t.Errorf("log tail got %d bytes, want all 4 ticks", got)
	}
	if got := read("/sermon.web.v1.LogService/QueryLogs"); got >= 4*len("tick\n") {
		t.Errorf("log queries must keep the write timeout, got %d bytes", got)
	}
	if got := read("/sermon.web.v1.MetricsService/QueryMetrics"); got >= 4*len("tick\n") {
		t.Errorf("other requests must keep the write timeout, got %d bytes", got)
	}
}
