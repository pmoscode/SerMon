//go:build integration

package webapi_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/adapters/oidc/oidctest"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

const erinID = "0193f5a0-0000-7000-8000-0000000000e1"

// browser is a cookie jar with an auth client that doesn't follow redirects.
type browser struct {
	jar  http.CookieJar
	http *http.Client
	auth webv1connect.AuthServiceClient
}

func newBrowser(t *testing.T, s stack) browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := s.httpClient(jar, s.origin)
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return browser{jar: jar, http: hc, auth: webv1connect.NewAuthServiceClient(hc, s.server.URL)}
}

// signIn runs a flow: BeginOidc, the provider's login, the return to the callback; it returns
// where the browser goes next.
func (b browser) signIn(t *testing.T, s stack, intent webv1.OidcIntent, next string, who oidctest.Login) string {
	t.Helper()
	ctx := context.Background()
	res, err := b.auth.BeginOidc(ctx, connect.NewRequest(&webv1.BeginOidcRequest{Intent: intent, Next: next}))
	if err != nil {
		t.Fatalf("BeginOidc(%v) = %v", intent, err)
	}
	back, err := s.idp.Authorize(res.Msg.GetAuthorizationUrl(), who)
	if err != nil {
		t.Fatal(err)
	}
	return b.follow(t, back)
}

func (b browser) follow(t *testing.T, url string) string {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("callback: %d, %v", resp.StatusCode, resp.Header)
	}
	return resp.Header.Get("Location")
}

// M3-20 over the real stack: Erin links an identity (signed in, with a step-up), signs in with it,
// renews the step-up at the provider; strangers and other browsers are turned away alike.
func TestOIDC_LinkSignInAndStepUp(t *testing.T) {
	s := newStack(t, 1000)
	ctx := context.Background()
	ownerSQL(t, "DELETE FROM user_identities WHERE user_id = '"+erinID+"'")

	anon := newBrowser(t, s)
	opts, err := anon.auth.GetLoginOptions(ctx, connect.NewRequest(&webv1.GetLoginOptionsRequest{}))
	if err != nil || !opts.Msg.GetOidc() || opts.Msg.GetOidcName() != "Test IdP" {
		t.Fatalf("GetLoginOptions() = %v, %v", opts, err)
	}
	// Nobody has the identity yet: the sign-in fails like every failure.
	if got := anon.signIn(t, s, webv1.OidcIntent_OIDC_INTENT_LOGIN, "/alerts", oidctest.Login{Subject: "erin-sub"}); got != "/login?sso=failed" {
		t.Errorf("unknown identity: %s", got)
	}
	// Linking needs a session.
	if _, err := anon.auth.BeginOidc(ctx, connect.NewRequest(&webv1.BeginOidcRequest{Intent: webv1.OidcIntent_OIDC_INTENT_LINK})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("link without a session: %v", err)
	}

	erin := newBrowser(t, s)
	if _, err := erin.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "erin@example.org", Password: userPassword})); err != nil {
		t.Fatal(err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+erinID+"'")
	_, err = erin.auth.BeginOidc(ctx, connect.NewRequest(&webv1.BeginOidcRequest{Intent: webv1.OidcIntent_OIDC_INTENT_LINK}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "confirm") {
		t.Errorf("link with an old login: %v", err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() WHERE user_id = '"+erinID+"'")
	if got := erin.signIn(t, s, webv1.OidcIntent_OIDC_INTENT_LINK, "/settings/security", oidctest.Login{Subject: "erin-sub", Email: "erin@idp.example"}); got != "/settings/security" {
		t.Errorf("link: back to %s", got)
	}
	var linked int
	ownerQuery(t, "SELECT count(*) FROM user_identities WHERE user_id = '"+erinID+"' AND subject = 'erin-sub'", &linked)
	if linked != 1 {
		t.Fatalf("%d identities linked", linked)
	}

	// A fresh browser signs in with it and lands where it wanted.
	fresh := newBrowser(t, s)
	if got := fresh.signIn(t, s, webv1.OidcIntent_OIDC_INTENT_LOGIN, "/alerts?tab=rules", oidctest.Login{Subject: "erin-sub"}); got != "/alerts?tab=rules" {
		t.Errorf("sign-in: back to %s", got)
	}
	who, err := fresh.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if err != nil || who.Msg.GetSession().GetEmail() != "erin@example.org" {
		t.Fatalf("GetSession() after the sign-in = %v, %v", who, err)
	}
	// An open redirect is not one.
	if got := fresh.signIn(t, s, webv1.OidcIntent_OIDC_INTENT_LOGIN, "//evil.example/x", oidctest.Login{Subject: "erin-sub"}); got != "/" {
		t.Errorf("next //evil.example: %s", got)
	}

	// The code and state of one browser don't work in another (login CSRF).
	res, err := fresh.auth.BeginOidc(ctx, connect.NewRequest(&webv1.BeginOidcRequest{Intent: webv1.OidcIntent_OIDC_INTENT_LOGIN}))
	if err != nil {
		t.Fatal(err)
	}
	back, err := s.idp.Authorize(res.Msg.GetAuthorizationUrl(), oidctest.Login{Subject: "erin-sub"})
	if err != nil {
		t.Fatal(err)
	}
	victim := newBrowser(t, s)
	if got := victim.follow(t, back); got != "/login?sso=failed" {
		t.Errorf("another browser: %s", got)
	}
	if _, err := victim.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("the other browser got a session: %v", err)
	}
	// The provider's refusal.
	res, _ = fresh.auth.BeginOidc(ctx, connect.NewRequest(&webv1.BeginOidcRequest{Intent: webv1.OidcIntent_OIDC_INTENT_LOGIN}))
	back, _ = s.idp.Authorize(res.Msg.GetAuthorizationUrl(), oidctest.Login{Subject: "erin-sub"})
	if got := fresh.follow(t, back+"&error=access_denied"); got != "/login?sso=failed" {
		t.Errorf("refused at the provider: %s", got)
	}

	// The step-up asks the provider for a fresh login and renews the session's step-up.
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+erinID+"'")
	if got := fresh.signIn(t, s, webv1.OidcIntent_OIDC_INTENT_STEP_UP, "/admin", oidctest.Login{Subject: "erin-sub", AuthTime: time.Now()}); got != "/admin" {
		t.Errorf("step-up: back to %s", got)
	}
	if s.idp.LastAuth.Get("max_age") != "0" || s.idp.LastAuth.Get("prompt") != "login" {
		t.Errorf("step-up request %v", s.idp.LastAuth)
	}
	var fresher int
	ownerQuery(t, "SELECT count(*) FROM sessions WHERE user_id = '"+erinID+"' AND step_up_at > now() - interval '1 minute'", &fresher)
	if fresher < 1 {
		t.Error("no session with a fresh step-up")
	}
	// A login the provider remembered from before doesn't count.
	if got := fresh.signIn(t, s, webv1.OidcIntent_OIDC_INTENT_STEP_UP, "/admin", oidctest.Login{Subject: "erin-sub", AuthTime: time.Now().Add(-time.Hour)}); got != "/login?sso=failed" {
		t.Errorf("an old login at the provider: %s", got)
	}
	// Someone else's identity is no step-up for Erin.
	if got := fresh.signIn(t, s, webv1.OidcIntent_OIDC_INTENT_STEP_UP, "/admin", oidctest.Login{Subject: "mallory", AuthTime: time.Now()}); got != "/login?sso=failed" {
		t.Errorf("another identity: %s", got)
	}
}
