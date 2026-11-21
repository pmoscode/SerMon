//go:build integration

package webapi_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/pmoscode/SerMon/server/internal/app/notify"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1/webv1connect"
)

// channelAl is a notification channel of Alpha (isolation fixtures).
const channelAl = "0193f5a0-0000-7000-8000-0000000001a4"

// policyAl is a notification policy of Alpha to channelAl (isolation fixtures).
const policyAl = "0193f5a0-0000-7000-8000-0000000001a5"

const hookToken = "hook-secret-token-0815" // fixture, not a secret

// hookNotifier is the channel type of the API tests: a URL and a secret token; it records sends.
type hookNotifier struct {
	mu   sync.Mutex
	sent []notify.Config
}

var hooks = &hookNotifier{}

func (*hookNotifier) Type() notify.ChannelType { return "hook" }

func (*hookNotifier) Fields() []notify.Field {
	return []notify.Field{{Name: "url", Kind: notify.FieldURL, Required: true}, {Name: "token", Kind: notify.FieldText, Secret: true, Required: true}}
}

func (*hookNotifier) Validate(notify.Config) error { return nil }

func (h *hookNotifier) Send(_ context.Context, c notify.Config, _ notify.Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, c)
	return nil
}

func (h *hookNotifier) last() notify.Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.sent) == 0 {
		return notify.Config{}
	}
	return h.sent[len(h.sent)-1]
}

