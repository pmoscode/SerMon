package agentca_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/pki"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/role"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

var (
	t0   = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	orgA = tenant.OrgID(uuid.MustParse("0193f5a0-0000-7000-8000-00000000000a"))
	node = identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000c1"))
)

// fakeStore is an in-memory agentca.Store.
type fakeStore struct {
	auths   []*fakeAuthority
	certs   map[string]agentca.CertificateRecord
	revoked map[string]time.Time
	inserts int
}

type fakeAuthority struct {
	id      int32
	rec     agentca.AuthorityRecord
	issuing bool
	retired *time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{certs: map[string]agentca.CertificateRecord{}, revoked: map[string]time.Time{}}
}

// first returns the first stored authority record (to inspect or tamper with it).
func (f *fakeStore) first() *agentca.AuthorityRecord { return &f.auths[0].rec }

func (f *fakeStore) TrustedAuthorities(context.Context) ([]agentca.StoredAuthority, error) {
	var out []agentca.StoredAuthority
	for _, a := range f.auths {
		if a.retired == nil {
			out = append(out, agentca.StoredAuthority{ID: a.id, Record: a.rec, Issuing: a.issuing})
		}
	}
	return out, nil
}

func (f *fakeStore) InsertAuthority(_ context.Context, r agentca.AuthorityRecord) error {
	f.inserts++
	if len(f.auths) > 0 {
		return agentca.ErrAuthorityExists
	}
	f.auths = append(f.auths, &fakeAuthority{id: 1, rec: r, issuing: true})
	return nil
}

func (f *fakeStore) StartRollover(ctx context.Context, r agentca.AuthorityRecord, _ identity.UserID) error {
	trusted, _ := f.TrustedAuthorities(ctx)
	if len(trusted) > 1 {
		return agentca.ErrRolloverInProgress
	}
	for _, a := range f.auths {
		a.issuing = false
	}
	f.auths = append(f.auths, &fakeAuthority{id: int32(len(f.auths) + 1), rec: r, issuing: true})
	return nil
}

func (f *fakeStore) Retire(_ context.Context, id int32, _ identity.UserID, now time.Time) error {
	for _, a := range f.auths {
		if a.id != id || a.retired != nil {
			continue
		}
		if a.issuing {
			return agentca.ErrIssuingAuthority
		}
		if f.inUse(id, now) > 0 {
			return agentca.ErrAuthorityInUse
		}
		a.retired = &now
		return nil
	}
	return agentca.ErrUnknownAuthority
}

func (f *fakeStore) Authorities(_ context.Context, now time.Time) ([]agentca.AuthorityState, error) {
	var out []agentca.AuthorityState
	for _, a := range f.auths {
		out = append(out, agentca.AuthorityState{ID: a.id, Issuing: a.issuing, RetiredAt: a.retired, CertificatesInUse: f.inUse(a.id, now)})
	}
	return out, nil
}

func (f *fakeStore) inUse(id int32, now time.Time) int64 {
	var n int64
	for serial, c := range f.certs {
		if _, revoked := f.revoked[serial]; c.Authority == id && !revoked && !now.Before(c.NotBefore) && now.Before(c.NotAfter) {
			n++
		}
	}
	return n
}

func (f *fakeStore) RecordCertificate(_ context.Context, scope tenant.Scope, c agentca.CertificateRecord) error {
	if c.Org != scope.OrgID() {
		return errors.New("cross-tenant write")
	}
	f.certs[c.Serial] = c
	return nil
}

func (f *fakeStore) Revoke(_ context.Context, scope tenant.Scope, serial string, at time.Time) (bool, error) {
	c, ok := f.certs[serial]
	if !ok || c.Org != scope.OrgID() {
		return false, nil
	}
	if _, already := f.revoked[serial]; already {
		return false, nil
	}
	f.revoked[serial] = at
	return true, nil
}

func (f *fakeStore) Usable(_ context.Context, serial string, at time.Time) (bool, error) {
	c, ok := f.certs[serial]
	_, revoked := f.revoked[serial]
	return ok && !revoked && !at.Before(c.NotBefore) && at.Before(c.NotAfter), nil
}

