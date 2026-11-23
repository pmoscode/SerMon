package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/authz"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
)

type fixture struct {
	store    *memStore
	notifier *fakeNotifier
	clock    *clock
	logs     *logs
	deps     notify.Deps
	svc      *notify.Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{store: newMemStore(), notifier: &fakeNotifier{}, clock: &clock{now: t0}, logs: &logs{}}
	registry, err := notify.NewRegistry(f.notifier)
	if err != nil {
		t.Fatal(err)
	}
	f.deps = notify.Deps{
		Store: f.store, Registry: registry, Box: box(t), Policy: authz.NewPolicy(), BaseURL: base,
		Now: f.clock.Now, NewID: uuid.New, Logger: f.logs.logger(),
	}
	f.svc = notify.NewService(f.deps)
	return f
}

func newChannel() notify.NewChannel {
	return notify.NewChannel{
		Type: "fake", Name: "Ops", Lang: notify.German, Enabled: true,
		Settings: map[string]string{"url": "https://hooks.example/ops", "priority": "high"},
		Secrets:  map[string]string{"token": secretToken},
	}
}

func (f *fixture) create(t *testing.T) notify.Channel {
	t.Helper()
	c, err := f.svc.Create(context.Background(), admin(orgA, t0), newChannel())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCreate_SealsTheSecretsAndAudits(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	if c.Name != "Ops" || c.Type != "fake" || c.Lang != notify.German || !c.Enabled || c.Settings["url"] != "https://hooks.example/ops" ||
		!slices.Equal(c.SecretNames, []string{"token"}) || c.CreatedBy != ann || !c.CreatedAt.Equal(t0) {
		t.Errorf("channel = %+v", c)
	}
	stored, err := f.store.Channel(context.Background(), scope(orgA), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored.SealedSecrets), secretToken) || len(stored.SealedSecrets) == 0 {
		t.Error("secrets stored in clear")
	}
	if _, ok := stored.Settings["token"]; ok {
		t.Error("secret among the settings")
	}
	// Bound to organization and channel: the ciphertext does not open elsewhere.
	if string(notify.SecretContext(orgA, c.ID)) == string(notify.SecretContext(orgB, c.ID)) {
		t.Error("the secret context does not name the organization")
	}
	if _, err := f.deps.Box.Open(stored.SealedSecrets, notify.SecretContext(orgB, c.ID)); err == nil {
		t.Error("secrets open in the context of another organization")
	}
	if _, err := f.deps.Box.Open(stored.SealedSecrets, notify.SecretContext(orgA, c.ID)); err != nil {
		t.Errorf("secrets don't open in their context: %v", err)
	}
	a := f.store.audits[0]
	if a.Action != "notification_channel.create" || a.Target != "notification_channel:"+c.ID.String() || a.Actor != ann {
		t.Errorf("audit = %+v", a)
	}
	for k, v := range a.Details {
		if strings.Contains(v, secretToken) || strings.Contains(v, "hooks.example") {
			t.Errorf("audit detail %s = %q reveals the target or a secret", k, v)
		}
	}
}

func TestChannels_RightsAndStepUp(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.create(t)
	stale := t0.Add(-access.StepUpMaxAge - time.Minute)
	if _, err := f.svc.Create(ctx, operator(orgA, t0), newChannel()); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator creates: %v", err)
	}
	if _, err := f.svc.List(ctx, operator(orgA, t0)); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator lists: %v", err)
	}
	if _, err := f.svc.Create(ctx, admin(orgA, stale), newChannel()); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("create without step-up: %v", err)
	}
	name := "Renamed"
	if _, err := f.svc.Update(ctx, admin(orgA, stale), c.ID, notify.ChannelChange{Name: &name}); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("update without step-up: %v", err)
	}
	if err := f.svc.Delete(ctx, admin(orgA, stale), c.ID); !errors.Is(err, access.ErrStepUpRequired) {
		t.Errorf("delete without step-up: %v", err)
	}
	// Another organization's admin sees nothing and changes nothing.
	if list, err := f.svc.List(ctx, admin(orgB, t0)); err != nil || len(list) != 0 {
		t.Errorf("org B lists %v, %v", list, err)
	}
	for name, err := range map[string]error{
		"update": func() error {
			_, err := f.svc.Update(ctx, admin(orgB, t0), c.ID, notify.ChannelChange{Name: &name})
			return err
		}(),
		"delete": f.svc.Delete(ctx, admin(orgB, t0), c.ID),
		"test":   func() error { _, err := f.svc.Test(ctx, admin(orgB, t0), c.ID); return err }(),
		"deliveries": func() error {
			_, err := f.svc.Deliveries(ctx, admin(orgB, t0), notify.DeliveryFilter{Channel: &c.ID})
			if err == nil {
				err = access.ErrNotFound // empty is fine as well
			}
			return err
		}(),
	} {
		if !errors.Is(err, access.ErrNotFound) {
			t.Errorf("org B %s: %v, want not found", name, err)
		}
	}
}

