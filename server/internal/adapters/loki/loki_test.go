package loki_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

var (
	org  = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	node = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001"))
	at   = time.Date(2026, 11, 23, 12, 0, 0, 123_456_789, time.UTC)
)

type recorded struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
}

func (r *recorded) handler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		data, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, req)
		r.bodies = append(r.bodies, data)
		r.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func client(t *testing.T, h http.Handler) *loki.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL + "/prefix")
	return loki.New(base)
}

func stream() loki.Stream {
	return loki.Stream{
		Labels: loki.Labels{Node: node, Source: "journal", Unit: `nginx.service"} or {x="1`, Severity: "error"},
		Entries: []loki.Entry{{
			At: at, Line: "upstream timed out",
			Metadata: map[string]string{"pid": "812", "process": "nginx"},
		}},
	}
}

type pushBody struct {
	Streams []struct {
		Stream map[string]string `json:"stream"`
		Values [][]any           `json:"values"`
	} `json:"streams"`
}

func TestPush_SendsGzipJSONWithTheTenantAndSanitizedLabels(t *testing.T) {
	rec := &recorded{}
	c := client(t, rec.handler(http.StatusNoContent, ""))
	if err := c.Push(context.Background(), org, []loki.Stream{stream()}); err != nil {
		t.Fatal(err)
	}
	req := rec.requests[0]
	if req.Method != http.MethodPost || req.URL.Path != "/prefix/loki/api/v1/push" {
		t.Errorf("request = %s %s", req.Method, req.URL.Path)
	}
	if req.Header.Get("X-Scope-OrgID") != org.String() || req.Header.Get("Content-Encoding") != "gzip" ||
		req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", req.Header)
	}
	zr, err := gzip.NewReader(strings.NewReader(string(rec.bodies[0])))
	if err != nil {
		t.Fatal(err)
	}
	var body pushBody
	if err := json.NewDecoder(zr).Decode(&body); err != nil {
		t.Fatal(err)
	}
	labels := body.Streams[0].Stream
	if len(labels) != 4 || labels["node_id"] != node.String() || labels["source"] != "journal" || labels["severity"] != "error" {
		t.Errorf("labels = %v, want exactly node_id, source, unit, severity", labels)
	}
	if got := labels["unit"]; got != "nginx.service___or__x__1" {
		t.Errorf("unit = %q, want the characters outside the allowed set replaced", got)
	}
	value := body.Streams[0].Values[0]
	if value[0] != "1795435200123456789" || value[1] != "upstream timed out" {
		t.Errorf("value = %v, want nanoseconds and the line", value)
	}
	if meta, _ := value[2].(map[string]any); meta["pid"] != "812" || meta["process"] != "nginx" {
		t.Errorf("structured metadata = %v", value[2])
	}
}

func TestLabels_AreBoundedAndNeverEmpty(t *testing.T) {
	rec := &recorded{}
	c := client(t, rec.handler(http.StatusNoContent, ""))
	long := stream()
	long.Labels.Unit = strings.Repeat("u", 300)
	empty := stream()
	empty.Labels.Unit, empty.Labels.Severity = "", ""
	if err := c.Push(context.Background(), org, []loki.Stream{long, empty}); err != nil {
		t.Fatal(err)
	}
	zr, _ := gzip.NewReader(strings.NewReader(string(rec.bodies[0])))
	var body pushBody
	_ = json.NewDecoder(zr).Decode(&body)
	if got := body.Streams[0].Stream["unit"]; len(got) != loki.MaxLabelBytes {
		t.Errorf("long unit has %d bytes, want %d", len(got), loki.MaxLabelBytes)
	}
	if got := body.Streams[1].Stream; got["unit"] != "-" || got["severity"] != "unknown" {
		t.Errorf("empty labels = %v", got)
	}
}

func TestPush_MapsErrors(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusTooManyRequests:     loki.ErrRateLimited,
		http.StatusBadRequest:          loki.ErrRejected,
		http.StatusServiceUnavailable:  loki.ErrUnavailable,
		http.StatusInternalServerError: loki.ErrUnavailable,
	} {
		c := client(t, (&recorded{}).handler(status, strings.Repeat("too far behind ", 1000)))
		err := c.Push(context.Background(), org, []loki.Stream{stream()})
		if !errors.Is(err, want) {
			t.Errorf("%d: %v, want %v", status, err, want)
		}
		if err != nil && len(err.Error()) > 600 {
			t.Errorf("%d: the error carries %d bytes of the body", status, len(err.Error()))
		}
	}
	base, _ := url.Parse("http://127.0.0.1:1")
	if err := loki.New(base).Push(context.Background(), org, []loki.Stream{stream()}); !errors.Is(err, loki.ErrUnavailable) {
		t.Errorf("unreachable: %v", err)
	}
	c := client(t, (&recorded{}).handler(http.StatusNoContent, ""))
	if err := c.Push(context.Background(), org, nil); err != nil {
		t.Errorf("nothing to push: %v", err)
	}
}

