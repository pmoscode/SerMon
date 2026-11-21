package httppost_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/httppost"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

func local(t *testing.T) *safehttp.Client {
	t.Helper()
	p, err := safehttp.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	if err != nil {
		t.Fatal(err)
	}
	return safehttp.New(safehttp.Options{Policy: p})
}

func TestPost_SendsBodyAndHeaders(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	h := http.Header{"Content-Type": {"application/json"}, "X-Test": {"1"}}
	if err := httppost.Post(context.Background(), local(t), srv.URL+"/hook", h, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/hook" || got.Header.Get("X-Test") != "1" || got.Header.Get("Content-Type") != "application/json" || body != `{"a":1}` {
		t.Errorf("request %s %s %v %q", got.Method, got.URL, got.Header, body)
	}
}

func TestPost_ClassifiesTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		status    int
		permanent bool
		rate      bool
	}{{404, true, false}, {401, true, false}, {500, false, false}, {503, false, false}, {429, false, true}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, "no such hook\nsecond line")
		}))
		err := httppost.Post(context.Background(), local(t), srv.URL, nil, []byte("x"))
		srv.Close()
		var rl *notify.RateLimitedError
		switch {
		case err == nil:
			t.Errorf("HTTP %d: no error", tc.status)
		case notify.IsPermanent(err) != tc.permanent:
			t.Errorf("HTTP %d: permanent %v", tc.status, notify.IsPermanent(err))
		case tc.rate && (!errors.As(err, &rl) || rl.RetryAfter != 2*time.Minute):
			t.Errorf("HTTP %d: %v, want rate limited for 2 min", tc.status, err)
		case tc.status == 404 && (!strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "no such hook") || strings.Contains(err.Error(), "\n")):
			t.Errorf("HTTP 404: %q, want the status and the start of the answer on one line", err)
		}
	}
}

func TestPost_RefusesInternalTargets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	err := httppost.Post(context.Background(), safehttp.New(safehttp.Options{}), srv.URL, nil, []byte("x"))
	if !errors.Is(err, safehttp.ErrBlocked) || !notify.IsPermanent(err) {
		t.Errorf("loopback target: %v, want blocked for good", err)
	}
}

// Telegram puts the wait into the body of a 429; the Retry-After header wins when both are there.
func TestPostWith_ReadsTheWaitFromTheAnswer(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{{"", 17 * time.Second}, {"30", 30 * time.Second}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if tc.header != "" {
				w.Header().Set("Retry-After", tc.header)
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"retry_after":17}`)
		}))
		err := httppost.PostWith(context.Background(), local(t), srv.URL, nil, []byte("x"), httppost.Options{
			RetryAfter: func(answer []byte) string {
				if strings.Contains(string(answer), "17") {
					return "17"
				}
				return ""
			},
		})
		srv.Close()
		var rl *notify.RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != tc.want {
			t.Errorf("header %q: %v, want a wait of %v", tc.header, err, tc.want)
		}
	}
}