func TestCreate_ValidatesTheConfiguration(t *testing.T) {
	for name, change := range map[string]func(*notify.NewChannel){
		"unknown type":         func(n *notify.NewChannel) { n.Type = "pager" },
		"no name":              func(n *notify.NewChannel) { n.Name = " " },
		"long name":            func(n *notify.NewChannel) { n.Name = strings.Repeat("x", 101) },
		"control in name":      func(n *notify.NewChannel) { n.Name = "Ops\nX" },
		"bad language":         func(n *notify.NewChannel) { n.Lang = "fr" },
		"missing required":     func(n *notify.NewChannel) { delete(n.Settings, "url") },
		"missing secret":       func(n *notify.NewChannel) { n.Secrets = nil },
		"unknown setting":      func(n *notify.NewChannel) { n.Settings["proxy"] = "x" },
		"secret as setting":    func(n *notify.NewChannel) { n.Settings["token"] = "x" },
		"setting as secret":    func(n *notify.NewChannel) { n.Secrets["url"] = "https://hooks.example/" },
		"metadata url":         func(n *notify.NewChannel) { n.Settings["url"] = "http://169.254.169.254/latest/" },
		"not a url":            func(n *notify.NewChannel) { n.Settings["url"] = "hooks.example/ops" },
		"credentials in url":   func(n *notify.NewChannel) { n.Settings["url"] = "https://u:p@hooks.example/" },
		"bad choice":           func(n *notify.NewChannel) { n.Settings["priority"] = "urgent" },
		"long value":           func(n *notify.NewChannel) { n.Secrets["token"] = strings.Repeat("x", 4097) },
		"control in value":     func(n *notify.NewChannel) { n.Secrets["token"] = "a\nb" },
		"empty optional value": func(n *notify.NewChannel) { n.Settings["priority"] = "" },
	} {
		f := newFixture(t)
		n := newChannel()
		change(&n)
		_, err := f.svc.Create(context.Background(), admin(orgA, t0), n)
		want := notify.ErrInvalidChannel
		if name == "unknown type" {
			want = notify.ErrUnknownType
		}
		if !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
		if err != nil && strings.Contains(err.Error(), secretToken) {
			t.Errorf("%s: the error %q reveals the secret", name, err)
		}
	}
	// The channel type checks what only it knows.
	f := newFixture(t)
	f.notifier.reject = errors.New("token must start with tok-")
	if _, err := f.svc.Create(context.Background(), admin(orgA, t0), newChannel()); !errors.Is(err, notify.ErrInvalidChannel) || !strings.Contains(err.Error(), "tok-") {
		t.Errorf("type's own check: %v", err)
	}
}

