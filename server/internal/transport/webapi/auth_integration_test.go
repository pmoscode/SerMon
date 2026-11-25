//go:build integration

package webapi_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/adapters/loki/lokitest"
	oidcadapter "github.com/pmoscode/SerMon/server/internal/adapters/oidc"
	"github.com/pmoscode/SerMon/server/internal/adapters/oidc/oidctest"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/adapters/timescale"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/alerting/ruleset"
	"github.com/pmoscode/SerMon/server/internal/app/alerts"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/oidc"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/auth/twofactor"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/app/fleet"
	"github.com/pmoscode/SerMon/server/internal/app/instanceaudit"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/app/logs"
	"github.com/pmoscode/SerMon/server/internal/app/logsettings"
	"github.com/pmoscode/SerMon/server/internal/app/metrics"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/oidcmapping"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/app/rules"
	"github.com/pmoscode/SerMon/server/internal/app/secevents"
	"github.com/pmoscode/SerMon/server/internal/app/selfmonitor"
	"github.com/pmoscode/SerMon/server/internal/app/updates"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
	"github.com/pmoscode/SerMon/server/internal/secrets"
	"github.com/pmoscode/SerMon/server/internal/transport/agentapi"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

// Test fixtures, not secrets.
const (
	userPassword  = "correct horse battery staple" // betterleaks:allow
	wrongPassword = "wrong password here"          // betterleaks:allow
	guessPassword = "guess guess guess"            // betterleaks:allow
)

type stack struct {
	server  *httptest.Server
	origin  string
	ca      *agentca.Service
	db      *postgres.DB
	ring    *metrics.Ring
	metrics *timescale.Store
	// hub is the live view of the processes; no agent is connected, snapshots come from tests.
	hub *liveview.Hub
	// idp is the fake OIDC provider of the stack (M3-20).
	idp *oidctest.Provider
}

// passkeyOrigin is where the software authenticator of the tests says it runs (M3-23).
const passkeyOrigin = "https://sermon.example.com"

// The fake provider's client (M3-20).
const (
	oidcClientID = "sermon"
	oidcSecret   = "oidc-fixture-secret" // betterleaks:allow
)

var (
	testBoxOnce sync.Once
	testBox     *secrets.Box
)

// masterKeys returns one master key per test process: the agent CA stored by the first stack
// must be readable by every later one.
func masterKeys(t *testing.T) *secrets.Box {
	t.Helper()
	testBoxOnce.Do(func() {
		line, err := secrets.GenerateKeyLine(1)
		if err == nil {
			testBox, err = secrets.ParseKeys(line)
		}
		if err != nil {
			t.Fatal(err)
		}
	})
	return testBox
}

// httpClient returns a new client with its own cookie jar. httptest.Server.Client returns the
// same *http.Client every time, so it must not be modified (clients would share cookies).
func (s stack) httpClient(jar http.CookieJar, origin string) *http.Client {
	return &http.Client{Jar: jar, Transport: originTransport{next: s.server.Client().Transport, origin: origin}}
}

// newStack starts the web API over TLS with a real database (Testcontainers).
func newStack(t *testing.T, loginLimit int) stack {
	t.Helper()
	return newStackWithMailer(t, loginLimit, nil)
}

