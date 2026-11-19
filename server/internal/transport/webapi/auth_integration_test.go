//go:build integration

package webapi_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/auth"
	"github.com/pmoscode/SerMon/server/internal/app/auth/password"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/enrollment"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/app/rotation"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/platform/ratelimit"
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
	server *httptest.Server
	origin string
	ca     *agentca.Service
	db     *postgres.DB
}

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
	login := auth.NewService(auth.Deps{
		Credentials: creds, Sessions: sessions, Hasher: hasher,
		IPLimiter: ratelimit.New(loginLimit, time.Minute, time.Now), Now: time.Now,
	})

	invites := invitation.NewService(postgres.NewInvitationStore(pg), creds, authz.NewPolicy(), hasher, time.Now, access.NewID)
	ca, err := agentca.Bootstrap(ctx, postgres.NewAgentCAStore(pg), masterKeys(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewUnstartedServer(mux)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	webapi.Mount(mux, webapi.Config{AllowedOrigins: []string{srv.URL}}, webapi.Handlers{
		Auth:   webapi.NewAuthHandler(login, sessions, creds, slog.New(slog.DiscardHandler)),
		System: webapi.NewSystemHandler("test"),
		Access: webapi.NewAccessHandler(access.NewService(postgres.NewAccessStore(pg), authz.NewPolicy(), time.Now, access.NewID),
			invites, slog.New(slog.DiscardHandler)),
		Invitation: webapi.NewInvitationHandler(invites, sessions, ratelimit.New(1000, time.Minute, time.Now), slog.New(slog.DiscardHandler)),
		Organization: webapi.NewOrganizationHandler(
			organization.NewService(postgres.NewOrganizationStore(pg), authz.NewPolicy(), time.Now, access.NewID), slog.New(slog.DiscardHandler)),
		Enrollment: webapi.NewEnrollmentTokenHandler(
			enrollment.NewService(postgres.NewEnrollmentTokenStore(pg), authz.NewPolicy(), time.Now, access.NewID), slog.New(slog.DiscardHandler)),
		Node: webapi.NewNodeHandler(rotation.NewService(postgres.NewRotationStore(pg), ca, time.Now,
			rotation.WithOnDemand(postgres.NewRotationStore(pg), agentapi.NewSessions(0), authz.NewPolicy())), slog.New(slog.DiscardHandler)),
		AgentCA: webapi.NewAgentCAHandler(ca, slog.New(slog.DiscardHandler)),
		AgentEnrollment: webapi.NewAgentEnrollmentHandler(
			enrollment.NewEnroller(postgres.NewEnrollmentTokenStore(pg), postgres.NewNodeRegistry(pg), ca, time.Now, access.NewID),
			ratelimit.New(1000, time.Minute, time.Now), slog.New(slog.DiscardHandler)),
		Authenticator: webapi.NewAuthenticator(sessions, access.NewLoader(postgres.NewAccessStore(pg)), slog.New(slog.DiscardHandler)),
	})
	return stack{server: srv, origin: srv.URL, ca: ca, db: pg}
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
