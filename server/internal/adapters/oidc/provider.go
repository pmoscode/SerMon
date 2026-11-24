// Package oidc is the adapter to an OpenID Connect provider (M3-20, ADR 0033): discovery, JWKS
// and the token request go through the safe HTTP client (safehttp, internal providers only on
// the operator's allowlist); go-oidc verifies the ID token (signature, issuer, audience, expiry).
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	app "github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
)

// Config names the provider and the client.
type Config struct {
	Issuer, ClientID, ClientSecret, RedirectURL string
	// Scopes besides openid.
	Scopes []string
}

// Provider talks to the identity provider. It discovers its endpoints on first use (and again
// after a failure), so the server starts while the provider is down.
type Provider struct {
	cfg    Config
	client *http.Client //nolint:forbidigo // the client of safehttp.Client.HTTPClient, with its checks

	mu       sync.Mutex
	verifier *gooidc.IDTokenVerifier
	oauth    *oauth2.Config
}

var _ app.Provider = (*Provider)(nil)

// signingAlgs are the accepted signatures: asymmetric only, never "none" or a shared secret.
var signingAlgs = []string{
	gooidc.RS256, gooidc.RS384, gooidc.RS512, gooidc.ES256, gooidc.ES384, gooidc.ES512,
	gooidc.PS256, gooidc.PS384, gooidc.PS512, gooidc.EdDSA,
}

// ErrDiscovery means the provider's configuration could not be read.
var ErrDiscovery = errors.New("oidc: discovery of the provider failed")

// New returns the provider; client is safehttp.Client.HTTPClient.
func New(cfg Config, client *http.Client) *Provider { //nolint:forbidigo // see Provider.client
	return &Provider{cfg: cfg, client: client}
}

func (p *Provider) discovered(ctx context.Context) (*gooidc.IDTokenVerifier, *oauth2.Config, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.verifier != nil {
		return p.verifier, p.oauth, nil
	}
	// The key set fetches with the context it was created with: one that lives as long as the
	// server (not cancelled with this request), carrying the safe client.
	ctx = gooidc.ClientContext(context.WithoutCancel(ctx), p.client)
	provider, err := gooidc.NewProvider(ctx, p.cfg.Issuer)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	p.verifier = provider.VerifierContext(ctx, &gooidc.Config{ClientID: p.cfg.ClientID, SupportedSigningAlgs: signingAlgs})
	p.oauth = &oauth2.Config{
		ClientID: p.cfg.ClientID, ClientSecret: p.cfg.ClientSecret, Endpoint: provider.Endpoint(),
		RedirectURL: p.cfg.RedirectURL, Scopes: append([]string{gooidc.ScopeOpenID}, p.cfg.Scopes...),
	}
	return p.verifier, p.oauth, nil
}

// Ready discovers the provider (health and startup log).
func (p *Provider) Ready(ctx context.Context) error {
	_, _, err := p.discovered(ctx)
	return err
}

// AuthCodeURL implements app.Provider.
func (p *Provider) AuthCodeURL(ctx context.Context, state, nonce, challenge string, forceLogin bool) (string, error) {
	_, cfg, err := p.discovered(ctx)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}
	if forceLogin {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
	}
	return cfg.AuthCodeURL(state, opts...), nil
}

// claims are the fields of the ID token besides the standard ones go-oidc checks.
type claims struct {
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	Name          string          `json:"name"`
	Groups        json.RawMessage `json:"groups"`
	AMR           []string        `json:"amr"`
	AuthTime      json.Number     `json:"auth_time"`
	Nonce         string          `json:"nonce"`
}

// Exchange implements app.Provider: the code with the PKCE verifier, then the ID token.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (app.Claims, error) {
	v, cfg, err := p.discovered(ctx)
	if err != nil {
		return app.Claims{}, err
	}
	ctx = gooidc.ClientContext(ctx, p.client)
	token, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return app.Claims{}, fmt.Errorf("oidc: token request: %w", err)
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return app.Claims{}, errors.New("oidc: the token response has no ID token")
	}
	idt, err := v.Verify(ctx, raw)
	if err != nil {
		return app.Claims{}, fmt.Errorf("oidc: ID token: %w", err)
	}
	var c claims
	if err := idt.Claims(&c); err != nil {
		return app.Claims{}, fmt.Errorf("oidc: ID token claims: %w", err)
	}
	out := app.Claims{
		Issuer: idt.Issuer, Subject: idt.Subject, Email: c.Email, EmailVerified: truthy(c.EmailVerified), Name: c.Name,
		Groups: stringsOf(c.Groups), AMR: c.AMR, Nonce: c.Nonce,
	}
	if c.AuthTime != "" {
		if secs, err := strconv.ParseFloat(string(c.AuthTime), 64); err == nil && secs > 0 {
			out.AuthTime = time.Unix(int64(secs), 0).UTC()
		}
	}
	return out, nil
}

// truthy reads a boolean claim that some providers send as a string.
func truthy(raw json.RawMessage) bool {
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	return json.Unmarshal(raw, &s) == nil && s == "true"
}

// stringsOf reads a list of strings, or a single one.
func stringsOf(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	return nil
}
