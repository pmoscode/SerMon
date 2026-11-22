// Command slack-mock stands in for Slack's incoming webhooks in the E2E demo (M2-27): the
// network alias hooks.slack.com points to it, and the server trusts its certificate through
// SSL_CERT_FILE. It answers every POST to /services/… like Slack ("ok") and keeps the bodies;
// the test reads them with their paths as a JSON array from GET /messages on a second, plain
// HTTP port.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const maxBody = 64 << 10 // Slack accepts far less; the server's payloads are a few KiB

func main() {
	type message struct {
		Path string          `json:"path"`
		Body json.RawMessage `json:"body"`
	}
	var (
		mu       sync.Mutex
		messages = []message{}
	)
	hooks := http.NewServeMux()
	hooks.HandleFunc("POST /services/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil || !json.Valid(body) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "invalid_payload", http.StatusBadRequest)
			return
		}
		mu.Lock()
		messages = append(messages, message{Path: r.URL.Path, Body: body})
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	})
	api := http.NewServeMux()
	api.HandleFunc("GET /messages", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(messages)
	})

	go serve(&http.Server{Addr: ":8080", Handler: api, ReadHeaderTimeout: 5 * time.Second}, "", "")
	serve(&http.Server{Addr: ":443", Handler: hooks, ReadHeaderTimeout: 5 * time.Second},
		os.Getenv("CERT_FILE"), os.Getenv("KEY_FILE"))
}

func serve(s *http.Server, cert, key string) {
	var err error
	if cert != "" {
		err = s.ListenAndServeTLS(cert, key)
	} else {
		err = s.ListenAndServe()
	}
	log.Fatal(err)
}