// The client goes to the configured URL only: neither redirects nor a proxy from the environment.
func TestClient_FollowsNoRedirectAndUsesNoProxy(t *testing.T) {
	elsewhere := &recorded{}
	target := httptest.NewServer(elsewhere.handler(http.StatusNoContent, ""))
	t.Cleanup(target.Close)
	c := client(t, http.RedirectHandler(target.URL+"/loki/api/v1/push", http.StatusTemporaryRedirect))
	if err := c.Push(context.Background(), org, []loki.Stream{stream()}); !errors.Is(err, loki.ErrRejected) {
		t.Errorf("redirect: %v, want ErrRejected", err)
	}
	proxy := &recorded{}
	proxySrv := httptest.NewServer(proxy.handler(http.StatusNoContent, ""))
	t.Cleanup(proxySrv.Close)
	t.Setenv("HTTP_PROXY", proxySrv.URL)
	t.Setenv("http_proxy", proxySrv.URL)
	direct := &recorded{}
	c = client(t, direct.handler(http.StatusNoContent, ""))
	if err := c.Push(context.Background(), org, []loki.Stream{stream()}); err != nil {
		t.Fatal(err)
	}
	if len(elsewhere.requests) != 0 || len(proxy.requests) != 0 || len(direct.requests) != 1 {
		t.Errorf("requests: redirect target %d, proxy %d, Loki %d", len(elsewhere.requests), len(proxy.requests), len(direct.requests))
	}
}

