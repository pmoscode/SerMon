package twofactor_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/auth/totp"
	"github.com/pmoscode/SerMon/server/internal/app/auth/twofactor"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

type memStore struct {
	required map[tenant.OrgID]bool
	audits   []access.AuditEntry
	mu       sync.Mutex
	secrets  map[identity.UserID]twofactor.Secret
	codes    map[identity.UserID]map[string]bool // hash → used
}

func newStore() *memStore {
	return &memStore{
		secrets: map[identity.UserID]twofactor.Secret{}, codes: map[identity.UserID]map[string]bool{},
		required: map[tenant.OrgID]bool{},
	}
}

func (m *memStore) Get(_ context.Context, user identity.UserID) (twofactor.Secret, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.secrets[user]
	return s, ok, nil
}

func (m *memStore) SavePending(_ context.Context, user identity.UserID, sealed []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.secrets[user].Confirmed {
		return twofactor.ErrAlreadyEnabled
	}
	m.secrets[user] = twofactor.Secret{Sealed: sealed}
	return nil
}

func (m *memStore) Confirm(_ context.Context, user identity.UserID, step int64, hashes [][]byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.secrets[user]
	if !ok || s.Confirmed {
		return false, nil
	}
	s.Confirmed, s.LastStep = true, step
	m.secrets[user] = s
	m.setCodes(user, hashes)
	return true, nil
}

func (m *memStore) setCodes(user identity.UserID, hashes [][]byte) {
	m.codes[user] = map[string]bool{}
	for _, h := range hashes {
		m.codes[user][string(h)] = false
	}
}

func (m *memStore) UseStep(_ context.Context, user identity.UserID, step int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.secrets[user]
	if step <= s.LastStep {
		return false, nil
	}
	s.LastStep = step
	m.secrets[user] = s
	return true, nil
}

func (m *memStore) UseRecoveryCode(_ context.Context, user identity.UserID, hash []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	used, ok := m.codes[user][string(hash)]
	if !ok || used {
		return false, nil
	}
	m.codes[user][string(hash)] = true
	return true, nil
}

func (m *memStore) ReplaceRecoveryCodes(_ context.Context, user identity.UserID, hashes [][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setCodes(user, hashes)
	return nil
}

func (m *memStore) RecoveryCodesLeft(_ context.Context, user identity.UserID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	left := 0
	for _, used := range m.codes[user] {
		if !used {
			left++
		}
	}
	return left, nil
}

func (m *memStore) Delete(_ context.Context, user identity.UserID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.secrets, user)
	delete(m.codes, user)
	return nil
}

func (m *memStore) SetRequiresSecondFactor(_ context.Context, scope tenant.Scope, required bool, audit access.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.required[scope.OrgID()] = required
	m.audits = append(m.audits, audit)
	return nil
}

func (m *memStore) RequiresSecondFactor(_ context.Context, scope tenant.Scope) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.required[scope.OrgID()], nil
}

type world struct {
	svc   *twofactor.Service
	store *memStore
	now   *time.Time
	user  identity.UserID
}

func newWorld(t *testing.T) world {
	t.Helper()
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.ParseKeys(line)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store := newStore()
	return world{
		svc:   twofactor.NewService(store, box, authz.NewPolicy(), func() time.Time { return now }),
		store: store, now: &now, user: identity.UserID(uuid.New()),
	}
}

