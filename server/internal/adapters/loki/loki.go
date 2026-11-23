// Package loki is the log store (ADR 0031, KONZEPT §4.3): pushes and queries for one fixed Loki
// endpoint of the operator, with the organization as tenant (X-Scope-OrgID).
//
// The client goes to that endpoint only: no proxy from the environment, no redirects, fixed
// timeouts and bounded answers. Labels are few and sanitized (node_id, source, unit, severity);
// everything else is structured metadata.
package loki

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// MaxLabelBytes bounds a label value (the unit comes from the host).
const MaxLabelBytes = 64

const (
	tenantHeader  = "X-Scope-OrgID"
	encodingFlags = "X-Loki-Response-Encoding-Flags"
	// Bytes of an error answer kept in the error.
	maxErrorBody = 256
	// Bytes of a query answer read at most.
	maxQueryBody = 32 << 20
	timeout      = 30 * time.Second
)

// Errors of the store.
var (
	// ErrRateLimited: Loki refused for the tenant's limits (429); retry later.
	ErrRateLimited = errors.New("loki: rate limited")
	// ErrRejected: Loki refused the request (4xx, a redirect); retrying won't help.
	ErrRejected = errors.New("loki: rejected")
	// ErrUnavailable: Loki is not reachable or failed (5xx); retry later.
	ErrUnavailable = errors.New("loki: unavailable")
	// ErrNoQuery: a query without an expression.
	ErrNoQuery = errors.New("loki: empty query")
)

// Labels are the labels of a stream.
type Labels struct {
	Node     identity.NodeID
	Source   string // journal, file
	Unit     string
	Severity string
}

// Entry is one log line.
type Entry struct {
	At       time.Time
	Line     string
	Metadata map[string]string
}

// Stream is a set of entries with the same labels.
type Stream struct {
	Labels  Labels
	Entries []Entry
}

// Direction is the order of a query.
type Direction string

// Directions.
const (
	Backward Direction = "backward" // newest first
	Forward  Direction = "forward"
)

// QueryRange is a query over a time range.
type QueryRange struct {
	Query      logql.Query
	Start, End time.Time
	Limit      int
	Direction  Direction
}

// Line is one line of a query answer.
type Line struct {
	Labels   map[string]string
	At       time.Time
	Line     string
	Metadata map[string]string
}

// Client talks to one Loki instance.
type Client struct {
	base *url.URL
	http *http.Client
}