// Allowlisted internal targets are fine (an internal Mattermost).
func TestCreate_AcceptsAllowlistedInternalTargets(t *testing.T) {
	f := newFixture(t)
	allow, err := safehttp.ParseAllowlist("10.20.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	p, err := safehttp.NewPolicy(allow)
	if err != nil {
		t.Fatal(err)
	}
	f.deps.Outbound = p
	svc := notify.NewService(f.deps)
	n := newChannel()
	n.Settings["url"] = "http://10.20.0.5:8065/hooks/x"
	if _, err := svc.Create(context.Background(), admin(orgA, t0), n); err != nil {
		t.Errorf("allowlisted target: %v", err)
	}
}

// An organization has at most MaxChannels channels (M2-38): each can mail or call out on its
// behalf through the operator's server.
func TestCreate_BoundsTheChannels(t *testing.T) {
	f := newFixture(t)
	for i := range notify.MaxChannels {
		nc := newChannel()
		nc.Name = fmt.Sprintf("Ops %d", i)
		if _, err := f.svc.Create(context.Background(), admin(orgA, t0), nc); err != nil {
			t.Fatalf("channel %d: %v", i, err)
		}
	}
	nc := newChannel()
	nc.Name = "one too many"
	if _, err := f.svc.Create(context.Background(), admin(orgA, t0), nc); !errors.Is(err, notify.ErrTooManyChannels) {
		t.Errorf("channel %d: %v", notify.MaxChannels+1, err)
	}
	if _, err := f.svc.Create(context.Background(), admin(orgB, t0), nc); err != nil {
		t.Errorf("another organization: %v", err)
	}
}

// A field that became secret (the webhook URL, M2-39) may still sit among the settings of an
// older channel: it is sent as a secret, never shown, and sealed with the next change.
func TestLegacy_SecretFieldsAmongTheSettings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.create(t)
	stored, err := f.store.Channel(ctx, scope(orgA), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Settings["token"] = "tok-legacy"
	plain, _ := json.Marshal(map[string]string{})
	if stored.SealedSecrets, err = f.deps.Box.Seal(plain, notify.SecretContext(orgA, c.ID)); err != nil {
		t.Fatal(err)
	}
	stored.SecretNames = nil
	f.store.channels[orgA][c.ID] = stored

	list, err := f.svc.List(ctx, admin(orgA, t0))
	if err != nil || len(list) != 1 || list[0].Settings["token"] != "" || !slices.Contains(list[0].SecretNames, "token") {
		t.Errorf("listed %+v, %v", list, err)
	}
	d := f.dispatcher()
	f.enqueue(t, d, c.ID)
	d.Round(ctx)
	msgs := f.notifier.messages()
	if len(msgs) != 1 || msgs[0].config.Secrets["token"] != "tok-legacy" || msgs[0].config.Settings["token"] != "" {
		t.Fatalf("sent %+v", msgs)
	}
	if msgs[0].message.ID == "" {
		t.Error("no delivery id")
	}
	name := "Ops renamed"
	if _, err := f.svc.Update(ctx, admin(orgA, t0), c.ID, notify.ChannelChange{Name: &name}); err != nil {
		t.Fatal(err)
	}
	stored, _ = f.store.Channel(ctx, scope(orgA), c.ID)
	open, err := notify.OpenSecrets(f.deps.Box, orgA, stored)
	if err != nil || stored.Settings["token"] != "" || open["token"] != "tok-legacy" {
		t.Errorf("after the change: settings %v, secrets %v, %v", stored.Settings, open, err)
	}
}

func TestUpdate_KeepsSecretsThatAreNotSent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.create(t)
	name, lang, off := "Ops night", notify.English, false
	f.clock.advance(time.Minute)
	// A new address with the old secrets would hand them to whoever runs it (M2-28): the secrets
	// have to be entered again.
	if _, err := f.svc.Update(ctx, admin(orgA, f.clock.Now()), c.ID, notify.ChannelChange{
		Settings: map[string]string{"url": "https://attacker.example/x", "priority": "high"},
	}); !errors.Is(err, notify.ErrInvalidChannel) {
		t.Errorf("new address without the secrets: %v", err)
	}
	got, err := f.svc.Update(ctx, admin(orgA, f.clock.Now()), c.ID, notify.ChannelChange{
		Name: &name, Lang: &lang, Enabled: &off, Settings: map[string]string{"url": "https://hooks.example/night"},
		Secrets: map[string]string{"token": secretToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.Lang != lang || got.Enabled || got.Settings["url"] != "https://hooks.example/night" ||
		got.Settings["priority"] != "" || !got.UpdatedAt.Equal(t0.Add(time.Minute)) || !got.CreatedAt.Equal(t0) {
		t.Errorf("updated = %+v", got)
	}
	secretsOf := func() map[string]string {
		t.Helper()
		stored, err := f.store.Channel(ctx, scope(orgA), c.ID)
		if err != nil {
			t.Fatal(err)
		}
		open, err := notify.OpenSecrets(f.deps.Box, orgA, stored)
		if err != nil {
			t.Fatal(err)
		}
		return open
	}
	if s := secretsOf(); s["token"] != secretToken {
		t.Errorf("secret lost: %v", s)
	}
	// Other settings keep the secrets.
	if _, err := f.svc.Update(ctx, admin(orgA, f.clock.Now()), c.ID, notify.ChannelChange{
		Settings: map[string]string{"url": "https://hooks.example/night", "priority": "low"},
	}); err != nil {
		t.Fatal(err)
	}
	if s := secretsOf(); s["token"] != secretToken {
		t.Errorf("secret lost with another setting: %v", s)
	}
	if _, err := f.svc.Update(ctx, admin(orgA, f.clock.Now()), c.ID, notify.ChannelChange{Secrets: map[string]string{"token": "tok-new"}}); err != nil {
		t.Fatal(err)
	}
	if s := secretsOf(); s["token"] != "tok-new" {
		t.Errorf("secret not replaced: %v", s)
	}
	a := f.store.audits[len(f.store.audits)-1]
	if a.Action != "notification_channel.update" || a.Details["changed"] != "secrets" {
		t.Errorf("audit = %+v", a)
	}
	// Changes are validated like a new channel.
	bad := map[string]string{"url": "http://127.0.0.1/"}
	if _, err := f.svc.Update(ctx, admin(orgA, f.clock.Now()), c.ID, notify.ChannelChange{Settings: bad}); !errors.Is(err, notify.ErrInvalidChannel) {
		t.Errorf("loopback target: %v", err)
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	if err := f.svc.Delete(context.Background(), admin(orgA, t0), c.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.svc.List(context.Background(), admin(orgA, t0)); len(list) != 0 {
		t.Errorf("channels left: %+v", list)
	}
	if a := f.store.audits[len(f.store.audits)-1]; a.Action != "notification_channel.delete" {
		t.Errorf("audit = %+v", a)
	}
}

func TestTest_SendsAtOnceAndRecordsTheDelivery(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.create(t)
	d, err := f.svc.Test(ctx, admin(orgA, t0), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != notify.StatusSent || !d.Test || d.Attempts != 1 || d.Channel != c.ID {
		t.Errorf("delivery = %+v", d)
	}
	msgs := f.notifier.messages()
	if len(msgs) != 1 || msgs[0].message.Kind != notify.Test || msgs[0].message.Lang != notify.German ||
		msgs[0].config.Secrets["token"] != secretToken || msgs[0].config.Settings["url"] != "https://hooks.example/ops" {
		t.Fatalf("sent %+v", msgs)
	}
	// A failure is shown, without the secret, and not retried.
	f.notifier.errs = []error{errors.New("HTTP 401: invalid token " + secretToken)}
	d, err = f.svc.Test(ctx, admin(orgA, t0), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != notify.StatusFailed || !strings.Contains(d.LastError, "HTTP 401") || strings.Contains(d.LastError, secretToken) {
		t.Errorf("failed test = %+v", d)
	}
	if list, _ := f.svc.Deliveries(ctx, admin(orgA, t0), notify.DeliveryFilter{Channel: &c.ID, Limit: 10}); len(list) != 2 || list[0].Status != notify.StatusFailed {
		t.Errorf("deliveries = %+v", list)
	}
	if strings.Contains(f.logs.buf.String(), secretToken) {
		t.Error("the secret was logged")
	}
}

// "Send test" sends to a target the admin chose: limited per organization.
func TestTest_IsRateLimited(t *testing.T) {
	f := newFixture(t)
	c := f.create(t)
	var err error
	for range notify.TestsPerMinute + 1 {
		_, err = f.svc.Test(context.Background(), admin(orgA, t0), c.ID)
	}
	if !errors.Is(err, notify.ErrTooManyTests) {
		t.Errorf("after %d tests: %v", notify.TestsPerMinute+1, err)
	}
	f.clock.advance(time.Minute)
	if _, err := f.svc.Test(context.Background(), admin(orgA, f.clock.Now()), c.ID); err != nil {
		t.Errorf("a minute later: %v", err)
	}
}

func TestTypes_DescribeTheForm(t *testing.T) {
	f := newFixture(t)
	types, err := f.svc.Types(admin(orgA, t0))
	if err != nil || len(types) != 1 || types[0].Type != "fake" || len(types[0].Fields) != 3 || !types[0].Fields[1].Secret {
		t.Errorf("Types() = %+v, %v", types, err)
	}
	if _, err := f.svc.Types(operator(orgA, t0)); !errors.Is(err, access.ErrForbidden) {
		t.Errorf("operator: %v", err)
	}
}

func TestNewRegistry_RejectsBrokenTypes(t *testing.T) {
	if _, err := notify.NewRegistry(&fakeNotifier{}, &fakeNotifier{}); err == nil {
		t.Error("two notifiers of one type")
	}
	if _, err := notify.NewRegistry(&badFields{}); err == nil {
		t.Error("a type with a duplicate field")
	}
}

type badFields struct{ fakeNotifier }

func (*badFields) Type() notify.ChannelType { return "bad" }
func (*badFields) Fields() []notify.Field {
	return []notify.Field{{Name: "url", Kind: notify.FieldURL}, {Name: "url", Kind: notify.FieldText}}
}