func newBox(t *testing.T) *secrets.Box {
	t.Helper()
	line, err := secrets.GenerateKeyLine(1)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.ParseKeys(line)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func clockAt(at *time.Time) func() time.Time { return func() time.Time { return *at } }

func scopeA(t *testing.T) tenant.Scope {
	t.Helper()
	s, err := tenant.ForAllGroups(orgA)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func csrDER(t *testing.T) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestBootstrap_CreatesAndStoresAnAuthorityOnce(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	first, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}

	second, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	if err != nil {
		t.Fatalf("second Bootstrap() error = %v", err)
	}
	if store.inserts != 1 || !first.TrustAnchor().Equal(second.TrustAnchor()) {
		t.Errorf("want one stored authority reused on restart, inserts=%d", store.inserts)
	}
}

func TestBootstrap_StoresPrivateKeysOnlySealed(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); err != nil {
		t.Fatal(err)
	}

	rec := store.first()
	for name, sealed := range map[string][]byte{"root": rec.RootKeySealed, "intermediate": rec.IntermediateKeySealed} {
		if _, err := x509.ParsePKCS8PrivateKey(sealed); err == nil {
			t.Errorf("%s key is stored in clear", name)
		}
	}
}

func TestBootstrap_FailsWithAnotherMasterKey(t *testing.T) {
	store, now := newFakeStore(), t0
	if _, err := agentca.Bootstrap(context.Background(), store, newBox(t), clockAt(&now)); err != nil {
		t.Fatal(err)
	}

	if _, err := agentca.Bootstrap(context.Background(), store, newBox(t), clockAt(&now)); !errors.Is(err, secrets.ErrDecrypt) {
		t.Errorf("Bootstrap() with a foreign master key error = %v, want ErrDecrypt", err)
	}
}

func TestBootstrap_SealedKeysCannotBeSwapped(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); err != nil {
		t.Fatal(err)
	}
	rec := store.first()
	rec.RootKeySealed, rec.IntermediateKeySealed = rec.IntermediateKeySealed, rec.RootKeySealed

	if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); err == nil {
		t.Error("swapped sealed keys must be rejected (associated data binds them to their certificate)")
	}
}

func TestBootstrap_ConcurrentCreationUsesTheWinner(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	winner, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	racing := &racingStore{fakeStore: store}

	loser, err := agentca.Bootstrap(context.Background(), racing, box, clockAt(&now))
	if err != nil {
		t.Fatalf("Bootstrap() after losing the race error = %v", err)
	}
	if !loser.TrustAnchor().Equal(winner.TrustAnchor()) {
		t.Error("the instance that lost the race must load the winner's authority")
	}
}

// racingStore pretends no authority exists on the first read (another instance inserts meanwhile).
type racingStore struct {
	*fakeStore
	reads int
}

func (r *racingStore) TrustedAuthorities(ctx context.Context) ([]agentca.StoredAuthority, error) {
	r.reads++
	if r.reads == 1 {
		return nil, nil
	}
	return r.fakeStore.TrustedAuthorities(ctx)
}

func TestIssue_RecordsTheCertificateAndItIsUsableUntilExpiry(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	issued, err := svc.IssueAgentCertificate(context.Background(), scopeA(t), csrDER(t), node)
	if err != nil {
		t.Fatalf("IssueAgentCertificate() error = %v", err)
	}
	rec, ok := store.certs[issued.Serial]
	if !ok || rec.Org != orgA || rec.Node != node {
		t.Fatalf("certificate not recorded for org/node: %+v", rec)
	}
	cert, _ := x509.ParseCertificate(issued.DER)
	if gotOrg, gotNode, err := pki.AgentIdentity(cert); err != nil || gotOrg != orgA || gotNode != node {
		t.Errorf("identity = %v, %v, %v", gotOrg, gotNode, err)
	}

	if usable, _ := svc.CertificateUsable(context.Background(), issued.Serial); !usable {
		t.Error("fresh certificate must be usable")
	}
	now = t0.Add(pki.AgentCertificateLifetime)
	if usable, _ := svc.CertificateUsable(context.Background(), issued.Serial); usable {
		t.Error("expired certificate must not be usable")
	}
}

