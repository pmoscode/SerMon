//go:build integration

package webapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/loki"
	"github.com/pmoscode/SerMon/server/internal/adapters/loki/lokitest"
	"github.com/pmoscode/SerMon/server/internal/domain/logql"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// newLifecycleOrg creates an organization whose first admin has joined; it returns its ID,
// name, the admin's address and the logged-in admin.
func newLifecycleOrg(t *testing.T, s stack, sam loggedIn) (string, string, string, loggedIn) {
	t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	name, adminEmail := "Delta "+suffix, "delta-admin-"+suffix+"@example.org"
	created, err := sam.organization.CreateOrganization(ctx, connect.NewRequest(&webv1.CreateOrganizationRequest{Name: name, AdminEmail: adminEmail}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anonymous(s).AcceptInvitation(ctx, connect.NewRequest(&webv1.AcceptInvitationRequest{
		Token: created.Msg.GetInvitationToken(), DisplayName: "Delta Admin", Password: userPassword,
	})); err != nil {
		t.Fatal(err)
	}
	return created.Msg.GetOrganization().GetId(), name, adminEmail, login(t, s, adminEmail)
}

func summaryOf(t *testing.T, sam loggedIn, org string) *webv1.OrganizationSummary {
	t.Helper()
	list, err := sam.organization.ListOrganizations(context.Background(), connect.NewRequest(&webv1.ListOrganizationsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range list.Msg.GetOrganizations() {
		if o.GetId() == org {
			return o
		}
	}
	return nil
}

// memberOf logs in and reports whether the login offers the organization.
func memberOf(t *testing.T, s stack, email, org string) bool {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	auth := webv1connect.NewAuthServiceClient(s.httpClient(jar, s.origin), s.server.URL)
	res, err := auth.Login(context.Background(), connect.NewRequest(&webv1.LoginRequest{Email: email, Password: userPassword}))
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range res.Msg.GetSession().GetOrganizations() {
		if o.GetId() == org {
			return true
		}
	}
	return false
}

func instanceAudit(t *testing.T, org, action string) int {
	t.Helper()
	var n int
	ownerQuery(t, "SELECT count(*) FROM instance_audit_log WHERE target = 'org:"+org+"' AND action = '"+action+"'", &n)
	return n
}

// verifyExport recomputes the hash chain from the exported file alone.
func verifyExport(t *testing.T, file []byte) int {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(file)), "\n")
	var header struct {
		Organization string `json:"organization"`
		Entries      int    `json:"entries"`
		ChainValid   bool   `json:"chain_valid"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil || !header.ChainValid || header.Entries != len(lines)-1 {
		t.Fatalf("header %s: %v", lines[0], err)
	}
	prev := ""
	for _, raw := range lines[1:] {
		var e struct {
			Seq      int64  `json:"seq"`
			At       string `json:"at"`
			Actor    string `json:"actor"`
			Action   string `json:"action"`
			Target   string `json:"target"`
			Details  string `json:"details"`
			PrevHash string `json:"prev_hash"`
			Hash     string `json:"hash"`
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		if e.PrevHash != prev {
			t.Fatalf("seq %d: prev_hash %s, want %s", e.Seq, e.PrevHash, prev)
		}
		prevBytes, _ := hex.DecodeString(prev)
		fields := strings.Join([]string{header.Organization, strconv.FormatInt(e.Seq, 10), e.At, e.Actor, e.Action, e.Target, e.Details}, "\x1f")
		sum := sha256.Sum256(append(prevBytes, fields...))
		if hex.EncodeToString(sum[:]) != e.Hash {
			t.Fatalf("seq %d: hash does not match the line %s", e.Seq, raw)
		}
		prev = e.Hash
	}
	return header.Entries
}

func TestOrganizationAPI_SuperadminLocksExportsUnlocksAndDeletes(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	seedLogin(t, samID, "sam@example.org")
	ownerSQL(t, "INSERT INTO superadmins (user_id) VALUES ('"+samID+"') ON CONFLICT DO NOTHING")
	sam := login(t, s, "sam@example.org")
	ctx := context.Background()
	org, name, adminEmail, admin := newLifecycleOrg(t, s, sam)

	// The name must be typed exactly; an org admin may not lock.
	if _, err := sam.organization.LockOrganization(ctx, connect.NewRequest(&webv1.LockOrganizationRequest{OrganizationId: org, ConfirmName: "Delta"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("lock with a wrong name: %v, want INVALID_ARGUMENT", err)
	}
	if _, err := admin.organization.LockOrganization(ctx, connect.NewRequest(&webv1.LockOrganizationRequest{OrganizationId: org, ConfirmName: name})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("lock by the org admin: %v, want PERMISSION_DENIED", err)
	}
	if _, err := sam.organization.ExportOrganizationAudit(ctx, connect.NewRequest(&webv1.ExportOrganizationAuditRequest{OrganizationId: org})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("export while active: %v, want FAILED_PRECONDITION", err)
	}
	if _, err := sam.organization.LockOrganization(ctx, connect.NewRequest(&webv1.LockOrganizationRequest{OrganizationId: org, ConfirmName: name})); err != nil {
		t.Fatal(err)
	}

	// The lock ends the admin's session; a new login no longer offers the organization.
	if _, err := admin.access.ListMembers(ctx, connect.NewRequest(&webv1.ListMembersRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("session of the admin after the lock: %v, want UNAUTHENTICATED", err)
	}
	if memberOf(t, s, adminEmail, org) {
		t.Error("login lists the locked organization")
	}
	if o := summaryOf(t, sam, org); o.GetStatus() != webv1.OrganizationStatus_ORGANIZATION_STATUS_LOCKED || o.GetLockTime() == nil || o.GetDeletionTime() != nil {
		t.Errorf("summary after the lock = %v", o)
	}

	// The superadmin exports the audit log; the chain can be checked from the file alone.
	exported, err := sam.organization.ExportOrganizationAudit(ctx, connect.NewRequest(&webv1.ExportOrganizationAuditRequest{OrganizationId: org}))
	if err != nil || !exported.Msg.GetChainValid() || exported.Msg.GetFileName() != "audit-"+org+".jsonl" {
		t.Fatalf("ExportOrganizationAudit() = %v, %v", exported, err)
	}
	if n := verifyExport(t, exported.Msg.GetContent()); n < 3 { // create, join, lock
		t.Errorf("exported entries = %d", n)
	}
	if instanceAudit(t, org, "org.lock") != 1 || instanceAudit(t, org, "org.audit_export") != 1 {
		t.Error("lock or export missing in the instance audit log")
	}

	// Unlocking restores access.
	if _, err := sam.organization.UnlockOrganization(ctx, connect.NewRequest(&webv1.UnlockOrganizationRequest{OrganizationId: org, ConfirmName: name})); err != nil {
		t.Fatal(err)
	}
	if !memberOf(t, s, adminEmail, org) {
		t.Error("login lacks the unlocked organization")
	}
	if o := summaryOf(t, sam, org); o.GetStatus() != webv1.OrganizationStatus_ORGANIZATION_STATUS_ACTIVE || o.GetLockTime() != nil {
		t.Errorf("summary after the unlock = %v", o)
	}

	// Scheduled: locked, with the date of the deletion.
	scheduled, err := sam.organization.ScheduleOrganizationDeletion(ctx, connect.NewRequest(&webv1.ScheduleOrganizationDeletionRequest{OrganizationId: org, ConfirmName: name}))
	if err != nil {
		t.Fatal(err)
	}
	if due := time.Until(scheduled.Msg.GetDeletionTime().AsTime()); due < 29*24*time.Hour || due > 31*24*time.Hour {
		t.Errorf("deletion in %v, want 30 days", due)
	}
	if o := summaryOf(t, sam, org); o.GetStatus() != webv1.OrganizationStatus_ORGANIZATION_STATUS_DELETION_SCHEDULED || o.GetDeletionTime() == nil {
		t.Errorf("summary after scheduling = %v", o)
	}

	// Deleting at once needs a fresh step-up.
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+samID+"'")
	if _, err := sam.organization.DeleteOrganization(ctx, connect.NewRequest(&webv1.DeleteOrganizationRequest{OrganizationId: org, ConfirmName: name})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("delete with an old login: %v, want FAILED_PRECONDITION", err)
	}
	sam = login(t, s, "sam@example.org")
	deleted, err := sam.organization.DeleteOrganization(ctx, connect.NewRequest(&webv1.DeleteOrganizationRequest{OrganizationId: org, ConfirmName: name}))
	if err != nil || deleted.Msg.GetAccountsDeleted() != 1 {
		t.Fatalf("DeleteOrganization() = %v, %v", deleted, err)
	}
	if summaryOf(t, sam, org) != nil {
		t.Error("the deleted organization is still listed")
	}
	var users int
	ownerQuery(t, "SELECT count(*) FROM users WHERE email = '"+adminEmail+"' OR id = '"+samID+"'", &users)
	if users != 1 {
		t.Errorf("users left = %d, want only the superadmin", users)
	}
	if instanceAudit(t, org, "org.delete") != 1 {
		t.Error("deletion missing in the instance audit log")
	}
	// Its logs went first (M3-10): the deletion of its whole tenant is recorded in Loki, Alpha's
	// tenant has none.
	lokiClient := loki.New(lokitest.Shared(t))
	requests, err := lokiClient.Deletions(ctx, tenant.OrgID(uuid.MustParse(org)))
	if err != nil || len(requests) != 1 || requests[0].Query != logql.Everything().String() {
		t.Errorf("Loki deletions of the organization = %+v, %v", requests, err)
	}
	if alpha, err := lokiClient.Deletions(ctx, tenant.OrgID(uuid.MustParse(orgAlpha))); err != nil || len(alpha) != 0 {
		t.Errorf("Loki deletions of Alpha = %+v, %v", alpha, err)
	}
}