func TestQuery_SendsTheTypedQueryAndReadsCategorizedLines(t *testing.T) {
	rec := &recorded{}
	response := `{"status":"success","data":{"resultType":"streams","encodingFlags":["categorize-labels"],"result":[
		{"stream":{"node_id":"` + node.String() + `","unit":"nginx.service"},
		 "values":[["1795435200123456789","upstream timed out",{"structuredMetadata":{"pid":"812"}}],["1795435199000000000","started"]]}]}}`
	c := client(t, rec.handler(http.StatusOK, response))
	q, _ := logql.Select([]identity.NodeID{node})
	lines, err := c.Query(context.Background(), org, loki.QueryRange{
		Query: q, Start: at.Add(-time.Hour), End: at, Limit: 100, Direction: loki.Backward,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := rec.requests[0]
	// M3-29: a POST form, so thousands of node IDs don't hit the limits of a URL.
	params, _ := url.ParseQuery(string(rec.bodies[0]))
	if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || req.URL.RawQuery != "" {
		t.Errorf("request %s %s, %s", req.Method, req.URL, req.Header.Get("Content-Type"))
	}
	if req.URL.Path != "/prefix/loki/api/v1/query_range" || params.Get("query") != q.String() ||
		params.Get("limit") != "100" || params.Get("direction") != "backward" ||
		params.Get("start") != "1795431600123456789" || params.Get("end") != "1795435200123456789" {
		t.Errorf("request = %s", req.URL)
	}
	if req.Header.Get("X-Scope-OrgID") != org.String() || req.Header.Get("X-Loki-Response-Encoding-Flags") != "categorize-labels" {
		t.Errorf("headers = %v", req.Header)
	}
	if len(lines) != 2 || !lines[0].At.Equal(at) || lines[0].Line != "upstream timed out" ||
		lines[0].Metadata["pid"] != "812" || lines[0].Labels["unit"] != "nginx.service" || lines[1].Metadata != nil {
		t.Errorf("lines = %+v", lines)
	}
	if _, err := c.Query(context.Background(), org, loki.QueryRange{End: at, Start: at.Add(-time.Hour), Limit: 1}); !errors.Is(err, loki.ErrNoQuery) {
		t.Errorf("empty query: %v, want ErrNoQuery", err)
	}
}

func TestQuery_RejectsMalformedAnswers(t *testing.T) {
	q, _ := logql.Select([]identity.NodeID{node})
	for name, body := range map[string]string{
		"not json":     "<html>",
		"bad time":     `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["x","l"]]}]}}`,
		"short value":  `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[["1"]]}]}}`,
		"other result": `{"status":"success","data":{"resultType":"matrix","result":[]}}`,
	} {
		c := client(t, (&recorded{}).handler(http.StatusOK, body))
		if _, err := c.Query(context.Background(), org, loki.QueryRange{Query: q, Start: at.Add(-time.Hour), End: at, Limit: 1}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestReady(t *testing.T) {
	rec := &recorded{}
	c := client(t, rec.handler(http.StatusOK, "ready"))
	if err := c.Ready(context.Background()); err != nil || rec.requests[0].URL.Path != "/prefix/ready" || rec.requests[0].Header.Get("X-Scope-OrgID") != "" {
		t.Errorf("Ready() = %v, request %v", err, rec.requests[0].URL)
	}
	c = client(t, (&recorded{}).handler(http.StatusServiceUnavailable, "Ingester not ready"))
	if err := c.Ready(context.Background()); err == nil {
		t.Error("not ready: no error")
	}
}

func TestCounts_ReadsTheMatrixOfAMetricQuery(t *testing.T) {
	rec := &recorded{}
	response := `{"status":"success","data":{"resultType":"matrix","result":[
		{"metric":{"severity":"error"},"values":[[1795435200,"3"],[1795435260.5,"1"]]},
		{"metric":{"severity":"info"},"values":[[1795435200,"12"]]}]}}`
	c := client(t, rec.handler(http.StatusOK, response))
	q, _ := logql.CountBySeverity(logql.Filter{Nodes: []identity.NodeID{node}}, time.Minute)
	series, err := c.Counts(context.Background(), org, q, at.Add(-time.Hour), at, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	params, _ := url.ParseQuery(string(rec.bodies[0]))
	if rec.requests[0].Method != http.MethodPost || params.Get("step") != "60" || params.Get("query") != q.String() || rec.requests[0].Header.Get("X-Scope-OrgID") != org.String() {
		t.Errorf("request = %s", rec.requests[0].URL)
	}
	if len(series) != 2 || series[0].Labels["severity"] != "error" || len(series[0].Points) != 2 ||
		series[0].Points[1].Value != 1 || !series[0].Points[1].At.Equal(time.Unix(1795435260, 500_000_000).UTC()) {
		t.Errorf("series = %+v", series)
	}
	bad := client(t, (&recorded{}).handler(http.StatusOK, `{"status":"success","data":{"resultType":"streams","result":[]}}`))
	if _, err := bad.Counts(context.Background(), org, q, at.Add(-time.Hour), at, time.Minute); !errors.Is(err, loki.ErrRejected) {
		t.Errorf("streams instead of a matrix: %v", err)
	}
}

// Loki groups the lines of an answer by stream; the client returns them in the asked order over
// all streams.
func TestQuery_OrdersTheLinesOverAllStreams(t *testing.T) {
	response := `{"status":"success","data":{"resultType":"streams","result":[
		{"stream":{"unit":"a"},"values":[["1795435200000000003","a3"],["1795435200000000001","a1"]]},
		{"stream":{"unit":"b"},"values":[["1795435200000000004","b4"],["1795435200000000002","b2"]]}]}}`
	c := client(t, (&recorded{}).handler(http.StatusOK, response))
	q, _ := logql.Select([]identity.NodeID{node})
	for direction, want := range map[loki.Direction]string{loki.Backward: "b4 a3 b2 a1", "": "b4 a3 b2 a1", loki.Forward: "a1 b2 a3 b4"} {
		lines, err := c.Query(context.Background(), org, loki.QueryRange{Query: q, Start: at.Add(-time.Hour), End: at, Limit: 10, Direction: direction})
		if err != nil {
			t.Fatal(err)
		}
		texts := make([]string, 0, len(lines))
		for _, l := range lines {
			texts = append(texts, l.Line)
		}
		if got := strings.Join(texts, " "); got != want {
			t.Errorf("%q: %s, want %s", direction, got, want)
		}
	}
}

func TestDelete_AsksTheCompactorInTheTenant(t *testing.T) {
	rec := &recorded{}
	c := client(t, rec.handler(http.StatusNoContent, ""))
	before := time.Now()
	if err := c.Delete(context.Background(), org, logql.Everything(), time.Unix(0, 0), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	req := rec.requests[0]
	params := req.URL.Query()
	if req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/loki/api/v1/delete") || req.Header.Get("X-Scope-OrgID") != org.String() ||
		params.Get("query") != `{node_id=~".+"}` {
		t.Errorf("request = %s %s", req.Method, req.URL)
	}
	start, _ := strconv.ParseInt(params.Get("start"), 10, 64)
	end, _ := strconv.ParseInt(params.Get("end"), 10, 64)
	if start != loki.DeleteFrom.Unix() || end > time.Now().Unix() || end < before.Unix() {
		t.Errorf("start %d, end %d: not clamped", start, end)
	}
	down := client(t, (&recorded{}).handler(http.StatusServiceUnavailable, ""))
	if err := down.Delete(context.Background(), org, logql.Everything(), loki.DeleteFrom, time.Now()); !errors.Is(err, loki.ErrUnavailable) {
		t.Errorf("Loki down: %v", err)
	}
	if err := c.Delete(context.Background(), org, logql.Query{}, loki.DeleteFrom, time.Now()); !errors.Is(err, loki.ErrNoQuery) {
		t.Errorf("no query: %v", err)
	}
}

// M5-29: the eraser deletes the streams of one node in its organization's tenant only.
func TestEraser_EraseNodeAsksForTheNodesStreams(t *testing.T) {
	rec := &recorded{}
	e := loki.NewEraser(client(t, rec.handler(http.StatusNoContent, "")))
	if err := e.EraseNode(context.Background(), org, node); err != nil {
		t.Fatal(err)
	}
	req := rec.requests[0]
	if req.Method != http.MethodPost || req.Header.Get("X-Scope-OrgID") != org.String() ||
		req.URL.Query().Get("query") != `{node_id=~"`+node.String()+`"}` || req.URL.Query().Get("start") != strconv.FormatInt(loki.DeleteFrom.Unix(), 10) {
		t.Errorf("request = %s %s (tenant %q)", req.Method, req.URL, req.Header.Get("X-Scope-OrgID"))
	}
	down := loki.NewEraser(client(t, (&recorded{}).handler(http.StatusServiceUnavailable, "")))
	if err := down.EraseNode(context.Background(), org, node); !errors.Is(err, loki.ErrUnavailable) {
		t.Errorf("Loki down: %v", err)
	}
}

// M5-30: an organization's own retention asks for the deletion of its lines before it, from a day
// before the instance's retention, in its tenant only.
func TestEraser_EraseBeforeAsksForTheOrganizationsOldLines(t *testing.T) {
	rec := &recorded{}
	e := loki.NewEraser(client(t, rec.handler(http.StatusNoContent, "")))
	before := time.Now().Add(-72 * time.Hour)
	if err := e.EraseBefore(context.Background(), org, before); err != nil {
		t.Fatal(err)
	}
	req := rec.requests[0]
	params := req.URL.Query()
	start, _ := strconv.ParseInt(params.Get("start"), 10, 64)
	end, _ := strconv.ParseInt(params.Get("end"), 10, 64)
	if req.Header.Get("X-Scope-OrgID") != org.String() || params.Get("query") != `{node_id=~".+"}` || end != before.Unix() {
		t.Errorf("request = %s (tenant %q)", req.URL, req.Header.Get("X-Scope-OrgID"))
	}
	if want := time.Now().Add(-15 * 24 * time.Hour).Unix(); start < want-5 || start > want+5 {
		t.Errorf("start = %d, want 15 days back (%d)", start, want)
	}
}

// M3-29: an answer beyond the bound is ErrTooLarge (the caller asks for fewer lines), not a
// broken answer.
func TestQuery_AnAnswerBeyondTheBoundIsTooLarge(t *testing.T) {
	q, _ := logql.Select([]identity.NodeID{node})
	line := strings.Repeat("x", 1<<20)
	var values []string
	for range loki.MaxQueryBody>>20 + 1 {
		values = append(values, `["1795435200123456789","`+line+`"]`)
	}
	body := `{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[` + strings.Join(values, ",") + `]}]}}`
	c := client(t, (&recorded{}).handler(http.StatusOK, body))
	if _, err := c.Query(context.Background(), org, loki.QueryRange{Query: q, Start: at.Add(-time.Hour), End: at, Limit: 1000}); !errors.Is(err, loki.ErrTooLarge) {
		t.Errorf("Query() = %v, want ErrTooLarge", err)
	}
}