// newStackWithMailer is newStack with invitation links sent by mailer (production).
func newStackWithMailer(t *testing.T, loginLimit int, mailer invitation.Mailer) stack {
	t.Helper()
	ctx := context.Background()
	db := pgtest.Shared(t)
	if err := postgres.Migrate(ctx, db.OwnerDSN); err != nil {
		t.Fatal(err)
	}
	appDSN := pgtest.EnableAppRole(t, db)
	seedUser(t, db.OwnerDSN)

	pg, err := postgres.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	hasher, _ := password.NewHasher(password.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32})
	creds := postgres.NewCredentialStore(pg)
	sessions := session.NewManager(postgres.NewSessionStore(pg), time.Now)
	secondFactor := twofactor.NewService(postgres.NewTwoFactorStore(pg), masterKeys(t), authz.NewPolicy(), time.Now)
	login := auth.NewService(auth.Deps{
		Credentials: creds, Sessions: sessions, Hasher: hasher,
		IPLimiter: ratelimit.New(loginLimit, time.Minute, time.Now), Now: time.Now, SecondFactor: secondFactor,
	})
	// Passkeys (M3-23) for the origin of the software authenticator of the tests.
	rp, err := passkey.Config([]string{passkeyOrigin})
	if err != nil {
		t.Fatal(err)
	}
	relyingParty, err := webauthn.New(rp)
	if err != nil {
		t.Fatal(err)
	}
	passkeys := passkey.NewService(passkey.Deps{
		WebAuthn: relyingParty, Store: postgres.NewPasskeyStore(pg), Users: creds, Lockout: login, Sessions: sessions,
		Limiter: ratelimit.New(1000, time.Minute, time.Now), Now: time.Now,
	})
	secondFactor.WithPasskeys(passkeys)

	mux := http.NewServeMux()
	srv := httptest.NewUnstartedServer(mux)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	idp := oidctest.New(oidcClientID, oidcSecret)
	t.Cleanup(idp.Close)
	loopback, err := safehttp.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")})
	if err != nil {
		t.Fatal(err)
	}
	oidcStore := postgres.NewOIDCStore(pg)
	mappings := oidcmapping.NewService(postgres.NewOIDCMappingStore(pg), creds, authz.NewPolicy(), time.Now, access.NewID, nil)
	sso := oidc.NewService(oidc.Deps{
		Provider: oidcadapter.New(oidcadapter.Config{
			Issuer: idp.URL, ClientID: oidcClientID, ClientSecret: oidcSecret, RedirectURL: srv.URL + webapi.OIDCCallbackPath,
			Scopes: []string{"email", "profile"},
		}, safehttp.New(safehttp.Options{Policy: loopback}).HTTPClient()),
		Flows: oidcStore, Identities: oidcStore, Users: creds, Sessions: sessions, SecondFactor: secondFactor,
		Limiter: ratelimit.New(1000, time.Minute, time.Now), MFA: oidc.MFAByClaim, Now: time.Now,
		Provisioner: mappings, JIT: true,
	})
	var inviteOptions []invitation.Option
	var orgOptions []organization.Option
	if mailer != nil {
		inviteOptions = append(inviteOptions, invitation.WithMailer(mailer, srv.URL))
		orgOptions = append(orgOptions, organization.WithMailer(mailer, srv.URL))
	}
	lifecycleOptions := []organization.LifecycleOption{
		organization.WithAgentSessions(agentapi.NewSessions(0)),
		organization.WithLogs(loki.NewEraser(loki.New(lokitest.Shared(t)))),
	}
	if mailer != nil {
		lifecycleOptions = append(lifecycleOptions, organization.WithLifecycleMailer(mailer))
	}
	lifecycle, err := organization.NewLifecycle(postgres.NewOrganizationLifecycleStore(pg), authz.NewPolicy(), time.Now,
		organization.DefaultDeletionGrace, lifecycleOptions...)
	if err != nil {
		t.Fatal(err)
	}
	invites := invitation.NewService(postgres.NewInvitationStore(pg), creds, authz.NewPolicy(), hasher, time.Now, access.NewID, inviteOptions...)
	ca, err := agentca.Bootstrap(ctx, postgres.NewAgentCAStore(pg), masterKeys(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ring := metrics.NewRing()
	metricStore := timescale.New(pg)
	nodes := fleet.NewService(postgres.NewFleetStore(pg), metrics.NewReader(metricStore, ring, time.Now), time.Now)
	policies := routing.NewService(routing.ServiceDeps{
		Store: postgres.NewRoutingStore(pg), Windows: postgres.NewRoutingStore(pg), Channels: postgres.NewNotificationStore(pg),
		Groups: postgres.NewFleetStore(pg), Policy: authz.NewPolicy(), Now: time.Now, NewID: access.NewID,
	})
	hub := liveview.NewHub(agentapi.NewSessions(0))
	liveViews := liveview.NewService(hub, postgres.NewFleetStore(pg), postgres.NewLogSettingsStore(pg), authz.NewPolicy(),
		liveview.WithRecheck(200*time.Millisecond))
	webapi.Mount(mux, webapi.Config{AllowedOrigins: []string{srv.URL}}, webapi.Handlers{
		LiveView: webapi.NewLiveViewHandler(liveViews, nil, slog.New(slog.DiscardHandler)),
		Auth: webapi.NewAuthHandler(login, sessions, creds, secondFactor, slog.New(slog.DiscardHandler)).
			WithPrincipals(access.NewLoader(postgres.NewAccessStore(pg), secondFactor)).
			WithInstanceHealth(postgres.NewAccessStore(pg), fakeProblems{}).WithOIDC(sso, "Test IdP").WithPasskeys(passkeys),
		Passkeys: webapi.NewPasskeyHandler(passkeys, slog.New(slog.DiscardHandler)),
		System:   webapi.NewSystemHandler("test"),
		Access: webapi.NewAccessHandler(access.NewService(postgres.NewAccessStore(pg), authz.NewPolicy(), time.Now, access.NewID),
			invites, slog.New(slog.DiscardHandler)),
		Invitation: webapi.NewInvitationHandler(invites, sessions, ratelimit.New(1000, time.Minute, time.Now), slog.New(slog.DiscardHandler)),
		Organization: webapi.NewOrganizationHandler(
			organization.NewService(postgres.NewOrganizationStore(pg), authz.NewPolicy(), time.Now, access.NewID, orgOptions...), slog.New(slog.DiscardHandler)).
			WithLifecycle(lifecycle),
		Enrollment: webapi.NewEnrollmentTokenHandler(
			enrollment.NewService(postgres.NewEnrollmentTokenStore(pg), authz.NewPolicy(), time.Now, access.NewID), slog.New(slog.DiscardHandler)),
		Node: webapi.NewNodeHandler(rotation.NewService(postgres.NewRotationStore(pg), ca, time.Now,
			rotation.WithOnDemand(postgres.NewRotationStore(pg), agentapi.NewSessions(0), authz.NewPolicy())), nodes, slog.New(slog.DiscardHandler)).
			WithTagger(fleet.NewTagger(postgres.NewFleetStore(pg), authz.NewPolicy(), time.Now)),
		AgentCA: webapi.NewAgentCAHandler(ca, slog.New(slog.DiscardHandler)),
		InstanceAudit: webapi.NewInstanceAuditHandler(
			instanceaudit.NewService(postgres.NewInstanceAuditStore(pg), authz.NewPolicy()), slog.New(slog.DiscardHandler)),
		Metrics: webapi.NewMetricsHandler(nodes, nil, slog.New(slog.DiscardHandler)),
		Fleet:   webapi.NewFleetHandler(nodes, slog.New(slog.DiscardHandler)),
		SecurityEvents: webapi.NewSecurityEventHandler(withGeo(
			secevents.NewService(postgres.NewSecurityEventStore(pg), authz.NewPolicy())), slog.New(slog.DiscardHandler)),
		Updates: webapi.NewUpdateHandler(updates.NewService(postgres.NewUpdateStore(pg, 0), authz.NewPolicy()).
			WithRefresher(postgres.NewCommandStore(pg), time.Now).
			WithManual(postgres.NewRolloutStore(pg), postgres.NewUpdatePolicyStore(pg), uuid.New), slog.New(slog.DiscardHandler)),
		UpdatePolicies: webapi.NewUpdatePolicyHandler(
			updates.NewPolicyService(postgres.NewUpdatePolicyStore(pg), authz.NewPolicy(), time.Now), slog.New(slog.DiscardHandler)),
		AgentEnrollment: webapi.NewAgentEnrollmentHandler(
			enrollment.NewEnroller(postgres.NewEnrollmentTokenStore(pg), postgres.NewNodeRegistry(pg), ca, time.Now, access.NewID),
			ratelimit.New(1000, time.Minute, time.Now), slog.New(slog.DiscardHandler)),
		TwoFactor: webapi.NewTwoFactorHandler(secondFactor, creds, ratelimit.New(1000, time.Minute, time.Now), slog.New(slog.DiscardHandler)),
		Notifications: webapi.NewNotificationChannelHandler(notify.NewService(notify.Deps{
			Store: postgres.NewNotificationStore(pg), Registry: hookRegistry(t), Box: masterKeys(t), Policy: authz.NewPolicy(),
			BaseURL: srv.URL, Now: time.Now, NewID: access.NewID, Logger: slog.New(slog.DiscardHandler),
		}), slog.New(slog.DiscardHandler)),
		Policies: webapi.NewNotificationPolicyHandler(policies, slog.New(slog.DiscardHandler)),
		Windows:  webapi.NewMaintenanceWindowHandler(policies, slog.New(slog.DiscardHandler)),
		Logs: webapi.NewLogHandler(
			logs.NewService(loki.NewReader(loki.New(lokitest.Shared(t))), postgres.NewFleetStore(pg), authz.NewPolicy()), nil, slog.New(slog.DiscardHandler)),
		OIDCMappings: webapi.NewOIDCMappingHandler(mappings, true, true, slog.New(slog.DiscardHandler)),
		LogSettings: webapi.NewLogSettingsHandler(
			logsettings.NewService(postgres.NewLogSettingsStore(pg), authz.NewPolicy(), agentapi.NewSessions(0), time.Now), slog.New(slog.DiscardHandler)),
		Rules: webapi.NewAlertRuleHandler(rules.NewService(rules.Deps{
			Defaults: mustDefaultRules(t), Store: postgres.NewAlertRuleStore(pg), Policy: authz.NewPolicy(), Now: time.Now,
		}), slog.New(slog.DiscardHandler)),
		Alerts: webapi.NewAlertHandler(alerts.NewService(alerts.Deps{
			Store: postgres.NewAlertStore(pg), Nodes: postgres.NewFleetStore(pg), Rules: noRules{},
			Users: routing.UsersOf(postgres.NewAccessStore(pg)), Mutes: postgres.NewRoutingStore(pg),
			Sink: routing.NewRouter(routing.RouterDeps{
				Store: postgres.NewRoutingStore(pg), Rules: noRules{}, Users: routing.UsersOf(postgres.NewAccessStore(pg)), Logger: slog.New(slog.DiscardHandler),
			}),
			Policy: authz.NewPolicy(), Now: time.Now, Box: masterKeys(t),
		}), slog.New(slog.DiscardHandler)),
		Silences: webapi.NewAlertSilenceHandler(routing.NewSilenceService(routing.SilenceDeps{
			Store: postgres.NewRoutingStore(pg), Nodes: postgres.NewFleetStore(pg), Policy: authz.NewPolicy(), Now: time.Now, NewID: access.NewID,
		}), slog.New(slog.DiscardHandler)),
		Authenticator: webapi.NewAuthenticator(sessions, access.NewLoader(postgres.NewAccessStore(pg), secondFactor), slog.New(slog.DiscardHandler)),
	})
	return stack{server: srv, origin: srv.URL, ca: ca, db: pg, ring: ring, metrics: metricStore, hub: hub, idp: idp}
}

// ownerSQL runs statements on one owner connection (set_config persists between them).
func ownerSQL(t *testing.T, statements ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, sql := range statements {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
}

// seedLogin creates (or resets) a user with userPassword who is a member of the organizations.
func seedLogin(t *testing.T, id, email string, orgs ...string) {
	t.Helper()
	h, _ := password.NewHasher(password.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32})
	hash, _ := h.Hash(userPassword)
	stmts := []string{
		"INSERT INTO users (id, email, display_name) VALUES ('" + id + "', '" + email + "', 'Test') ON CONFLICT DO NOTHING",
		"INSERT INTO user_credentials (user_id, password_hash) VALUES ('" + id + "', '" + hash + "') ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash, failed_attempts = 0, locked_until = NULL",
	}
	for _, org := range orgs {
		stmts = append(stmts, "INSERT INTO memberships (org_id, user_id) VALUES ('"+org+"', '"+id+"') ON CONFLICT DO NOTHING")
	}
	ownerSQL(t, stmts...)
}

func seedUser(t *testing.T, ownerDSN string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	h, _ := password.NewHasher(password.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLength: 16, KeyLength: 32})
	hash, _ := h.Hash(userPassword)
	for _, sql := range []string{
		`INSERT INTO organizations (id, name) VALUES ('0193f5a0-0000-7000-8000-00000000000a', 'Org A') ON CONFLICT DO NOTHING`,
		`INSERT INTO users (id, email, display_name) VALUES ('0193f5a0-0000-7000-8000-0000000000e1', 'erin@example.org', 'Erin') ON CONFLICT DO NOTHING`,
		`INSERT INTO memberships (org_id, user_id) VALUES ('0193f5a0-0000-7000-8000-00000000000a', '0193f5a0-0000-7000-8000-0000000000e1') ON CONFLICT DO NOTHING`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO user_credentials (user_id, password_hash) VALUES ('0193f5a0-0000-7000-8000-0000000000e1', $1)
		ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash, failed_attempts = 0, locked_until = NULL`, hash); err != nil {
		t.Fatal(err)
	}
}

func client(t *testing.T, s stack, origin string) webv1connect.AuthServiceClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := s.httpClient(jar, origin)
	return webv1connect.NewAuthServiceClient(hc, s.server.URL)
}

func TestAuthAPI_LoginSessionLogout(t *testing.T) {
	s := newStack(t, 20)
	c := client(t, s, s.origin)
	ctx := context.Background()

	res, err := c.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "Erin@example.org", Password: userPassword}))
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if got := res.Msg.GetSession(); got.GetEmail() != "erin@example.org" || got.GetDisplayName() != "Erin" ||
		got.GetActiveOrganizationId() != "0193f5a0-0000-7000-8000-00000000000a" || len(got.GetOrganizations()) != 1 {
		t.Errorf("session = %+v", got)
	}
	cookie := res.Header().Get("Set-Cookie")
	for _, attr := range []string{webapi.SessionCookie + "=", "Path=/", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(cookie, attr) {
			t.Errorf("Set-Cookie %q lacks %q", cookie, attr)
		}
	}
	if strings.Contains(strings.ToLower(cookie), "domain=") {
		t.Errorf("__Host- cookie must not have a Domain: %q", cookie)
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Error("API responses must not be cached")
	}

	if _, err := c.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); err != nil {
		t.Fatalf("GetSession() with cookie error = %v", err)
	}
	if _, err := c.Logout(ctx, connect.NewRequest(&webv1.LogoutRequest{})); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := c.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("GetSession() after logout error = %v, want unauthenticated", err)
	}
}

func TestAuthAPI_WrongPasswordAndUnknownUserAreIndistinguishable(t *testing.T) {
	s := newStack(t, 20)
	c := client(t, s, s.origin)
	ctx := context.Background()

	_, errWrong := c.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "erin@example.org", Password: wrongPassword}))
	_, errUnknown := c.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "nobody@example.org", Password: wrongPassword}))

	if connect.CodeOf(errWrong) != connect.CodeUnauthenticated || connect.CodeOf(errUnknown) != connect.CodeUnauthenticated {
		t.Fatalf("codes = %v / %v, want unauthenticated", connect.CodeOf(errWrong), connect.CodeOf(errUnknown))
	}
	var a, b *connect.Error
	if errors.As(errWrong, &a) && errors.As(errUnknown, &b) && a.Message() != b.Message() {
		t.Errorf("messages differ: %q vs %q", a.Message(), b.Message())
	}
	seedUser(t, pgtest.Shared(t).OwnerDSN) // reset the failure counter for other tests
}

func TestAuthAPI_RejectsForeignOrigins(t *testing.T) {
	s := newStack(t, 20)
	c := client(t, s, "https://evil.example")

	_, err := c.Login(context.Background(), connect.NewRequest(&webv1.LoginRequest{Email: "erin@example.org", Password: userPassword}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("Login() from a foreign origin: error = %v, want permission denied", err)
	}
}

func TestAuthAPI_ValidatesInput(t *testing.T) {
	s := newStack(t, 20)
	c := client(t, s, s.origin)

	_, err := c.Login(context.Background(), connect.NewRequest(&webv1.LoginRequest{Email: strings.Repeat("a", 330), Password: "x"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("oversized e-mail: error = %v, want invalid argument", err)
	}
}

func TestAuthAPI_RateLimitsLoginAttempts(t *testing.T) {
	s := newStack(t, 3)
	c := client(t, s, s.origin)
	ctx := context.Background()
	for range 3 {
		_, _ = c.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "nobody@example.org", Password: guessPassword}))
	}

	_, err := c.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "erin@example.org", Password: userPassword}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("error = %v, want resource exhausted", err)
	}
}

func TestAuthAPI_GetSessionWithoutCookieIsUnauthenticated(t *testing.T) {
	s := newStack(t, 20)
	c := client(t, s, s.origin)

	if _, err := c.GetSession(context.Background(), connect.NewRequest(&webv1.GetSessionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("error = %v, want unauthenticated", err)
	}
}

func TestAuthAPI_RemovingTheMembershipEndsAccessImmediately(t *testing.T) {
	s := newStack(t, 20)
	t.Cleanup(func() { seedUser(t, pgtest.Shared(t).OwnerDSN) })
	jar, _ := cookiejar.New(nil)
	hc := s.httpClient(jar, s.origin)
	authc := webv1connect.NewAuthServiceClient(hc, s.server.URL)
	system := webv1connect.NewSystemServiceClient(hc, s.server.URL)
	ctx := context.Background()

	if _, err := system.GetVersion(ctx, connect.NewRequest(&webv1.GetVersionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("GetVersion() before login: %v, want unauthenticated", err)
	}
	if _, err := authc.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "erin@example.org", Password: userPassword})); err != nil {
		t.Fatal(err)
	}
	if _, err := system.GetVersion(ctx, connect.NewRequest(&webv1.GetVersionRequest{})); err != nil {
		t.Fatalf("GetVersion() after login: %v", err)
	}

	conn, err := pgx.Connect(ctx, pgtest.Shared(t).OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `DELETE FROM memberships WHERE user_id = '0193f5a0-0000-7000-8000-0000000000e1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := system.GetVersion(ctx, connect.NewRequest(&webv1.GetVersionRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("GetVersion() after removal: %v, want unauthenticated", err)
	}
}

