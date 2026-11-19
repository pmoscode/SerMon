package spa_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pmoscode/SerMon/server/internal/transport/spa"
)

const (
	bootstrap = `import("/_app/immutable/entry/start.js")`
	indexHTML = `<!doctype html><html><head>` +
		`<script type="module" src="/_app/immutable/entry/app.js"></script>` +
		`</head><body><script>` + bootstrap + `</script></body></html>`
)

func testFiles() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                       {Data: []byte(indexHTML)},
		"robots.txt":                       {Data: []byte("User-agent: *\nDisallow: /\n")},
		"_app/immutable/entry/app.js":      {Data: []byte("console.log('app')")},
		"_app/immutable/assets/font.woff2": {Data: []byte("woff2")},
	}
}

func newHandler(t *testing.T) *spa.Handler {
	t.Helper()
	h, err := spa.New(testFiles())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return h
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), method, target, nil))
	return rec
}

func sha256Source(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

func TestNew_FailsWithoutIndex(t *testing.T) {
	_, err := spa.New(fstest.MapFS{".gitkeep": {}})

	if !errors.Is(err, spa.ErrNoIndex) {
		t.Errorf("New() error = %v, want ErrNoIndex", err)
	}
}

func TestServe_IndexWithStrictCSPAndSecurityHeaders(t *testing.T) {
	rec := do(t, newHandler(t), http.MethodGet, "/")

	if rec.Code != http.StatusOK || rec.Body.String() != indexHTML {
		t.Fatalf("GET / = %d %q, want 200 with index.html", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'",
		"script-src 'self' " + sha256Source(bootstrap),
		"style-src 'self'",
		"font-src 'self'",
		"connect-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP %q lacks %q", csp, directive)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "http") {
		t.Errorf("CSP must not allow inline code or foreign origins: %q", csp)
	}
	wantHeaders := map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "no-cache",
		"Content-Type":                 "text/html; charset=utf-8",
	}
	for name, want := range wantHeaders {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if rec.Header().Get("Permissions-Policy") == "" {
		t.Error("Permissions-Policy header missing")
	}
}

func TestServe_ClientRoutesFallBackToIndex(t *testing.T) {
	for _, target := range []string{"/nodes/42", "/settings/users", "/_app", "/../../etc/passwd"} {
		rec := do(t, newHandler(t), http.MethodGet, target)

		if rec.Code != http.StatusOK || rec.Body.String() != indexHTML {
			t.Errorf("GET %s = %d %q, want 200 with index.html", target, rec.Code, rec.Body.String())
		}
	}
}

func TestServe_ImmutableAssetsAreCachedForever(t *testing.T) {
	rec := do(t, newHandler(t), http.MethodGet, "/_app/immutable/entry/app.js")

	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('app')" {
		t.Fatalf("GET app.js = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q, want immutable", got)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/javascript") {
		t.Errorf("Content-Type = %q, want text/javascript", got)
	}
}

func TestServe_OtherFilesAreRevalidated(t *testing.T) {
	rec := do(t, newHandler(t), http.MethodGet, "/robots.txt")

	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("GET /robots.txt = %d, Cache-Control %q, want 200 no-cache", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestServe_MissingFilesAreNotFound(t *testing.T) {
	for _, target := range []string{"/missing.js", "/_app/immutable/nope.css"} {
		rec := do(t, newHandler(t), http.MethodGet, target)

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", target, rec.Code)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: security headers missing on 404", target)
		}
	}
}

func TestServe_HeadReturnsNoBody(t *testing.T) {
	rec := do(t, newHandler(t), http.MethodHead, "/")

	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD / = %d with %d bytes, want 200 without body", rec.Code, rec.Body.Len())
	}
}

func TestInlineScriptHashes_IgnoresExternalScripts(t *testing.T) {
	html := `<script src="/a.js"></script><script>one()</script><script type="module">two()</script>`

	got := spa.InlineScriptHashes([]byte(html))

	want := []string{sha256Source("one()"), sha256Source("two()")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("InlineScriptHashes() = %v, want %v", got, want)
	}
}

func TestStyleAttributeHashes_FindsStaticStyleAttributesInBundles(t *testing.T) {
	files := fstest.MapFS{
		"index.html":           {Data: []byte(indexHTML)},
		"_app/immutable/a.js":  {Data: []byte("var t=p(`<div id=\"x\" style=\"position: absolute; left: 0\"></div>`)")},
		"_app/immutable/b.js":  {Data: []byte("var u=p(`<span style=\"width: 1px\"></span><i style=\"position: absolute; left: 0\"></i>`)")},
		"_app/immutable/c.css": {Data: []byte(`.ignored{}`)},
	}

	got, err := spa.StyleAttributeHashes(files)
	if err != nil {
		t.Fatalf("StyleAttributeHashes() error = %v", err)
	}

	want := []string{sha256Source("position: absolute; left: 0"), sha256Source("width: 1px")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("StyleAttributeHashes() = %v, want %v (deduplicated, in order of appearance)", got, want)
	}
}

func TestServe_CSPAllowsOnlyTheBundledStyleAttributes(t *testing.T) {
	files := testFiles()
	files["_app/immutable/announcer.js"] = &fstest.MapFile{Data: []byte("p(`<div style=\"width: 1px\"></div>`)")}
	h, err := spa.New(files)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	csp := do(t, h, http.MethodGet, "/").Header().Get("Content-Security-Policy")

	if !strings.Contains(csp, "style-src 'self' 'unsafe-hashes' "+sha256Source("width: 1px")) {
		t.Errorf("CSP %q does not allow exactly the bundled style attribute", csp)
	}
}

func TestServe_CSPWithoutStyleAttributesHasNoHashExceptions(t *testing.T) {
	csp := do(t, newHandler(t), http.MethodGet, "/").Header().Get("Content-Security-Policy")

	if !strings.Contains(csp, "style-src 'self';") || strings.Contains(csp, "unsafe-hashes") {
		t.Errorf("CSP %q should have a plain style-src 'self'", csp)
	}
}

func TestServe_OnlyAllowsGetAndHead(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := do(t, newHandler(t), method, "/")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("%s / = %d (Allow %q), want 405 with Allow: GET, HEAD", method, rec.Code, rec.Header().Get("Allow"))
		}
	}
}
