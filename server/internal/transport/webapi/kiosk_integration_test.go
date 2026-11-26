//go:build integration

package webapi_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/durationpb"

	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
	"github.com/pmoscode/SerMon/server/internal/transport/webapi"
)

// kioskTestCalls is the limit of calls per token and minute of the test stack.
const kioskTestCalls = 40

// kioskReachable are the only procedures a kiosk token opens (M5-26). Every other procedure must
// answer a call with the token exactly as without it: UNAUTHENTICATED behind the Authenticator,
// and the same answer for the anonymous services, which take no token.
var kioskReachable = map[string]bool{
	"/sermon.web.v1.KioskService/GetKioskDashboard": true,
	"/sermon.web.v1.KioskService/QueryKioskWidget":  true,
}

// kioskAnonymous are the services without a session: the token neither opens nor changes them.
var kioskAnonymous = map[string]string{
	"sermon.web.v1.AuthService":       "sign-in: anonymous by design (session cookie)",
	"sermon.web.v1.SetupService":      "first start: the setup token",
	"sermon.web.v1.InvitationService": "the link of an invitation",
}

type procedure struct {
	path      string
	service   string
	streaming bool
}

func webProcedures(t *testing.T) []procedure {
	t.Helper()
	var out []procedure
	protoregistry.GlobalFiles.RangeFilesByPackage("sermon.web.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := range fd.Services().Len() {
			svc := fd.Services().Get(i)
			for j := range svc.Methods().Len() {
				m := svc.Methods().Get(j)
				out = append(out, procedure{
					path: "/" + string(svc.FullName()) + "/" + string(m.Name()), service: string(svc.FullName()), streaming: m.IsStreamingServer(),
				})
			}
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	if len(out) < 100 {
		t.Fatalf("only %d procedures – registry not loaded", len(out))
	}
	return out
}

// rawCall posts an empty message (unary JSON or one enveloped stream message) with the
// Authorization header (if any) and no cookie; it returns the Connect code ("" for success).
func rawCall(t *testing.T, s stack, p procedure, authorization string, body string) string {
	t.Helper()
	payload, contentType := []byte(body), "application/json"
	if p.streaming {
		frame := make([]byte, 5)
		binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
		payload, contentType = append(frame, payload...), "application/connect+json"
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.server.URL+p.path, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	res, err := s.httpClient(nil, s.origin).Do(req.WithContext(ctx))
	if d := time.Since(start); d > time.Second {
		t.Logf("%s took %v", p.path, d)
	}
	if err != nil {
		// The test stack mounts no SetupService: its nil handler ends the connection.
		return "no_answer"
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var answer struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if p.streaming {
		// The end of the stream carries the error: the last frame.
		for len(data) >= 5 {
			n := int(binary.BigEndian.Uint32(data[1:5]))
			if len(data) < 5+n {
				break
			}
			if data[0]&2 != 0 {
				_ = json.Unmarshal(data[5:5+n], &answer)
				return answer.Error.Code
			}
			data = data[5+n:]
		}
		return ""
	}
	if res.StatusCode == http.StatusOK {
		return ""
	}
	_ = json.Unmarshal(data, &answer)
	return answer.Code
}

func kioskHeader(secret string) string { return webapi.KioskAuthorization + secret }

// M5-26: the kiosk token opens the KioskService and nothing else – every procedure of the web
// API is called with it.
func TestKiosk_TheTokenOpensOnlyTheKioskService(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	token := kioskHeader(kioskSecretAl)
	for _, p := range webProcedures(t) {
		body := "{}"
		if p.path == "/sermon.web.v1.KioskService/QueryKioskWidget" {
			body = `{"widgetId":"cpu"}`
		}
		with := rawCall(t, s, p, token, body)
		without := rawCall(t, s, p, "", body)
		_, anonymous := kioskAnonymous[p.service]
		switch {
		case kioskReachable[p.path]:
			if with != "" {
				t.Errorf("%s with the token: %q, want success", p.path, with)
			}
			if without != "unauthenticated" {
				t.Errorf("%s without the token: %q, want unauthenticated", p.path, without)
			}
		case with != without:
			t.Errorf("%s answers %q with the token and %q without: the token must change nothing", p.path, with, without)
		case !anonymous && with != "unauthenticated":
			t.Errorf("%s with only a kiosk token: %q, want unauthenticated", p.path, with)
		}
	}
	for path := range kioskReachable {
		found := false
		for _, p := range webProcedures(t) {
			found = found || p.path == path
		}
		if !found {
			t.Errorf("%s is not a procedure any more – update kioskReachable", path)
		}
	}
}

// A session alone opens no kiosk procedure; a header of another scheme is no token.
func TestKiosk_ASessionIsNoKioskToken(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	hc := s.httpClient(ann.jar, s.origin)
	kioskClient := webv1connect.NewKioskServiceClient(hc, s.server.URL)
	if _, err := kioskClient.GetKioskDashboard(context.Background(), connect.NewRequest(&webv1.GetKioskDashboardRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("with a session cookie: %v", err)
	}
	p := procedure{path: "/sermon.web.v1.KioskService/GetKioskDashboard"}
	for _, header := range []string{"Bearer " + kioskSecretAl, "kiosk " + kioskSecretAl, kioskHeader(kioskSecretAl + "x"), kioskHeader("")} {
		if code := rawCall(t, s, p, header, "{}"); code != "unauthenticated" {
			t.Errorf("header %q: %q", header[:6], code)
		}
	}
}

// Revoked, expired, unshared, the creator no admin any more, the organization locked: nothing.
func TestKiosk_EndsWithTheTokenTheDashboardAndTheCreatorsRights(t *testing.T) {
	s := newStack(t, 1000)
	p := procedure{path: "/sermon.web.v1.KioskService/GetKioskDashboard"}
	token := kioskHeader(kioskSecretAl)
	for name, sql := range map[string][]string{
		"revoked":           {"UPDATE kiosk_tokens SET revoked_at = now() WHERE id = '" + kioskAl + "'"},
		"expired":           {"UPDATE kiosk_tokens SET expires_at = now() - interval '1 second', created_at = now() - interval '1 day' WHERE id = '" + kioskAl + "'"},
		"dashboard private": {"UPDATE dashboards SET shared = false WHERE id = '" + dashAl + "'"},
		"creator demoted":   {"UPDATE role_bindings SET role = 'operator' WHERE org_id = '" + orgAlpha + "' AND user_id = '" + annID + "'"},
		"creator removed":   {"DELETE FROM memberships WHERE org_id = '" + orgAlpha + "' AND user_id = '" + annID + "'"},
		"org locked":        {"UPDATE organizations SET locked_at = now() WHERE id = '" + orgAlpha + "'"},
	} {
		t.Run(name, func(t *testing.T) {
			seedAccessFixtures(t)
			ownerSQL(t, "UPDATE organizations SET locked_at = NULL WHERE id = '"+orgAlpha+"'")
			if code := rawCall(t, s, p, token, "{}"); code != "" {
				t.Fatalf("before: %q", code)
			}
			ownerSQL(t, append([]string{"SELECT set_config('sermon.org_id', '" + orgAlpha + "', false)"}, sql...)...)
			if code := rawCall(t, s, p, token, "{}"); code != "unauthenticated" {
				t.Errorf("after: %q, want unauthenticated", code)
			}
		})
	}
	ownerSQL(t, "UPDATE organizations SET locked_at = NULL WHERE id = '"+orgAlpha+"'")
}

// A dashboard can't name another organization's node, not even written by hand into the
// database: the readers run in the token's organization and find nothing.
func TestKiosk_ReadsOnlyTheTokensOrganization(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	betaNode := "0193f5a0-0000-7000-8000-0000000002b5"
	betaGroup := "0193f5a0-0000-7000-8000-0000000002b9"
	ownerSQL(t,
		"SELECT set_config('sermon.org_id', '"+orgBeta+"', false)",
		"INSERT INTO node_groups (id, org_id, name) VALUES ('"+betaGroup+"', '"+orgBeta+"', 'beta-web') ON CONFLICT DO NOTHING",
		"INSERT INTO nodes (id, org_id, group_id, hostname, machine_id, os_id, os_version, kernel_release, architecture, cpu_count, memory_bytes) VALUES ('"+betaNode+"', '"+orgBeta+"', '"+betaGroup+"', 'beta-01', '"+strings.Repeat("b", 32)+"', 'debian', '13', '6.12.0', 'amd64', 2, 1073741824) ON CONFLICT DO NOTHING",
		"UPDATE dashboards SET spec = replace(spec::text, '"+nodeAl+"', '"+betaNode+"')::jsonb WHERE id = '"+dashAl+"'",
	)
	p := procedure{path: "/sermon.web.v1.KioskService/QueryKioskWidget"}
	if code := rawCall(t, s, p, kioskHeader(kioskSecretAl), `{"widgetId":"cpu"}`); code != "not_found" {
		t.Errorf("a node of another organization: %q, want not_found", code)
	}
	if code := rawCall(t, s, p, kioskHeader(kioskSecretAl), `{"widgetId":"nope"}`); code != "not_found" {
		t.Errorf("an unknown widget: %q, want not_found", code)
	}
}

// Ann creates a token in the UI's way; the display reads its dashboard; revoking ends it at once.
// Every token has a limit of calls per minute.
func TestKiosk_CreateUseRevokeAndRateLimit(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ann := login(t, s, "ann@example.org")
	ctx := context.Background()
	created, err := ann.kioskTokens.CreateKioskToken(ctx, connect.NewRequest(&webv1.CreateKioskTokenRequest{
		DashboardId: dashAl, Name: "Hall 2", ValidFor: durationpb.New(48 * time.Hour),
	}))
	if err != nil {
		t.Fatal(err)
	}
	secret := created.Msg.GetSecret()
	if !strings.HasPrefix(secret, "smk_") || strings.Contains(created.Msg.GetToken().String(), secret) {
		t.Fatalf("created = %v", created.Msg)
	}
	var stored int
	ownerQueryRow(t, "SELECT count(*) FROM kiosk_tokens WHERE token_hash = sha256(convert_to('"+secret+"', 'UTF8'))", &stored)
	if stored != 1 {
		t.Error("the token must be stored as its SHA-256")
	}
	var plain int
	ownerQueryRow(t, "SELECT count(*) FROM audit_log WHERE details::text LIKE '%"+secret[4:20]+"%'", &plain)
	if plain != 0 {
		t.Error("the secret must not be in the audit log")
	}
	get := procedure{path: "/sermon.web.v1.KioskService/GetKioskDashboard"}
	if code := rawCall(t, s, get, kioskHeader(secret), "{}"); code != "" {
		t.Fatalf("the new token: %q", code)
	}
	list, err := ann.kioskTokens.ListKioskTokens(ctx, connect.NewRequest(&webv1.ListKioskTokensRequest{DashboardId: dashAl}))
	if err != nil || len(list.Msg.GetTokens()) < 2 {
		t.Fatalf("list = %v, %v", list, err)
	}
	for i := 1; i < kioskTestCalls; i++ {
		if code := rawCall(t, s, get, kioskHeader(secret), "{}"); code != "" {
			t.Fatalf("call %d: %q", i+1, code)
		}
	}
	if code := rawCall(t, s, get, kioskHeader(secret), "{}"); code != "resource_exhausted" {
		t.Errorf("beyond the limit: %q, want resource_exhausted", code)
	}
	if code := rawCall(t, s, get, kioskHeader(kioskSecretAl), "{}"); code != "" {
		t.Errorf("another token keeps its own limit: %q", code)
	}
	if _, err := ann.kioskTokens.RevokeKioskToken(ctx, connect.NewRequest(&webv1.RevokeKioskTokenRequest{TokenId: created.Msg.GetToken().GetId()})); err != nil {
		t.Fatal(err)
	}
	if code := rawCall(t, s, get, kioskHeader(secret), "{}"); code != "unauthenticated" && code != "resource_exhausted" {
		t.Errorf("revoked: %q", code)
	}
	var audits []string
	ownerQueryRow(t, "SELECT array_agg(action ORDER BY seq) FROM audit_log WHERE target = 'kiosk_token:"+created.Msg.GetToken().GetId()+"'", &audits)
	if strings.Join(audits, ",") != "kiosk_token.create,kiosk_token.revoke" {
		t.Errorf("audit = %v", audits)
	}

	// A private dashboard gets no token.
	ownerSQL(t, "UPDATE dashboards SET shared = false WHERE id = '"+dashAl+"'")
	_, err = ann.kioskTokens.CreateKioskToken(ctx, connect.NewRequest(&webv1.CreateKioskTokenRequest{
		DashboardId: dashAl, Name: "Hall 3", ValidFor: durationpb.New(48 * time.Hour),
	}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("private dashboard: %v", err)
	}
	// A viewer manages no tokens.
	mia := login(t, s, "mia@example.org")
	if _, err := mia.kioskTokens.ListKioskTokens(ctx, connect.NewRequest(&webv1.ListKioskTokensRequest{DashboardId: dashAl})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a viewer lists tokens: %v", err)
	}
}
