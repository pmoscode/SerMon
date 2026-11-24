// Package oidctest is a fake OpenID Connect provider for tests (M3-20): discovery, JWKS with
// its own RSA key, the authorization step as a function call and a token endpoint that checks
// the client secret and the PKCE verifier (S256). Tests change the tokens it issues.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// Login is who logs in at the provider.
type Login struct {
	Subject  string
	Email    string
	Name     string
	Groups   []string
	AMR      []string
	AuthTime time.Time
}

type grant struct {
	login     Login
	nonce     string
	challenge string
	client    string
	redirect  string
}

// Provider is the fake provider; its URL is the issuer.
type Provider struct {
	*httptest.Server
	ClientID, ClientSecret string

	mu     sync.Mutex
	key    *rsa.PrivateKey
	other  *rsa.PrivateKey
	grants map[string]grant
	// Tamper changes the claims of the next ID tokens (expiry, audience, issuer, nonce, ...).
	Tamper func(claims map[string]any)
	// Unsigned issues tokens with "alg": "none"; ForeignKey signs with a key not in the JWKS.
	Unsigned, ForeignKey bool
	// Requests counts the token requests.
	Requests int
	// LastAuth is the query of the last authorization request.
	LastAuth url.Values
}

// New starts a provider for one client.
func New(clientID, clientSecret string) *Provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	p := &Provider{ClientID: clientID, ClientSecret: clientSecret, key: key, other: other, grants: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /keys", p.keys)
	mux.HandleFunc("POST /token", p.token)
	p.Server = httptest.NewServer(mux)
	return p
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer": p.URL, "authorization_endpoint": p.URL + "/auth", "token_endpoint": p.URL + "/token",
		"jwks_uri": p.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"code_challenge_methods_supported": []string{"S256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &p.key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
}

// Authorize plays the browser at the provider: login logs in for the authorization URL; it
// returns the URL the provider redirects back to (with code and state).
func (p *Provider) Authorize(authURL string, login Login) (string, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", fmt.Errorf("authorization URL: %w", err)
	}
	q := u.Query()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.LastAuth = q
	if q.Get("response_type") != "code" || q.Get("client_id") != p.ClientID {
		return "", errors.New("oidctest: not a code request of the client")
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		return "", errors.New("oidctest: no PKCE (S256)")
	}
	if login.AuthTime.IsZero() {
		login.AuthTime = time.Now()
	}
	code := randomCode()
	p.grants[code] = grant{login: login, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), client: q.Get("client_id"), redirect: q.Get("redirect_uri")}
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		return "", fmt.Errorf("redirect URI: %w", err)
	}
	bq := back.Query()
	bq.Set("code", code)
	bq.Set("state", q.Get("state"))
	back.RawQuery = bq.Encode()
	return back.String(), nil
}

func randomCode() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Requests++
	g, found := p.grants[r.PostForm.Get("code")]
	delete(p.grants, r.PostForm.Get("code"))
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case r.PostForm.Get("grant_type") != "authorization_code" || !found:
		tokenError(w, "invalid_grant")
		return
	case id != p.ClientID || secret != p.ClientSecret || g.client != id:
		tokenError(w, "invalid_client")
		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
		tokenError(w, "invalid_grant") // PKCE
		return
	case r.PostForm.Get("redirect_uri") != g.redirect:
		tokenError(w, "invalid_grant")
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.URL, "sub": g.login.Subject, "aud": p.ClientID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"nonce": g.nonce, "auth_time": g.login.AuthTime.Unix(), "email": g.login.Email, "email_verified": true,
	}
	if g.login.Name != "" {
		claims["name"] = g.login.Name
	}
	if g.login.Groups != nil {
		claims["groups"] = g.login.Groups
	}
	if g.login.AMR != nil {
		claims["amr"] = g.login.AMR
	}
	if p.Tamper != nil {
		p.Tamper(claims)
	}
	idToken, err := p.sign(claims)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"access_token": randomCode(), "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
}

func (p *Provider) sign(claims map[string]any) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("claims: %w", err)
	}
	if p.Unsigned {
		head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		return head + "." + base64.RawURLEncoding.EncodeToString(payload) + ".", nil
	}
	key := p.key
	if p.ForeignKey {
		key = p.other
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		return "", fmt.Errorf("signer: %w", err)
	}
	obj, err := signer.Sign(payload)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return obj.CompactSerialize() //nolint:wrapcheck // test helper
}

func tokenError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