func TestIssue_RequiresAValidScope(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if _, err := svc.IssueAgentCertificate(context.Background(), tenant.Scope{}, csrDER(t), node); err == nil {
		t.Error("issuing without tenant scope must fail")
	}
}

func TestRevoke_MakesTheCertificateUnusableImmediately(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	issued, _ := svc.IssueAgentCertificate(context.Background(), scopeA(t), csrDER(t), node)

	if err := svc.Revoke(context.Background(), scopeA(t), issued.Serial); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if usable, _ := svc.CertificateUsable(context.Background(), issued.Serial); usable {
		t.Error("revoked certificate must not be usable")
	}
	if err := svc.Revoke(context.Background(), scopeA(t), issued.Serial); !errors.Is(err, agentca.ErrNotFound) {
		t.Errorf("second Revoke() error = %v, want ErrNotFound", err)
	}
}

func TestCertificateUsable_UnknownSerialIsUnusable(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if usable, err := svc.CertificateUsable(context.Background(), "00000000000000000000000000000000"); err != nil || usable {
		t.Errorf("unknown serial: usable=%v err=%v, want false", usable, err)
	}
}

func TestTrustAnchor_IsTheRootCertificate(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if !svc.TrustAnchor().IsCA || !bytes.Equal(svc.TrustAnchor().RawSubject, svc.TrustAnchor().RawIssuer) {
		t.Error("trust anchor must be the self-signed root")
	}
}

// failingStore returns errs for the named operations.
type failingStore struct {
	*fakeStore
	fail map[string]bool
}

var errStore = errors.New("database unavailable")

func (f failingStore) TrustedAuthorities(ctx context.Context) ([]agentca.StoredAuthority, error) {
	if f.fail["active"] {
		return nil, errStore
	}
	return f.fakeStore.TrustedAuthorities(ctx)
}

func (f failingStore) InsertAuthority(ctx context.Context, r agentca.AuthorityRecord) error {
	if f.fail["insert"] {
		return errStore
	}
	return f.fakeStore.InsertAuthority(ctx, r)
}

func (f failingStore) RecordCertificate(ctx context.Context, s tenant.Scope, c agentca.CertificateRecord) error {
	if f.fail["record"] {
		return errStore
	}
	return f.fakeStore.RecordCertificate(ctx, s, c)
}

func (f failingStore) Revoke(ctx context.Context, s tenant.Scope, serial string, at time.Time) (bool, error) {
	if f.fail["revoke"] {
		return false, errStore
	}
	return f.fakeStore.Revoke(ctx, s, serial, at)
}

func (f failingStore) Usable(ctx context.Context, serial string, at time.Time) (bool, error) {
	if f.fail["usable"] {
		return false, errStore
	}
	return f.fakeStore.Usable(ctx, serial, at)
}

func TestService_PropagatesStoreErrors(t *testing.T) {
	box, now := newBox(t), t0
	for _, op := range []string{"active", "insert"} {
		store := failingStore{fakeStore: newFakeStore(), fail: map[string]bool{op: true}}
		if _, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now)); !errors.Is(err, errStore) {
			t.Errorf("Bootstrap with failing %s: error = %v, want errStore", op, err)
		}
	}

	store := failingStore{fakeStore: newFakeStore(), fail: map[string]bool{}}
	svc, err := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	if err != nil {
		t.Fatal(err)
	}
	store.fail["record"], store.fail["revoke"], store.fail["usable"] = true, true, true
	if _, err := svc.IssueAgentCertificate(context.Background(), scopeA(t), csrDER(t), node); !errors.Is(err, errStore) {
		t.Errorf("Issue: error = %v, want errStore", err)
	}
	if err := svc.Revoke(context.Background(), scopeA(t), "x"); !errors.Is(err, errStore) {
		t.Errorf("Revoke: error = %v, want errStore", err)
	}
	if _, err := svc.CertificateUsable(context.Background(), "x"); !errors.Is(err, errStore) {
		t.Errorf("Usable: error = %v, want errStore", err)
	}
}

