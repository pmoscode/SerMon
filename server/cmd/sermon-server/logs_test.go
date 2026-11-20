package main

import (
	"bytes"
	"regexp"
	"sync"
	"testing"
	"time"
)

// syncBuffer collects the logs of a running server for concurrent reads.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitFor(t *testing.T, logs *syncBuffer, pattern *regexp.Regexp) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if m := pattern.FindStringSubmatch(logs.String()); m != nil {
			return m[1]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s not found in logs:\n%s", pattern, logs.String())
	return ""
}