// New returns a client for the Loki at base (config.LokiURL).
func New(base *url.URL) *Client {
	transport := &http.Transport{
		Proxy:                 nil, // never a proxy from the environment
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Client{
		base: base,
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			// Only the configured endpoint: a redirect is an error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Push stores streams for the organization.
func (c *Client) Push(ctx context.Context, org tenant.OrgID, streams []Stream) error {
	if len(streams) == 0 {
		return nil
	}
	type jsonStream struct {
		Stream map[string]string `json:"stream"`
		Values [][]any           `json:"values"`
	}
	body := struct {
		Streams []jsonStream `json:"streams"`
	}{Streams: make([]jsonStream, 0, len(streams))}
	for _, s := range streams {
		values := make([][]any, 0, len(s.Entries))
		for _, e := range s.Entries {
			value := []any{strconv.FormatInt(e.At.UnixNano(), 10), e.Line}
			if len(e.Metadata) > 0 {
				value = append(value, e.Metadata)
			}
			values = append(values, value)
		}
		body.Streams = append(body.Streams, jsonStream{Stream: s.Labels.loki(), Values: values})
	}
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if err := json.NewEncoder(zw).Encode(body); err != nil {
		return fmt.Errorf("loki: encode push: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("loki: compress push: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/loki/api/v1/push"), &compressed)
	if err != nil {
		return fmt.Errorf("loki: push request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set(tenantHeader, org.String())
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	// Read the (empty) answer, so that the connection is reused.
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxErrorBody)) }()
	return check(res)
}

// Query reads lines of the organization.
func (c *Client) Query(ctx context.Context, org tenant.OrgID, q QueryRange) ([]Line, error) {
	if q.Query.String() == "" {
		return nil, ErrNoQuery
	}
	params := url.Values{}
	params.Set("query", q.Query.String())
	params.Set("start", strconv.FormatInt(q.Start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(q.End.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(q.Limit))
	if q.Direction != "" {
		params.Set("direction", string(q.Direction))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/loki/api/v1/query_range")+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("loki: query request: %w", err)
	}
	req.Header.Set(tenantHeader, org.String())
	req.Header.Set(encodingFlags, "categorize-labels")
	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if err := check(res); err != nil {
		return nil, err
	}
	lines, err := decodeStreams(io.LimitReader(res.Body, maxQueryBody))
	if err != nil {
		return nil, err
	}
	// Loki groups the lines by stream; the order holds over all of them.
	slices.SortStableFunc(lines, func(a, b Line) int {
		if q.Direction == Forward {
			return a.At.Compare(b.At)
		}
		return b.At.Compare(a.At)
	})
	return lines, nil
}

// Point is one value of a metric series.
type Point struct {
	At    time.Time
	Value float64
}

// Series is a metric series of a query answer.
type Series struct {
	Labels map[string]string
	Points []Point
}

// Counts runs a metric query (e.g. logql.CountBySeverity) over a time range.
func (c *Client) Counts(ctx context.Context, org tenant.OrgID, q logql.Query, start, end time.Time, step time.Duration) ([]Series, error) {
	if q.String() == "" {
		return nil, ErrNoQuery
	}
	params := url.Values{}
	params.Set("query", q.String())
	params.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	params.Set("step", strconv.FormatInt(int64(step/time.Second), 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/loki/api/v1/query_range")+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("loki: count request: %w", err)
	}
	req.Header.Set(tenantHeader, org.String())
	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if err := check(res); err != nil {
		return nil, err
	}
	return decodeMatrix(io.LimitReader(res.Body, maxQueryBody))
}

func decodeMatrix(r io.Reader) ([]Series, error) {
	var answer struct {
		Data struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string    `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&answer); err != nil {
		return nil, fmt.Errorf("%w: unreadable answer: %w", ErrRejected, err)
	}
	if answer.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("%w: answer of type %q", ErrRejected, answer.Data.ResultType)
	}
	out := make([]Series, 0, len(answer.Data.Result))
	for _, result := range answer.Data.Result {
		series := Series{Labels: result.Metric, Points: make([]Point, 0, len(result.Values))}
		for _, v := range result.Values {
			var seconds float64
			var text string
			if err := json.Unmarshal(v[0], &seconds); err != nil {
				return nil, fmt.Errorf("%w: point time: %w", ErrRejected, err)
			}
			if err := json.Unmarshal(v[1], &text); err != nil {
				return nil, fmt.Errorf("%w: point value: %w", ErrRejected, err)
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: point value: %w", ErrRejected, err)
			}
			whole := int64(seconds)
			at := time.Unix(whole, int64((seconds-float64(whole))*1e9)).UTC()
			series.Points = append(series.Points, Point{At: at, Value: value})
		}
		out = append(out, series)
	}
	return out, nil
}

// Delete asks Loki to delete the lines of q between start and end in the organization's tenant
// (the compactor's delete API, M3-10). Loki filters them from queries at once and removes them
// from its storage after the cancel period; asking twice is no error.
func (c *Client) Delete(ctx context.Context, org tenant.OrgID, q logql.Query, start, end time.Time) error {
	if q.String() == "" {
		return ErrNoQuery
	}
	// Loki refuses deletes in the future and starts near the epoch; nothing is older than
	// DeleteFrom (the schema starts 2026-01-01, deploy/compose/loki.yaml).
	if now := time.Now(); end.After(now) {
		end = now
	}
	if start.Before(DeleteFrom) {
		start = DeleteFrom
	}
	params := url.Values{}
	params.Set("query", q.String())
	params.Set("start", strconv.FormatInt(start.Unix(), 10))
	params.Set("end", strconv.FormatInt(end.Unix(), 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/loki/api/v1/delete")+"?"+params.Encode(), nil)
	if err != nil {
		return fmt.Errorf("loki: delete request: %w", err)
	}
	req.Header.Set(tenantHeader, org.String())
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	defer func() { _, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxErrorBody)) }()
	return check(res)
}

// DeleteFrom is the earliest start of a delete request: before the schema of SerMon's Loki.
var DeleteFrom = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// Deletion is a delete request of a tenant as Loki lists it.
type Deletion struct {
	Query      string
	Start, End time.Time
	// Status is received, processed or, in between, partially processed.
	Status string
}

// Deletions lists the delete requests of the organization's tenant.
func (c *Client) Deletions(ctx context.Context, org tenant.OrgID) ([]Deletion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/loki/api/v1/delete"), nil)
	if err != nil {
		return nil, fmt.Errorf("loki: deletions request: %w", err)
	}
	req.Header.Set(tenantHeader, org.String())
	res, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if err := check(res); err != nil {
		return nil, err
	}
	var answer []struct {
		Query     string  `json:"query"`
		StartTime float64 `json:"start_time"`
		EndTime   float64 `json:"end_time"`
		Status    string  `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxQueryBody)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("%w: unreadable answer: %w", ErrRejected, err)
	}
	out := make([]Deletion, 0, len(answer))
	for _, a := range answer {
		out = append(out, Deletion{
			Query: a.Query, Status: a.Status,
			Start: time.UnixMilli(int64(a.StartTime * 1000)).UTC(), End: time.UnixMilli(int64(a.EndTime * 1000)).UTC(),
		})
	}
	return out, nil
}

// Ready reports whether Loki takes requests (/ready, for /readyz).
func (c *Client) Ready(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/ready"), nil)
	if err != nil {
		return fmt.Errorf("loki: ready request: %w", err)
	}
	res, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	return check(res)
}

func (c *Client) endpoint(path string) string {
	u := *c.base
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	return u.String()
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	res, err := c.http.Do(req)
	if err != nil {
		// The URL is the operator's and holds no credentials (config); the cause stays generic.
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, errorCause(err))
	}
	return res, nil
}

func errorCause(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "connection failed"
}

// check maps the status; an error keeps the start of the body (Loki's reason).
func check(res *http.Response) error {
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return nil
	}
	data, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	reason := strings.ToValidUTF8(strings.TrimSpace(string(data)), "\uFFFD")
	var kind error
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		kind = ErrRateLimited
	case res.StatusCode >= 500:
		kind = ErrUnavailable
	default:
		kind = ErrRejected
	}
	return fmt.Errorf("%w: status %d: %s", kind, res.StatusCode, reason)
}

func (l Labels) loki() map[string]string {
	return map[string]string{
		"node_id":  l.Node.String(),
		"source":   labelValue(l.Source, "unknown"),
		"unit":     labelValue(l.Unit, "-"),
		"severity": labelValue(l.Severity, "unknown"),
	}
}

// labelValue keeps [A-Za-z0-9@:._-], replaces anything else with _, and cuts to MaxLabelBytes.
func labelValue(v, empty string) string {
	if v == "" {
		return empty
	}
	var b strings.Builder
	for _, r := range v {
		if b.Len() >= MaxLabelBytes {
			break
		}
		switch {
		case r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@:._-", r)):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// decodeStreams reads a query_range answer of type streams with categorized labels.
func decodeStreams(r io.Reader) ([]Line, error) {
	var answer struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Stream map[string]string   `json:"stream"`
				Values [][]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&answer); err != nil {
		return nil, fmt.Errorf("%w: unreadable answer: %w", ErrRejected, err)
	}
	if answer.Data.ResultType != "streams" {
		return nil, fmt.Errorf("%w: answer of type %q", ErrRejected, answer.Data.ResultType)
	}
	var lines []Line
	for _, stream := range answer.Data.Result {
		for _, value := range stream.Values {
			line, err := decodeValue(stream.Stream, value)
			if err != nil {
				return nil, err
			}
			lines = append(lines, line)
		}
	}
	return lines, nil
}

func decodeValue(labels map[string]string, value []json.RawMessage) (Line, error) {
	if len(value) < 2 {
		return Line{}, fmt.Errorf("%w: a value without line", ErrRejected)
	}
	var stamp, text string
	if err := json.Unmarshal(value[0], &stamp); err != nil {
		return Line{}, fmt.Errorf("%w: timestamp: %w", ErrRejected, err)
	}
	ns, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return Line{}, fmt.Errorf("%w: timestamp: %w", ErrRejected, err)
	}
	if err := json.Unmarshal(value[1], &text); err != nil {
		return Line{}, fmt.Errorf("%w: line: %w", ErrRejected, err)
	}
	line := Line{Labels: labels, At: time.Unix(0, ns).UTC(), Line: text}
	if len(value) > 2 {
		var categories struct {
			StructuredMetadata map[string]string `json:"structuredMetadata"`
		}
		if err := json.Unmarshal(value[2], &categories); err != nil {
			return Line{}, fmt.Errorf("%w: metadata: %w", ErrRejected, err)
		}
		line.Metadata = categories.StructuredMetadata
	}
	return line, nil
}