func secretOf(t *testing.T, e twofactor.Enrollment) []byte {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(e.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// enable runs the setup and returns the secret and the recovery codes.
func (w world) enable(t *testing.T) ([]byte, []string) {
	t.Helper()
	e, err := w.svc.Begin(context.Background(), w.user, "mia@example.org", *w.now)
	if err != nil {
		t.Fatal(err)
	}
	secret := secretOf(t, e)
	codes, err := w.svc.Confirm(context.Background(), w.user, totp.Code(secret, totp.Step(*w.now)))
	if err != nil {
		t.Fatal(err)
	}
	*w.now = w.now.Add(totp.Period)
	return secret, codes
}

func TestSetup_NeedsAStepUpAndAWorkingCode(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	if _, err := w.svc.Begin(ctx, w.user, "mia@example.org", w.now.Add(-time.Hour)); !errors.Is(err, access.ErrStepUpRequired) {
		t.Fatalf("Begin() with an old step-up: %v", err)
	}
	e, err := w.svc.Begin(ctx, w.user, "mia@example.org", *w.now)
	if err != nil {
		t.Fatal(err)
	}
	uri, _ := url.Parse(e.URI)
	if uri.Scheme != "otpauth" || uri.Query().Get("secret") != e.Secret || !strings.Contains(e.URI, "mia@example.org") {
		t.Errorf("enrollment = %+v", e)
	}
	if stored := w.store.secrets[w.user]; bytes.Contains(stored.Sealed, secretOf(t, e)) {
		t.Error("the secret is stored in plain text")
	}
	if enabled, _ := w.svc.Enabled(ctx, w.user); enabled {
		t.Error("enabled before the confirmation")
	}
	if _, err := w.svc.Confirm(ctx, w.user, "000000"); !errors.Is(err, twofactor.ErrInvalidCode) {
		t.Errorf("wrong code: %v", err)
	}

	codes, err := w.svc.Confirm(ctx, w.user, totp.Code(secretOf(t, e), totp.Step(*w.now)))
	if err != nil || len(codes) != 10 {
		t.Fatalf("Confirm() = %v, %v", codes, err)
	}
	if status, _ := w.svc.Status(ctx, w.user); !status.Enabled || status.RecoveryCodesLeft != 10 {
		t.Errorf("status = %+v", status)
	}
	if _, err := w.svc.Begin(ctx, w.user, "mia@example.org", *w.now); !errors.Is(err, twofactor.ErrAlreadyEnabled) {
		t.Errorf("Begin() when enabled: %v", err)
	}
}

func TestVerify_EveryCodeWorksOnce(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	secret, codes := w.enable(t)

	code := totp.Code(secret, totp.Step(*w.now))
	if ok, err := w.svc.Verify(ctx, w.user, code); !ok || err != nil {
		t.Fatalf("Verify(totp) = %v, %v", ok, err)
	}
	if ok, _ := w.svc.Verify(ctx, w.user, code); ok {
		t.Error("the same TOTP code twice")
	}
	if ok, _ := w.svc.Verify(ctx, w.user, strings.ToUpper(codes[3])); !ok {
		t.Error("recovery code rejected")
	}
	if ok, _ := w.svc.Verify(ctx, w.user, codes[3]); ok {
		t.Error("the same recovery code twice")
	}
	if status, _ := w.svc.Status(ctx, w.user); status.RecoveryCodesLeft != 9 {
		t.Errorf("codes left = %d", status.RecoveryCodesLeft)
	}
	if ok, _ := w.svc.Verify(ctx, identity.UserID(uuid.New()), code); ok {
		t.Error("a user without 2FA verified")
	}
}

func TestVerify_ParallelUseOfOneCodeSucceedsOnce(t *testing.T) {
	w := newWorld(t)
	secret, _ := w.enable(t)
	code := totp.Code(secret, totp.Step(*w.now))
	results := make(chan bool, 8)
	for range 8 {
		go func() {
			ok, _ := w.svc.Verify(context.Background(), w.user, code)
			results <- ok
		}()
	}
	wins := 0
	for range 8 {
		if <-results {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("%d parallel successes, want 1", wins)
	}
}

func TestRegenerateAndDisable_NeedAStepUp(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	_, old := w.enable(t)
	stale := w.now.Add(-time.Hour)

	if _, err := w.svc.RegenerateRecoveryCodes(ctx, w.user, stale); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("regenerate with an old step-up: %v", err)
	}
	fresh, err := w.svc.RegenerateRecoveryCodes(ctx, w.user, *w.now)
	if err != nil || len(fresh) != 10 {
		t.Fatalf("RegenerateRecoveryCodes() = %v, %v", fresh, err)
	}
	if ok, _ := w.svc.Verify(ctx, w.user, old[0]); ok {
		t.Error("an old recovery code still works")
	}
	if err := w.svc.Disable(ctx, w.user, stale); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("disable with an old step-up: %v", err)
	}
	if err := w.svc.Disable(ctx, w.user, *w.now); err != nil {
		t.Fatal(err)
	}
	if enabled, _ := w.svc.Enabled(ctx, w.user); enabled {
		t.Error("still enabled")
	}
	if _, err := w.svc.RegenerateRecoveryCodes(ctx, w.user, *w.now); !errors.Is(err, twofactor.ErrNotEnabled) {
		t.Errorf("regenerate without 2FA: %v", err)
	}
}

func TestSecret_IsBoundToTheUser(t *testing.T) {
	w := newWorld(t)
	secret, _ := w.enable(t)
	other := identity.UserID(uuid.New())
	// Somebody copies the sealed secret to another user in the database.
	w.store.secrets[other] = w.store.secrets[w.user]

	if _, err := w.svc.Verify(context.Background(), other, totp.Code(secret, totp.Step(*w.now))); !errors.Is(err, secrets.ErrDecrypt) {
		t.Errorf("copied secret: %v, want ErrDecrypt", err)
	}
}

func TestSetupRequired_OnlyWhereTheOrganizationRequiresIt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	strict, relaxed := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	w.store.required[strict] = true

	if required, err := w.svc.SetupRequired(ctx, strict, w.user); err != nil || !required {
		t.Errorf("strict org without 2FA: %v, %v", required, err)
	}
	if required, _ := w.svc.SetupRequired(ctx, relaxed, w.user); required {
		t.Error("relaxed org requires a setup")
	}
	w.enable(t)
	if required, _ := w.svc.SetupRequired(ctx, strict, w.user); required {
		t.Error("strict org with 2FA still requires a setup")
	}
}

func TestSetOrganizationRequirement_AdminsWithOwnSecondFactorAndStepUp(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	org := tenant.OrgID(uuid.New())
	admin := access.Caller{Principal: authz.Principal{User: w.user, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}}}, StepUpAt: *w.now}
	operator := access.Caller{Principal: authz.Principal{User: w.user, Org: org, Bindings: []authz.Binding{{AllGroups: true, Role: role.Operator}}}, StepUpAt: *w.now}

	if err := w.svc.SetOrganizationRequirement(ctx, operator, true); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator: %v, want ErrForbidden", err)
	}
	if err := w.svc.SetOrganizationRequirement(ctx, admin, true); !errors.Is(err, twofactor.ErrOwnSecondFactorFirst) {
		t.Errorf("admin without own 2FA: %v", err)
	}
	w.enable(t)
	stale := admin
	stale.StepUpAt = w.now.Add(-time.Hour)
	if err := w.svc.SetOrganizationRequirement(ctx, stale, true); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("old step-up: %v", err)
	}
	admin.StepUpAt = *w.now
	if err := w.svc.SetOrganizationRequirement(ctx, admin, true); err != nil {
		t.Fatal(err)
	}
	if required, _ := w.svc.OrganizationRequirement(ctx, operator); !required || len(w.store.audits) != 1 ||
		w.store.audits[0].Details["required"] != "true" {
		t.Errorf("requirement %v, audits %+v", required, w.store.audits)
	}
	if err := w.svc.SetOrganizationRequirement(ctx, admin, false); err != nil {
		t.Errorf("making it optional again: %v", err)
	}
}
