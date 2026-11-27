//go:build integration

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base32"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/pgtest"
	"github.com/pmoscode/SerMon/server/internal/app/auth/passkey/passkeytest"
	"github.com/pmoscode/SerMon/server/internal/app/auth/totp"
	"github.com/pmoscode/SerMon/server/internal/config"
	"github.com/pmoscode/SerMon/server/internal/domain/module"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1/agentv1connect"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/platform/backupfile"
	"github.com/pmoscode/SerMon/server/internal/secrets"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

// Fixtures of the backup test, not secrets.
const (
	backupOrigin     = "https://sermon.example.com" // passkeys need a domain name
	backupOrgName    = "Backup Example Org"
	backupAdmin      = "ann@example.org"
	backupPassword   = "backup admin passphrase 2026"                    // betterleaks:allow
	backupModulePass = "module-credential-of-the-backup-test"            // betterleaks:allow
	backupKioskToken = "smk_backupKioskFixtureForTheRestoreTest00000000" // betterleaks:allow
	backupEnrollment = "sme_BackupRestoreIntegrationTestToken0123456789" // betterleaks:allow
)

// instanceDB is a database of its own, owned by a non-superuser owner as in production.
type instanceDB struct {
	name                 string
	super, owner, backup string
	app                  string
}

func newInstanceDB(t *testing.T, name, appDSN string) instanceDB {
	t.Helper()
	shared := pgtest.Shared(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, shared.OwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	// The roles of deploy/compose/initdb/010-roles.sh; sermon_definer and sermon_app exist after
	// the first migration of the shared database.
	for _, sql := range []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_owner_v105') THEN
			CREATE ROLE sermon_owner_v105 LOGIN PASSWORD 'owner-test' NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS; END IF; END $$`,
		`GRANT sermon_definer TO sermon_owner_v105`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sermon_backup') THEN
			CREATE ROLE sermon_backup LOGIN PASSWORD 'backup-test' NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS IN ROLE pg_read_all_data; END IF; END $$`,
		"CREATE DATABASE " + name + " OWNER sermon_owner_v105",
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	at := func(dsn, user string) string {
		dsn = strings.Replace(dsn, "/sermon?", "/"+name+"?", 1)
		if user != "" {
			dsn = regexp.MustCompile(`//[^@]+@`).ReplaceAllString(dsn, "//"+user+"@")
		}
		return dsn
	}
	return instanceDB{
		name: name, super: at(shared.OwnerDSN, ""), owner: at(shared.OwnerDSN, "sermon_owner_v105:owner-test"),
		backup: at(shared.OwnerDSN, "sermon_backup:backup-test"), app: at(appDSN, ""),
	}
}

func (d instanceDB) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, d.super)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func (d instanceDB) value(t *testing.T, sql string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, d.super)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var v string
	if err := conn.QueryRow(ctx, sql).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func envOf(env map[string]string) config.Source {
	return config.Source{LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }, ReadFile: os.ReadFile}
}

func (d instanceDB) serverEnv(keysFile string) config.Source {
	return envOf(map[string]string{
		"SERMON_MODE": "development", "SERMON_LOG_FORMAT": "text",
		"SERMON_DATABASE_URL": d.app, "SERMON_DATABASE_MIGRATE_URL": d.owner, "SERMON_MASTER_KEYS_FILE": keysFile,
		"SERMON_HTTP_ADDR": "127.0.0.1:0", "SERMON_AGENT_ADDR": "127.0.0.1:0", "SERMON_AGENT_HOSTNAMES": "localhost",
		"SERMON_ALLOWED_ORIGINS": backupOrigin,
	})
}

// browserOf is a browser of the backup test: Origin and session cookie by hand.
type browserOf struct {
	http *http.Client
	auth webv1connect.AuthServiceClient
}

