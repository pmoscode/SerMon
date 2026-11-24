package oidc_test

import (
	"context"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/oidc"
	"github.com/pmoscode/SerMon/server/internal/adapters/oidc/oidctest"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

const (
	clientID = "sermon"
	secret   = "client-fixture-secret" // betterleaks:allow
	redirect = "https://sermon.example.com/auth/oidc/callback"
	verifier = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
	// challenge is the S256 of verifier.
	challenge = "EPLRB-mJyFPMDGrrGA6Ecy8kCSaKez3dx7RPEqzr0OQ"
)

func newProvider(t *testing.T, idp *oidctest.Provider, allow bool) *oidc.Provider {
	t.Helper()
	var list []netip.Prefix
	if allow {
		list = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	}
	policy, err := safehttp.NewPolicy(list)
	if err != nil {
		t.Fatal(err)
	}
	client := safehttp.New(safehttp.Options{Policy: policy}).HTTPClient()
	return oidc.New(oidc.Config{Issuer: idp.URL, ClientID: clientID, ClientSecret: secret, RedirectURL: redirect, Scopes: []string{"email", "groups"}}, client)
}

// login runs the authorization step and returns the code of the redirect back.
func login(t *testing.T, p *oidc.Provider, idp *oidctest.Provider, who oidctest.Login, force bool) string {
	t.Helper()
	authURL, err := p.AuthCodeURL(context.Background(), "state-1", "nonce-1", challenge, force)
	if err != nil {
		t.Fatal(err)
	}
	back, err := idp.Authorize(authURL, who)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(back)
	if u.Query().Get("state") != "state-1" || !strings.HasPrefix(back, redirect) {
		t.Fatalf("back to %s", back)
	}
	return u.Query().Get("code")
}

func TestProvider_LogsInWithPKCE(t *testing.T) {
	idp := oidctest.New(clientID, secret)
	defer idp.Close()
	p := newProvider(t, idp, true)
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	code := login(t, p, idp, oidctest.Login{Subject: "alice-sub", Email: "alice@example.org", Name: "Alice", Groups: []string{"ops", "dev"}, AMR: []string{"pwd", "otp"}, AuthTime: at}, false)
	q := idp.LastAuth
	if q.Get("scope") != "openid email groups" || q.Get("nonce") != "nonce-1" || q.Get("code_challenge") != challenge ||
		q.Get("prompt") != "" || q.Get("max_age") != "" || q.Get("redirect_uri") != redirect {
		t.Errorf("authorization request %v", q)
	}
	c, err := p.Exchange(context.Background(), code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if c.Issuer != idp.URL || c.Subject != "alice-sub" || c.Email != "alice@example.org" || !c.EmailVerified || c.Name != "Alice" ||
		strings.Join(c.Groups, ",") != "ops,dev" || strings.Join(c.AMR, ",") != "pwd,otp" || !c.AuthTime.Equal(at) || c.Nonce != "nonce-1" {
		t.Errorf("claims %+v", c)
	}
	// A step-up asks for a fresh login.
	login(t, p, idp, oidctest.Login{Subject: "alice-sub"}, true)
	if idp.LastAuth.Get("prompt") != "login" || idp.LastAuth.Get("max_age") != "0" {
		t.Errorf("step-up request %v", idp.LastAuth)
	}
}

func TestProvider_RefusesBadTokens(t *testing.T) {
	for name, tc := range map[string]struct {
		setup    func(idp *oidctest.Provider)
		verifier string
	}{
		"expired": {func(idp *oidctest.Provider) {
			idp.Tamper = func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }
		}, verifier},
		"other audience": {func(idp *oidctest.Provider) { idp.Tamper = func(c map[string]any) { c["aud"] = "someone-else" } }, verifier},
		"other issuer": {func(idp *oidctest.Provider) {
			idp.Tamper = func(c map[string]any) { c["iss"] = "https://evil.example" }
		}, verifier},
		"alg none":         {func(idp *oidctest.Provider) { idp.Unsigned = true }, verifier},
		"foreign key":      {func(idp *oidctest.Provider) { idp.ForeignKey = true }, verifier},
		"wrong verifier":   {func(*oidctest.Provider) {}, strings.Repeat("x", 43)},
		"missing verifier": {func(*oidctest.Provider) {}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			idp := oidctest.New(clientID, secret)
			defer idp.Close()
			p := newProvider(t, idp, true)
			code := login(t, p, idp, oidctest.Login{Subject: "alice-sub"}, false)
			tc.setup(idp)
			if c, err := p.Exchange(context.Background(), code, tc.verifier); err == nil {
				t.Errorf("Exchange() = %+v, want an error", c)
			}
		})
	}
}

func TestProvider_WrongClientSecretAndReusedCode(t *testing.T) {
	idp := oidctest.New(clientID, "another-secret")
	defer idp.Close()
	p := newProvider(t, idp, true)
	code := login(t, p, idp, oidctest.Login{Subject: "alice-sub"}, false)
	if _, err := p.Exchange(context.Background(), code, verifier); err == nil {
		t.Error("a wrong client secret was accepted")
	}
	idp.ClientSecret = secret
	if _, err := p.Exchange(context.Background(), code, verifier); err == nil {
		t.Error("a code was redeemed twice")
	}
}

// Discovery and keys go through the safe client: a provider on an internal address needs the
// operator's allowlist. A failed discovery is tried again.
func TestProvider_InternalProvidersOnlyOnTheAllowlist(t *testing.T) {
	idp := oidctest.New(clientID, secret)
	defer idp.Close()
	blocked := newProvider(t, idp, false)
	if _, err := blocked.AuthCodeURL(context.Background(), "s", "n", challenge, false); err == nil {
		t.Error("discovery on 127.0.0.1 without the allowlist")
	}
	if err := blocked.Ready(context.Background()); err == nil {
		t.Error("Ready() without the allowlist")
	}
	ok := newProvider(t, idp, true)
	if err := ok.Ready(context.Background()); err != nil {
		t.Errorf("Ready() = %v", err)
	}
}