func TestIssue_RejectsInvalidCSRs(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	if _, err := svc.IssueAgentCertificate(context.Background(), scopeA(t), []byte("garbage"), node); !errors.Is(err, pki.ErrInvalidCSR) {
		t.Errorf("error = %v, want ErrInvalidCSR", err)
	}
	if len(store.certs) != 0 {
		t.Error("nothing must be recorded for a rejected CSR")
	}
}

func TestSign_ReturnsTheRecordWithoutStoringIt(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))

	issued, rec, err := svc.SignAgentCertificate(orgA, csrDER(t), node)
	if err != nil {
		t.Fatalf("SignAgentCertificate() error = %v", err)
	}
	if len(store.certs) != 0 {
		t.Error("signing alone must not record the certificate (the caller records it in its transaction)")
	}
	if rec.Serial != issued.Serial || rec.Org != orgA || rec.Node != node || !rec.NotAfter.Equal(issued.NotAfter) {
		t.Errorf("record = %+v, issued serial %s", rec, issued.Serial)
	}
	if usable, _ := svc.CertificateUsable(context.Background(), issued.Serial); usable {
		t.Error("an unrecorded certificate must not be usable")
	}
	if _, _, err := svc.SignAgentCertificate(tenant.OrgID{}, csrDER(t), node); err == nil {
		t.Error("signing without organization must fail")
	}
}

func TestTrustChain_ContainsIntermediateAndRoot(t *testing.T) {
	svc, _ := agentca.Bootstrap(context.Background(), newFakeStore(), newBox(t), clockAt(&t0))
	if svc.Intermediate() == nil || svc.TrustAnchor() == nil || svc.Intermediate().CheckSignatureFrom(svc.TrustAnchor()) != nil {
		t.Error("intermediate must be signed by the trust anchor")
	}
}

func TestIssueServerCertificate_UsesTheServiceClock(t *testing.T) {
	now := t0
	svc, _ := agentca.Bootstrap(context.Background(), newFakeStore(), newBox(t), clockAt(&now))
	cert, err := svc.IssueServerCertificate([]string{"agents.example.org"})
	if err != nil || !cert.Leaf.NotAfter.Equal(t0.Add(pki.ServerCertificateLifetime)) {
		t.Errorf("IssueServerCertificate() = %v, %v", cert.Leaf, err)
	}
	if _, err := svc.IssueServerCertificate(nil); err == nil {
		t.Error("no hosts must fail")
	}
}

var super = access.Caller{Principal: authz.Principal{User: identity.UserID(uuid.MustParse("0193f5a0-0000-7000-8000-0000000000e1")), Superadmin: true}}

func superadmin(stepUp time.Duration) access.Caller {
	c := super
	c.StepUpAt = t0.Add(-stepUp)
	return c
}

func verifiesAgainst(t *testing.T, issued []byte, root, inter *x509.Certificate) bool {
	t.Helper()
	cert, err := x509.ParseCertificate(issued)
	if err != nil {
		t.Fatal(err)
	}
	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root)
	inters.AddCert(inter)
	_, err = cert.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inters, CurrentTime: t0.Add(time.Hour),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err == nil
}

func TestRollover_TrustsBothCAsAndIssuesAgentCertificatesWithTheNewOne(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	oldRoot, oldInter := svc.TrustAnchor(), svc.Intermediate()
	before, _, _ := svc.SignAgentCertificate(orgA, csrDER(t), node)
	gen := svc.Generation()

	if err := svc.StartRollover(context.Background(), superadmin(time.Minute)); err != nil {
		t.Fatalf("StartRollover() error = %v", err)
	}
	if len(svc.TrustAnchors()) != 2 || len(svc.Intermediates()) != 2 || svc.Generation() == gen {
		t.Errorf("trust bundle %d roots, generation %d→%d", len(svc.TrustAnchors()), gen, svc.Generation())
	}
	after, rec, _ := svc.SignAgentCertificate(orgA, csrDER(t), node)
	if !verifiesAgainst(t, after.DER, svc.TrustAnchor(), svc.Intermediate()) || svc.TrustAnchor().Equal(oldRoot) || rec.Authority != 2 {
		t.Error("new agent certificates must come from the new CA")
	}
	if !verifiesAgainst(t, before.DER, oldRoot, oldInter) {
		t.Error("certificates of the old CA stay valid")
	}
	server, _ := svc.IssueServerCertificate([]string{"agents.example.org"})
	if !verifiesAgainst(t, server.Leaf.Raw, oldRoot, oldInter) {
		t.Error("during the rollover the server certificate comes from the old CA (agents may not know the new one yet)")
	}
	if err := svc.StartRollover(context.Background(), superadmin(time.Minute)); !errors.Is(err, agentca.ErrRolloverInProgress) {
		t.Errorf("second rollover: %v, want ErrRolloverInProgress", err)
	}
}