func hookRegistry(t *testing.T) *notify.Registry {
	t.Helper()
	r, err := notify.NewRegistry(hooks)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func seedChannelAlpha(t *testing.T) {
	t.Helper()
	ownerSQL(t,
		"SELECT set_config('sermon.org_id', '"+orgAlpha+"', false)",
		"DELETE FROM notification_policies WHERE org_id = '"+orgAlpha+"'",
		"DELETE FROM notification_channels WHERE org_id = '"+orgAlpha+"'",
		"DELETE FROM notification_deliveries WHERE org_id = '"+orgAlpha+"'",
		`INSERT INTO notification_channels (org_id, id, type, name, language, enabled, settings, secret_names, sealed_secrets, created_at, updated_at)
		 VALUES ('`+orgAlpha+`', '`+channelAl+`', 'hook', 'alpha-hook', 'de', true, '{"url": "https://alpha.example/hook"}', '{token}', '\x00', now(), now())`,
		`INSERT INTO notification_deliveries (org_id, channel_id, notification, status, created_at, next_attempt_at, last_error)
		 VALUES ('`+orgAlpha+`', '`+channelAl+`', '{"kind": "firing", "hostname": "alpha-01"}', 'failed', now(), now(), 'alpha-error')`,
		`INSERT INTO notification_policies (org_id, id, name, enabled, grouping, created_at, updated_at)
		 VALUES ('`+orgAlpha+`', '`+policyAl+`', 'alpha-policy', true, 'node', now(), now())`,
		`INSERT INTO notification_policy_channels (org_id, policy_id, channel_id, escalation, position)
		 VALUES ('`+orgAlpha+`', '`+policyAl+`', '`+channelAl+`', false, 0)`,
	)
}

func channelClient(t *testing.T, s stack, l loggedIn) webv1connect.NotificationChannelServiceClient {
	t.Helper()
	return webv1connect.NewNotificationChannelServiceClient(s.httpClient(l.jar, s.origin), s.server.URL)
}

func TestNotificationAPI_AdminManagesChannelsWithoutSeeingSecrets(t *testing.T) {
	s := newStack(t, 1000)
	seedAccessFixtures(t)
	ownerSQL(t, "DELETE FROM notification_channels WHERE org_id = '"+orgAlpha+"'")
	ann := login(t, s, "ann@example.org")
	c := channelClient(t, s, ann)
	ctx := context.Background()

	types, err := c.ListChannelTypes(ctx, connect.NewRequest(&webv1.ListChannelTypesRequest{}))
	if err != nil || len(types.Msg.GetTypes()) != 1 || !types.Msg.GetTypes()[0].GetFields()[1].GetSecret() {
		t.Fatalf("ListChannelTypes() = %v, %v", types, err)
	}
	created, err := c.CreateChannel(ctx, connect.NewRequest(&webv1.CreateChannelRequest{
		Type: "hook", Name: "Ops", Language: "en", Enabled: true,
		Settings: map[string]string{"url": "https://hooks.example/ops"}, Secrets: map[string]string{"token": hookToken},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ch := created.Msg.GetChannel()
	if ch.GetName() != "Ops" || ch.GetSettings()["url"] != "https://hooks.example/ops" || len(ch.GetSecretNames()) != 1 {
		t.Errorf("created %v", ch)
	}
	list, err := c.ListChannels(ctx, connect.NewRequest(&webv1.ListChannelsRequest{}))
	if err != nil || len(list.Msg.GetChannels()) != 1 {
		t.Fatalf("ListChannels() = %v, %v", list, err)
	}
	renamed := "Ops night"
	updated, err := c.UpdateChannel(ctx, connect.NewRequest(&webv1.UpdateChannelRequest{ChannelId: ch.GetId(), Name: &renamed}))
	if err != nil || updated.Msg.GetChannel().GetName() != renamed {
		t.Fatalf("UpdateChannel() = %v, %v", updated, err)
	}
	for name, msg := range map[string]string{"create": created.Msg.String(), "list": list.Msg.String(), "update": updated.Msg.String()} {
		if strings.Contains(msg, hookToken) {
			t.Errorf("%s returns the secret", name)
		}
	}

	test, err := c.TestChannel(ctx, connect.NewRequest(&webv1.TestChannelRequest{ChannelId: ch.GetId()}))
	if err != nil || test.Msg.GetDelivery().GetStatus() != webv1.DeliveryStatus_DELIVERY_STATUS_SENT || !test.Msg.GetDelivery().GetTest() {
		t.Fatalf("TestChannel() = %v, %v", test, err)
	}
	if got := hooks.last(); got.Secrets["token"] != hookToken || got.Settings["url"] != "https://hooks.example/ops" {
		t.Errorf("the channel got %+v; want the secret kept over the update", got)
	}
	log, err := c.ListDeliveries(ctx, connect.NewRequest(&webv1.ListDeliveriesRequest{ChannelId: ch.GetId()}))
	if err != nil || len(log.Msg.GetDeliveries()) != 1 || log.Msg.GetDeliveries()[0].GetKind() != "test" {
		t.Errorf("ListDeliveries() = %v, %v", log, err)
	}

	// Invalid targets are refused, naming the field, not the value.
	_, err = c.CreateChannel(ctx, connect.NewRequest(&webv1.CreateChannelRequest{
		Type: "hook", Name: "Bad", Language: "en", Settings: map[string]string{"url": "http://169.254.169.254/latest/"}, Secrets: map[string]string{"token": hookToken},
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || strings.Contains(err.Error(), hookToken) || !strings.Contains(err.Error(), "url") {
		t.Errorf("metadata target: %v", err)
	}

	// A viewer may not, and an old login must step up first.
	mia := channelClient(t, s, login(t, s, "mia@example.org"))
	if _, err := mia.ListChannels(ctx, connect.NewRequest(&webv1.ListChannelsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("viewer lists channels: %v", err)
	}
	ownerSQL(t, "UPDATE sessions SET step_up_at = now() - interval '1 hour' WHERE user_id = '"+annID+"'")
	if _, err := c.DeleteChannel(ctx, connect.NewRequest(&webv1.DeleteChannelRequest{ChannelId: ch.GetId()})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("delete with an old login: %v, want FAILED_PRECONDITION", err)
	}
	ann = login(t, s, "ann@example.org")
	if _, err := channelClient(t, s, ann).DeleteChannel(ctx, connect.NewRequest(&webv1.DeleteChannelRequest{ChannelId: ch.GetId()})); err != nil {
		t.Errorf("delete: %v", err)
	}
}