func newBrowserOf(srv runningServer) browserOf {
	hc := &http.Client{Transport: &cookieJar{cookies: map[string]string{}}, Timeout: 30 * time.Second}
	return browserOf{http: hc, auth: webv1connect.NewAuthServiceClient(hc, "http://"+srv.web)}
}

// cookieJar keeps every cookie by hand (they are Secure, the test talks plain HTTP): the session
// and the binding of a passkey ceremony.
type cookieJar struct {
	mu      sync.Mutex
	cookies map[string]string
}

func (j *cookieJar) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Origin", backupOrigin)
	j.mu.Lock()
	for name, value := range j.cookies {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	j.mu.Unlock()
	res, err := http.DefaultTransport.RoundTrip(r)
	if err == nil {
		j.mu.Lock()
		for _, c := range res.Cookies() {
			if c.MaxAge < 0 || c.Value == "" {
				delete(j.cookies, c.Name)
			} else {
				j.cookies[c.Name] = c.Value
			}
		}
		j.mu.Unlock()
	}
	return res, err
}

// moduleSession connects as the agent with the module simulate built in and returns the
// credentials the server sends it.
func (s runningServer) moduleSession(t *testing.T, agent testAgent) (map[string]string, error) {
	t.Helper()
	pool := x509.NewCertPool()
	for _, der := range agent.roots {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		pool.AddCert(c)
	}
	tr := &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{
		RootCAs: pool, Certificates: []tls.Certificate{agent.cert}, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	}}
	client := agentv1connect.NewAgentServiceClient(&http.Client{Transport: tr, Timeout: 30 * time.Second}, "https://"+s.agent, connect.WithGRPC())
	stream := client.Session(context.Background())
	defer func() { _ = stream.CloseResponse() }()
	hello := &agentv1.SessionRequest{Payload: &agentv1.SessionRequest_Hello{Hello: &agentv1.Hello{
		ProtocolVersion: 1, AgentVersion: "0.0.0", HostFacts: hostFacts(), Modules: []string{"simulate"},
	}}}
	if err := stream.Send(hello); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	for range 10 {
		res, err := stream.Receive()
		if err != nil {
			return nil, err
		}
		if m := res.GetModuleSecrets(); m != nil {
			got := map[string]string{}
			for _, s := range m.GetSecrets() {
				got[s.GetModuleId()+"."+s.GetField()] = s.GetValue()
			}
			_ = stream.CloseRequest()
			return got, nil
		}
	}
	return nil, errors.New("no ModuleSecrets in the first messages")
}

// seedInstance writes what the UI would: a group with the module simulate and its credential,
// a dashboard with a kiosk token, an enrollment token, a security event and an alert rule.
func seedInstance(t *testing.T, d instanceDB, keysFile string) {
	t.Helper()
	org := d.value(t, "SELECT id::text FROM organizations")
	admin := d.value(t, "SELECT id::text FROM users")
	group := uuid.NewString()
	keyText, err := os.ReadFile(keysFile)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.ParseKeys(string(keyText))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte(backupModulePass), module.SecretContext(tenant.OrgID(uuid.MustParse(org)), tenant.GroupID(uuid.MustParse(group)), "simulate", "token"))
	if err != nil {
		t.Fatal(err)
	}
	dashboard := uuid.NewString()
	spec := `{"version":1,"title":"hall","timeRange":0,"widgets":[]}`
	d.exec(t, "INSERT INTO node_groups (id, org_id, name) VALUES ($1, $2, 'backup-servers')", group, org)
	d.exec(t, "INSERT INTO module_settings (org_id, group_id, module_id, enabled, config) VALUES ($1, $2, 'simulate', true, '{}')", org, group)
	d.exec(t, "INSERT INTO module_secrets (org_id, group_id, module_id, field, sealed) VALUES ($1, $2, 'simulate', 'token', $3)", org, group, sealed)
	d.exec(t, `INSERT INTO enrollment_tokens (id, org_id, group_id, token_hash, expires_at, max_uses)
		VALUES (gen_random_uuid(), $1, $2, sha256(convert_to($3, 'UTF8')), now() + interval '1 hour', 1)`, org, group, backupEnrollment)
	d.exec(t, `INSERT INTO dashboards (org_id, id, owner_id, title, shared, spec, created_at, updated_at)
		VALUES ($1, $2, $3, 'hall', true, $4, now(), now())`, org, dashboard, admin, spec)
	d.exec(t, `INSERT INTO kiosk_tokens (org_id, id, dashboard_id, name, token_hash, created_by, created_at, expires_at)
		VALUES ($1, gen_random_uuid(), $2, 'hall 2', sha256(convert_to($3, 'UTF8')), $4, now(), now() + interval '1 day')`, org, dashboard, backupKioskToken, admin)
	d.exec(t, `INSERT INTO security_events (org_id, node_id, occurred_at, kind, user_name, remote_ip)
		VALUES ($1, gen_random_uuid(), now() - interval '1 hour', 'ssh.login.failed', 'bob', '192.0.2.10')`, org)
	d.exec(t, `INSERT INTO alert_rule_overrides (org_id, rule_id, disabled) VALUES ($1, 'system.swap.usage_high', true)`, org)
}

