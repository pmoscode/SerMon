// Package httppost sends a notification request over safehttp and classifies the answer for the
// retry rules of notify (shared by the HTTP channels).
package httppost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

// maxReason bounds what an error quotes from the answer of a service.
const maxReason = 200

// Post sends body to url. It returns nil for 2xx and otherwise the error of notify.HTTPStatus with
// the start of the answer (services explain rejections there; notify removes secrets).
func Post(ctx context.Context, c *safehttp.Client, url string, header http.Header, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return notify.Permanent(fmt.Errorf("%w: %w", safehttp.ErrInvalidURL, err))
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.Do(req)
	if err != nil {
		return err //nolint:wrapcheck // safehttp names method and host
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err := notify.HTTPStatus(resp.StatusCode, resp.Header.Get("Retry-After")); err != nil {
		if reason := reasonOf(answer); reason != "" {
			return fmt.Errorf("%w: %s", err, reason)
		}
		return err
	}
	return nil
}

// reasonOf is the start of an answer as one line of valid text.
func reasonOf(answer []byte) string {
	s := strings.ToValidUTF8(string(answer), "")
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	if len(s) <= maxReason {
		return s
	}
	cut := maxReason
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