func TestRetire_WaitsForTheOldCertificatesAndThenSwitchesTheServerCertificate(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	svc, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	old, _ := svc.IssueAgentCertificate(context.Background(), scopeA(t), csrDER(t), node)
	if err := svc.StartRollover(context.Background(), superadmin(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err := svc.Retire(context.Background(), superadmin(time.Minute), 1); !errors.Is(err, agentca.ErrAuthorityInUse) {
		t.Errorf("retire with valid certificates: %v, want ErrAuthorityInUse", err)
	}
	if err := svc.Retire(context.Background(), superadmin(time.Minute), 2); !errors.Is(err, agentca.ErrIssuingAuthority) {
		t.Errorf("retire the issuing CA: %v, want ErrIssuingAuthority", err)
	}
	states, _ := svc.Status(context.Background(), superadmin(time.Hour))
	if len(states) != 2 || states[0].CertificatesInUse != 1 || !states[1].Issuing {
		t.Errorf("status = %+v", states)
	}

	if err := svc.Revoke(context.Background(), scopeA(t), old.Serial); err != nil { // e.g. rotated away
		t.Fatal(err)
	}
	gen := svc.Generation()
	if err := svc.Retire(context.Background(), superadmin(time.Minute), 1); err != nil {
		t.Fatalf("Retire() error = %v", err)
	}
	if len(svc.TrustAnchors()) != 1 || svc.Generation() == gen {
		t.Errorf("after retirement: %d roots, generation unchanged %v", len(svc.TrustAnchors()), svc.Generation() == gen)
	}
	server, _ := svc.IssueServerCertificate([]string{"agents.example.org"})
	if !verifiesAgainst(t, server.Leaf.Raw, svc.TrustAnchor(), svc.Intermediate()) {
		t.Error("after retirement the server certificate comes from the new CA")
	}
}

func TestRolloverOperations_NeedASuperadminWithStepUp(t *testing.T) {
	svc, _ := agentca.Bootstrap(context.Background(), newFakeStore(), newBox(t), clockAt(&t0))
	orgAdmin := access.Caller{Principal: authz.Principal{
		User: super.Principal.User, Org: orgA,
		Bindings: []authz.Binding{{AllGroups: true, Role: role.Admin}},
	}, StepUpAt: t0}
	if err := svc.StartRollover(context.Background(), orgAdmin); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("org admin: %v, want ErrForbidden", err)
	}
	if _, err := svc.Status(context.Background(), orgAdmin); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("status by org admin: %v, want ErrForbidden", err)
	}
	if err := svc.StartRollover(context.Background(), superadmin(time.Hour)); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("stale step-up: %v, want ErrStepUpRequired", err)
	}
	if err := svc.Retire(context.Background(), superadmin(time.Hour), 1); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("retire with stale step-up: %v, want ErrStepUpRequired", err)
	}
}

func TestRefresh_PicksUpARolloverOfAnotherInstance(t *testing.T) {
	store, box, now := newFakeStore(), newBox(t), t0
	a, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	b, _ := agentca.Bootstrap(context.Background(), store, box, clockAt(&now))
	if err := a.StartRollover(context.Background(), superadmin(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := b.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.TrustAnchors()) != 2 || !b.TrustAnchor().Equal(a.TrustAnchor()) {
		t.Error("the other instance must see the new issuing CA after a refresh")
	}
}
