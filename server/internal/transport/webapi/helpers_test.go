package webapi_test

import "net/http"

// originTransport adds the Origin header like a browser does.
type originTransport struct {
	next   http.RoundTripper
	origin string
}

func (o originTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Origin", o.origin)
	return o.next.RoundTrip(r)
}