func TestAuthAPI_ConfirmPasswordRenewsTheStepUp(t *testing.T) {
	s := newStack(t, 20)
	c := client(t, s, s.origin)
	ctx := context.Background()

	if _, err := c.ConfirmPassword(ctx, connect.NewRequest(&webv1.ConfirmPasswordRequest{Password: userPassword})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("without session: %v, want Unauthenticated", err)
	}
	login, err := c.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: "erin@example.org", Password: userPassword}))
	if err != nil {
		t.Fatal(err)
	}
	until := login.Msg.GetSession().GetStepUpUntil().AsTime()
	if d := time.Until(until); d < 14*time.Minute || d > 15*time.Minute {
		t.Errorf("step_up_until after login = %v, want now + 15 min", until)
	}

	_, err = c.ConfirmPassword(ctx, connect.NewRequest(&webv1.ConfirmPasswordRequest{Password: wrongPassword}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("wrong password: %v, want PermissionDenied", err)
	}
	time.Sleep(10 * time.Millisecond)
	res, err := c.ConfirmPassword(ctx, connect.NewRequest(&webv1.ConfirmPasswordRequest{Password: userPassword}))
	if err != nil {
		t.Fatalf("ConfirmPassword() error = %v", err)
	}
	if renewed := res.Msg.GetSession().GetStepUpUntil().AsTime(); !renewed.After(until) {
		t.Errorf("step_up_until = %v, want later than %v", renewed, until)
	}
	session, err := c.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if err != nil || !session.Msg.GetSession().GetStepUpUntil().AsTime().Equal(res.Msg.GetSession().GetStepUpUntil().AsTime()) {
		t.Errorf("GetSession() after the step-up = %v, %v", session, err)
	}
}

