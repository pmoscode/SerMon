package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres/sqlcdb"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/account"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

// Export implements account.Store: the tables of the account by its ID, the tenant tables inside
// each of its organizations, every query filtered by the account; the export is recorded in the
// instance's log.
func (s *AccountStore) Export(ctx context.Context, user identity.UserID, audit access.AuditEntry) (account.Export, error) {
	id := uuid.UUID(user)
	out := account.Export{
		Passkeys: []account.ExportedPasskey{}, Identities: []account.ExportedIdentity{}, Sessions: []account.ExportedSession{},
		Memberships: []account.ExportedMember{}, Dashboards: []account.ExportedDashboard{}, Audit: []account.ExportedAudit{},
	}
	err := s.db.instanceTx(ctx, func(q *sqlcdb.Queries) error {
		if err := exportOwn(ctx, q, id, &out); err != nil {
			return err
		}
		return s.audit(ctx, q, audit)
	})
	if err != nil {
		return account.Export{}, err
	}
	orgs, err := s.db.withoutTenant().UserOrganizations(ctx, id)
	if err != nil {
		return account.Export{}, fmt.Errorf("organizations: %w", err)
	}
	for _, o := range orgs {
		scope, err := tenant.ForAllGroups(tenant.OrgID(o.OrgID))
		if err != nil {
			return account.Export{}, fmt.Errorf("organization: %w", err)
		}
		if err := s.db.InTenant(ctx, scope, func(q *sqlcdb.Queries) error { return exportInOrg(ctx, q, id, o.Name, &out) }); err != nil {
			return account.Export{}, err
		}
	}
	return out, nil
}

func exportOwn(ctx context.Context, q *sqlcdb.Queries, id uuid.UUID, out *account.Export) error {
	profile, err := q.AccountProfile(ctx, id)
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}
	out.Account = account.ExportedAccount{ID: id.String(), Email: profile.Email, DisplayName: profile.DisplayName, CreatedAt: profile.CreatedAt}
	if changed, err := q.ExportPassword(ctx, id); err != nil {
		return fmt.Errorf("password: %w", err)
	} else if len(changed) > 0 {
		out.Password = &account.ExportedPassword{ChangedAt: changed[0]}
	}
	if since, err := q.ExportTOTP(ctx, id); err != nil {
		return fmt.Errorf("totp: %w", err)
	} else if len(since) > 0 {
		out.TwoFactor.TOTP, out.TwoFactor.TOTPSince = true, since[0]
	}
	left, err := q.ExportRecoveryCodesLeft(ctx, id)
	if err != nil {
		return fmt.Errorf("recovery codes: %w", err)
	}
	out.TwoFactor.RecoveryCodesLeft = int(left)
	passkeys, err := q.ExportPasskeys(ctx, id)
	if err != nil {
		return fmt.Errorf("passkeys: %w", err)
	}
	for _, p := range passkeys {
		out.Passkeys = append(out.Passkeys, account.ExportedPasskey{Name: p.Name, CreatedAt: p.CreatedAt, LastUsedAt: p.LastUsedAt})
	}
	identities, err := q.ExportIdentities(ctx, id)
	if err != nil {
		return fmt.Errorf("identities: %w", err)
	}
	for _, i := range identities {
		out.Identities = append(out.Identities, account.ExportedIdentity{
			Issuer: i.Issuer, Subject: i.Subject, Email: i.Email.String, CreatedAt: i.CreatedAt, LastLoginAt: i.LastLoginAt,
		})
	}
	sessions, err := q.ExportSessions(ctx, id)
	if err != nil {
		return fmt.Errorf("sessions: %w", err)
	}
	for _, se := range sessions {
		out.Sessions = append(out.Sessions, account.ExportedSession{CreatedAt: se.CreatedAt, LastSeenAt: se.LastSeenAt})
	}
	switch change, err := q.GetEmailChange(ctx, id); {
	case err == nil:
		out.EmailChange = &account.ExportedChange{NewEmail: change.NewEmail, ExpiresAt: change.ExpiresAt}
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("e-mail change: %w", err)
	}
	return nil
}

func exportInOrg(ctx context.Context, q *sqlcdb.Queries, id uuid.UUID, org string, out *account.Export) error {
	joined, err := q.ExportMembership(ctx, id)
	if err != nil {
		return fmt.Errorf("membership: %w", err)
	}
	if len(joined) == 0 {
		return nil
	}
	member := account.ExportedMember{Organization: org, JoinedAt: joined[0], Roles: []account.ExportedRole{}}
	roles, err := q.ExportRoles(ctx, id)
	if err != nil {
		return fmt.Errorf("roles: %w", err)
	}
	for _, r := range roles {
		member.Roles = append(member.Roles, account.ExportedRole{Group: r.GroupName, Role: r.Role, Origin: r.Origin})
	}
	out.Memberships = append(out.Memberships, member)
	dashboards, err := q.ExportDashboards(ctx, id)
	if err != nil {
		return fmt.Errorf("dashboards: %w", err)
	}
	for _, d := range dashboards {
		out.Dashboards = append(out.Dashboards, account.ExportedDashboard{Organization: org, Title: d.Title, Shared: d.Shared, UpdatedAt: d.UpdatedAt})
	}
	entries, err := q.ExportAuditEntries(ctx, sqlcdb.ExportAuditEntriesParams{UserID: &id, MaxRows: MaxExportedAuditEntries})
	if err != nil {
		return fmt.Errorf("audit entries: %w", err)
	}
	for _, e := range entries {
		out.Audit = append(out.Audit, account.ExportedAudit{Organization: org, At: e.At, Action: e.Action})
	}
	return nil
}