// TestBackupRestore_EndToEnd (V1-05): an instance with a superadmin (password, TOTP and a
// passkey), an agent, a module credential, a kiosk token, a dashboard, events and a rule is
// backed up as sermon_backup and restored into a new database as the owner: the admin signs in
// with password and TOTP and with the passkey, the agent connects with its certificate and gets
// its credential, the kiosk token works. The file holds nothing in plain text; without the
// right backup key or master keys, changed, or into a database with data, the restore is refused
// and changes nothing.
func TestBackupRestore_EndToEnd(t *testing.T) {
	ctx := context.Background()
	shared := pgtest.Shared(t)
	if err := postgres.Migrate(ctx, shared.OwnerDSN); err != nil {
		t.Fatal(err)
	}
	appDSN := pgtest.EnableAppRole(t, shared)
	src := newInstanceDB(t, "sermon_v105_src", appDSN)
	dir := t.TempDir()
	keysFile := filepath.Join(dir, "master_keys")

	// 1. A new instance: init, setup, TOTP and a passkey through the web API.
	var out, errOut bytes.Buffer
	if code := run(ctx, []string{"init", "--master-keys-file", keysFile}, envOf(map[string]string{
		"SERMON_MODE": "development", "SERMON_DATABASE_URL": src.app, "SERMON_DATABASE_MIGRATE_URL": src.owner,
	}), &out, &errOut); code != exitOK {
		t.Fatalf("init = %d: %s", code, errOut.String())
	}
	srv := startServerWith(t, src.serverEnv(keysFile))
	// The server replaces the token of init at its start.
	setupToken := waitFor(t, srv.logs, regexp.MustCompile(`setup_token=([A-Za-z0-9_-]{43})`))
	b := newBrowserOf(srv)
	if _, err := webv1connect.NewSetupServiceClient(b.http, "http://"+srv.web).CompleteSetup(ctx, connect.NewRequest(&webv1.CompleteSetupRequest{
		Token: setupToken, Email: backupAdmin, DisplayName: "Ann", Password: backupPassword, OrganizationName: backupOrgName,
	})); err != nil {
		t.Fatalf("CompleteSetup() = %v", err)
	}
	if _, err := b.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: backupAdmin, Password: backupPassword})); err != nil {
		t.Fatal(err)
	}
	twoFactor := webv1connect.NewTwoFactorServiceClient(b.http, "http://"+srv.web)
	begun, err := twoFactor.BeginTotpSetup(ctx, connect.NewRequest(&webv1.BeginTotpSetupRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	totpSecret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(begun.Msg.GetSecret())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := twoFactor.ConfirmTotpSetup(ctx, connect.NewRequest(&webv1.ConfirmTotpSetupRequest{Code: totp.Code(totpSecret, totp.Step(time.Now()))})); err != nil {
		t.Fatal(err)
	}
	passkeys := webv1connect.NewPasskeyServiceClient(b.http, "http://"+srv.web)
	key := passkeytest.New(backupOrigin)
	registration, err := passkeys.BeginPasskeyRegistration(ctx, connect.NewRequest(&webv1.BeginPasskeyRegistrationRequest{Name: "Laptop"}))
	if err != nil {
		t.Fatal(err)
	}
	answer, err := key.Register([]byte(registration.Msg.GetOptionsJson()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := passkeys.FinishPasskeyRegistration(ctx, connect.NewRequest(&webv1.FinishPasskeyRegistrationRequest{
		CeremonyId: registration.Msg.GetCeremonyId(), CredentialJson: string(answer),
	})); err != nil {
		t.Fatal(err)
	}
	seedInstance(t, src, keysFile)
	agent := srv.enroll(t, backupEnrollment)
	if got, err := srv.moduleSession(t, agent); err != nil || got["simulate.token"] != backupModulePass {
		t.Fatalf("before the backup: credentials %v, %v", got, err)
	}
	srv.stop()

	// 2. The backup as sermon_backup, to a new backup key.
	identityFile, recipient, err := backupfile.GenerateIdentity(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "backup_identity"), identityFile)
	writeFile(t, filepath.Join(dir, "backup_recipients"), recipient+"\n")
	backupPath := filepath.Join(dir, "sermon.backup")
	out.Reset()
	errOut.Reset()
	if code := run(ctx, []string{"backup", "--out", backupPath}, envOf(map[string]string{
		"SERMON_DATABASE_BACKUP_URL": src.backup, "SERMON_MASTER_KEYS_FILE": keysFile,
		"SERMON_BACKUP_RECIPIENTS_FILE": filepath.Join(dir, "backup_recipients"), "SERMON_LOG_FORMAT": "text",
	}), &out, &errOut); code != exitOK {
		t.Fatalf("backup = %d: %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "backup complete") || out.Len() != 0 {
		t.Errorf("backup log %q, stdout %d bytes", errOut.String(), out.Len())
	}
	file, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(backupPath); info.Mode().Perm() != 0o600 {
		t.Errorf("backup file mode %v, want 0600", info.Mode().Perm())
	}
	masterKeys, _ := os.ReadFile(keysFile)
	assertNoPlaintext(t, file, identityFile, string(masterKeys), totpSecret, begun.Msg.GetSecret())

	// 3. Refused restores change nothing.
	dst := newInstanceDB(t, "sermon_v105_dst", appDSN)
	otherIdentity, _, _ := backupfile.GenerateIdentity(time.Now())
	otherKeys, _ := secrets.GenerateKeyLine(1)
	writeFile(t, filepath.Join(dir, "other_identity"), otherIdentity)
	writeFile(t, filepath.Join(dir, "other_keys"), otherKeys+"\n")
	changed := bytes.Clone(file)
	changed[len(changed)-30] ^= 0x01
	writeFile(t, filepath.Join(dir, "changed.backup"), string(changed))
	restoreEnv := func(identity, keys string) config.Source {
		return envOf(map[string]string{
			"SERMON_DATABASE_MIGRATE_URL": dst.owner, "SERMON_MASTER_KEYS_FILE": keys,
			"SERMON_BACKUP_IDENTITY_FILE": identity, "SERMON_LOG_FORMAT": "text",
		})
	}
	for name, attempt := range map[string]struct {
		file, identity, keys, want string
	}{
		"another backup key":  {backupPath, "other_identity", keysFile, "no backup key opens"},
		"other master keys":   {backupPath, "backup_identity", filepath.Join(dir, "other_keys"), "master keys do not belong"},
		"a changed file":      {filepath.Join(dir, "changed.backup"), "backup_identity", keysFile, "damaged or was changed"},
		"another file format": {keysFile, "backup_identity", keysFile, "damaged or was changed"},
	} {
		errOut.Reset()
		code := run(ctx, []string{"restore", "--in", attempt.file}, restoreEnv(filepath.Join(dir, attempt.identity), attempt.keys), &out, &errOut)
		if code != exitRefused || !strings.Contains(errOut.String(), attempt.want) {
			t.Errorf("%s: restore = %d, %q", name, code, errOut.String())
		}
		if n := dst.value(t, "SELECT count(*)::text FROM pg_class WHERE relname IN ('users', 'organizations') AND relnamespace = 'public'::regnamespace"); n != "0" {
			if users := dst.value(t, "SELECT count(*)::text FROM users"); users != "0" {
				t.Errorf("%s: the refused restore left %s users", name, users)
			}
		}
	}

	// 4. The restore into the new database, the master keys installed where the server reads them.
	stateDir := t.TempDir()
	installed := filepath.Join(stateDir, "master_keys")
	errOut.Reset()
	if code := run(ctx, []string{"restore", "--in", backupPath, "--install-master-keys", installed},
		restoreEnv(filepath.Join(dir, "backup_identity"), keysFile), &out, &errOut); code != exitOK {
		t.Fatalf("restore = %d: %s", code, errOut.String())
	}
	if got, _ := os.ReadFile(installed); strings.TrimSpace(string(got)) != strings.TrimSpace(string(masterKeys)) {
		t.Error("the master keys were not installed")
	}
	errOut.Reset()
	if code := run(ctx, []string{"restore", "--in", backupPath}, restoreEnv(filepath.Join(dir, "backup_identity"), keysFile), &out, &errOut); code != exitRefused || !strings.Contains(errOut.String(), "not empty") {
		t.Errorf("second restore = %d, %q", code, errOut.String())
	}
	for _, table := range []string{
		"users", "organizations", "memberships", "nodes", "agent_certificates", "pki_authorities", "user_totp",
		"webauthn_credentials", "module_settings", "module_secrets", "kiosk_tokens", "dashboards", "security_events", "alert_rule_overrides",
		"audit_log", "enrollment_tokens", "superadmins", "instance_setup",
	} {
		if s, d := src.value(t, "SELECT count(*)::text FROM "+table), dst.value(t, "SELECT count(*)::text FROM "+table); s != d || s == "0" {
			t.Errorf("%s: %s rows in the source, %s after the restore", table, s, d)
		}
	}
	// The instance's audit log has the restore in addition, at the end of its chain.
	if got := dst.value(t, "SELECT action || ' ' || target FROM instance_audit_log ORDER BY seq DESC LIMIT 1"); !strings.HasPrefix(got, "instance.restored backup:") {
		t.Errorf("last instance audit entry %q, want the restore", got)
	}
	if s, d := src.value(t, "SELECT count(*)::text FROM instance_audit_log"), dst.value(t, "SELECT (count(*) - 1)::text FROM instance_audit_log"); s != d {
		t.Errorf("instance_audit_log: %s rows in the source, %s+1 after the restore", s, d)
	}
	if n := dst.value(t, "SELECT count(*)::text FROM sessions"); n != "0" {
		t.Errorf("%s sessions restored", n)
	}

	// 5. The restored instance works.
	srv = startServerWith(t, dst.serverEnv(installed))
	defer srv.stop()
	b = newBrowserOf(srv)
	login, err := b.auth.Login(ctx, connect.NewRequest(&webv1.LoginRequest{Email: backupAdmin, Password: backupPassword}))
	if err != nil || !login.Msg.GetSecondFactorRequired() {
		t.Fatalf("Login() = %v, %v; want the second factor asked", login, err)
	}
	verified, err := b.auth.VerifySecondFactor(ctx, connect.NewRequest(&webv1.VerifySecondFactorRequest{Code: totp.Code(totpSecret, totp.Step(time.Now())+1)}))
	if err != nil || verified.Msg.GetSession().GetOrganizations()[0].GetName() != backupOrgName {
		t.Fatalf("VerifySecondFactor() = %v, %v", verified, err)
	}
	fresh := newBrowserOf(srv)
	signIn, err := fresh.auth.BeginPasskey(ctx, connect.NewRequest(&webv1.BeginPasskeyRequest{Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_LOGIN}))
	if err != nil {
		t.Fatal(err)
	}
	assertion, err := key.Assert([]byte(signIn.Msg.GetOptionsJson()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.auth.FinishPasskey(ctx, connect.NewRequest(&webv1.FinishPasskeyRequest{
		Purpose: webv1.PasskeyPurpose_PASSKEY_PURPOSE_LOGIN, CeremonyId: signIn.Msg.GetCeremonyId(), CredentialJson: string(assertion),
	})); err != nil {
		t.Errorf("passkey sign-in after the restore: %v", err)
	}
	if got, err := srv.moduleSession(t, agent); err != nil || got["simulate.token"] != backupModulePass {
		t.Errorf("after the restore: credentials %v, %v (the agent's certificate and the module's credential)\n%s", got, err, srv.logs.String())
	}
	kioskReq := connect.NewRequest(&webv1.GetKioskDashboardRequest{})
	kioskReq.Header().Set("Authorization", webapi.KioskAuthorization+backupKioskToken)
	kioskClient := webv1connect.NewKioskServiceClient(&http.Client{Transport: originRoundTripper{next: http.DefaultTransport, origin: backupOrigin}}, "http://"+srv.web)
	if board, err := kioskClient.GetKioskDashboard(ctx, kioskReq); err != nil || board.Msg.GetSpec().GetTitle() != "hall" {
		t.Errorf("kiosk after the restore: %v, %v", board, err)
	}
	dashboards, err := webv1connect.NewDashboardServiceClient(b.http, "http://"+srv.web).ListDashboards(ctx, connect.NewRequest(&webv1.ListDashboardsRequest{}))
	if err != nil || len(dashboards.Msg.GetDashboards()) != 1 {
		t.Errorf("dashboards after the restore: %v, %v", dashboards, err)
	}
	unforced := dst.value(t, `SELECT coalesce(string_agg(relname, ','), '') FROM pg_class
		WHERE relnamespace = 'public'::regnamespace AND relrowsecurity AND NOT relforcerowsecurity`)
	if unforced != "" {
		t.Errorf("without FORCE ROW LEVEL SECURITY after the restore: %s", unforced)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// assertNoPlaintext checks the file and its decrypted content: no credential, no token, no key
// in plain text – the master keys aren't in the backup at all.
func assertNoPlaintext(t *testing.T, file []byte, identityFile, masterKeys string, totpSecret []byte, totpText string) {
	t.Helper()
	plain := []string{backupAdmin, backupOrgName, backupModulePass, backupKioskToken, backupEnrollment, backupPassword, totpText}
	for _, line := range strings.Split(strings.TrimSpace(masterKeys), "\n") {
		_, key, _ := strings.Cut(line, ":")
		plain = append(plain, key)
	}
	for _, p := range plain {
		if bytes.Contains(file, []byte(p)) {
			t.Errorf("%q is readable in the backup file", p)
		}
	}
	ids, err := backupfile.ParseIdentities(identityFile)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := age.Decrypt(bytes.NewReader(file), ids...)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(decrypted)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	// The probe: what is no secret is there.
	if !bytes.Contains(content, []byte(backupOrgName)) || !bytes.Contains(content, []byte(backupAdmin)) {
		t.Fatal("the decrypted backup lacks the organization – the probe is broken")
	}
	for _, p := range plain[2:] {
		if bytes.Contains(content, []byte(p)) {
			t.Errorf("%q is in plain text inside the backup", p)
		}
	}
	if bytes.Contains(content, totpSecret) {
		t.Error("the TOTP seed is in plain text inside the backup")
	}
}