func TestAuthAPI_SessionDescribesTheRights(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()

	ann := login(t, s, "ann@example.org") // admin of all groups
	if me, err := ann.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); err != nil ||
		!me.Msg.GetSession().GetOrgAdmin() || strings.Join(me.Msg.GetSession().GetEnrollGroupIds(), ",") != "*" ||
		strings.Join(me.Msg.GetSession().GetSilenceGroupIds(), ",") != "*" || strings.Join(me.Msg.GetSession().GetNodeIdentityGroupIds(), ",") != "*" ||
		strings.Join(me.Msg.GetSession().GetTagGroupIds(), ",") != "*" {
		t.Errorf("admin: %v, %v", me, err)
	}
	mia := login(t, s, "mia@example.org") // viewer of one group
	if me, err := mia.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); err != nil ||
		me.Msg.GetSession().GetOrgAdmin() || len(me.Msg.GetSession().GetEnrollGroupIds()) != 0 || len(me.Msg.GetSession().GetSilenceGroupIds()) != 0 ||
		len(me.Msg.GetSession().GetNodeIdentityGroupIds()) != 0 || len(me.Msg.GetSession().GetTagGroupIds()) != 0 {
		t.Errorf("viewer: %v, %v", me, err)
	}
	ownerSQL(t, "SELECT set_config('sermon.org_id', '"+orgAlpha+"', false)",
		"UPDATE role_bindings SET role = 'operator' WHERE id = '"+bindingAl+"'")
	t.Cleanup(func() { seedAccessFixtures(t) })
	if me, err := mia.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{})); err != nil ||
		strings.Join(me.Msg.GetSession().GetEnrollGroupIds(), ",") != groupAl || strings.Join(me.Msg.GetSession().GetSilenceGroupIds(), ",") != groupAl ||
		strings.Join(me.Msg.GetSession().GetNodeIdentityGroupIds(), ",") != groupAl ||
		len(me.Msg.GetSession().GetTagGroupIds()) != 0 { // tags need admin (M2-29)
		t.Errorf("operator of a group: %v, %v", me, err)
	}
}

