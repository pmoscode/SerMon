package webapi

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/agentca"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
)

type fakeAgentCA struct{ states []agentca.AuthorityState }

func (f fakeAgentCA) Status(context.Context, access.Caller) ([]agentca.AuthorityState, error) {
	return f.states, nil
}
func (fakeAgentCA) StartRollover(context.Context, access.Caller) error { return nil }
func (fakeAgentCA) Retire(context.Context, access.Caller, int32) error { return nil }

// V1-19: the page shows when each CA expires; a retired one keeps its retirement time.
func TestGetAgentCAStatus_NamesTheExpiry(t *testing.T) {
	created := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	retired := created.Add(800 * 24 * time.Hour)
	h := NewAgentCAHandler(fakeAgentCA{states: []agentca.AuthorityState{
		{ID: 1, CreatedAt: created, RetiredAt: &retired, ExpiresAt: created.Add(730 * 24 * time.Hour), RootFingerprint: "ab"},
		{ID: 2, Issuing: true, CreatedAt: retired, ExpiresAt: retired.Add(730 * 24 * time.Hour), RootFingerprint: "cd", CertificatesInUse: 3},
	}}, slog.New(slog.DiscardHandler))
	ctx := context.WithValue(context.WithValue(context.Background(), principalKey{}, authz.Principal{Superadmin: true}), sessionKey{}, session.Session{})
	res, err := h.GetAgentCAStatus(ctx, connect.NewRequest(&webv1.GetAgentCAStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	cas := res.Msg.GetAuthorities()
	if len(cas) != 2 || !cas[0].GetExpireTime().AsTime().Equal(created.Add(730*24*time.Hour)) ||
		!cas[0].GetRetireTime().AsTime().Equal(retired) || !cas[1].GetExpireTime().AsTime().Equal(retired.Add(730*24*time.Hour)) ||
		cas[1].GetRetireTime() != nil || !cas[1].GetIssuing() || cas[1].GetCertificatesInUse() != 3 {
		t.Errorf("authorities = %v", cas)
	}
}