// noRules knows no rule names (the API tests show the rule IDs).
type noRules struct{}

func (noRules) RuleInfos(context.Context, tenant.OrgID) (map[alert.RuleID]routing.RuleInfo, error) {
	return nil, nil
}

func mustDefaultRules(t *testing.T) []ruleset.Definition {
	t.Helper()
	defs, err := ruleset.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

// fakeProblems is the self-monitoring of the test stack: one problem, always.
type fakeProblems struct{}

func (fakeProblems) Problems() []selfmonitor.Problem {
	return []selfmonitor.Problem{{Check: selfmonitor.IngestLag, Level: selfmonitor.Warning, Since: time.Unix(1_795_000_000, 0), Value: 75}}
}

// Self-monitoring (M2-24): only superadmins see the server's problems in their session.
func TestAuthAPI_OnlySuperadminsSeeInstanceProblems(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ctx := context.Background()
	seedLogin(t, samID, "sam@example.org")
	ownerSQL(t, "INSERT INTO superadmins (user_id) VALUES ('"+samID+"') ON CONFLICT DO NOTHING")
	sam := login(t, s, "sam@example.org")
	me, err := sam.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	p := me.Msg.GetSession().GetInstanceProblems()
	if !me.Msg.GetSession().GetSuperadmin() || len(p) != 1 || p[0].GetCheck() != "ingest_lag" || p[0].GetLevel() != "warning" || p[0].GetValue() != 75 {
		t.Errorf("superadmin session = %v", me.Msg.GetSession())
	}
	ann := login(t, s, "ann@example.org") // org admin, no superadmin
	me, err = ann.auth.GetSession(ctx, connect.NewRequest(&webv1.GetSessionRequest{}))
	if err != nil || me.Msg.GetSession().GetSuperadmin() || len(me.Msg.GetSession().GetInstanceProblems()) != 0 {
		t.Errorf("member session = %v, %v", me, err)
	}
}
