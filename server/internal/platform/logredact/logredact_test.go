package logredact_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"
	webv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/web/v1"
	"github.com/pmoscode/SerMon/server/internal/platform/logredact"
)

// M5-33: protobuf-go ignores debug_redact in String() and json.Marshal shows every exported
// field: a message with a credential in a log call would print it. The server's handlers drop
// such fields from every message logged, also nested in another one, in a list or a group.
func TestReplaceAttr_DropsRedactedFieldsOfMessages(t *testing.T) {
	const value = "pg-value-in-a-log" // betterleaks:allow
	secrets := &agentv1.ModuleSecrets{Secrets: []*agentv1.ModuleSecret{{ModuleId: "postgres", Field: "password", Value: value}}}
	wrapped := &agentv1.SessionResponse{Payload: &agentv1.SessionResponse_ModuleSecrets{ModuleSecrets: secrets}}
	set := &webv1.SetGroupModuleRequest{Setting: &webv1.ModuleSetting{ModuleId: "redis"}, Secrets: map[string]string{"password": value}}
	for name, handler := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler {
			return slog.NewJSONHandler(b, &slog.HandlerOptions{ReplaceAttr: logredact.ReplaceAttr})
		},
		"text": func(b *bytes.Buffer) slog.Handler {
			return slog.NewTextHandler(b, &slog.HandlerOptions{ReplaceAttr: logredact.ReplaceAttr})
		},
	} {
		var out bytes.Buffer
		logger := slog.New(handler(&out))
		logger.Info("sent", "msg", secrets, "wrapped", wrapped, slog.Group("g", "set", set))
		logger.With("pre", secrets).Info("again")
		text := out.String()
		if strings.Contains(text, value) {
			t.Errorf("%s: plaintext in %s", name, text)
		}
		if !strings.Contains(text, "postgres") || !strings.Contains(text, "redis") {
			t.Errorf("%s: the rest is gone: %s", name, text)
		}
	}
	if secrets.GetSecrets()[0].GetValue() != value || set.GetSecrets()["password"] != value {
		t.Error("the logged message changed")
	}
	var out bytes.Buffer
	slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{ReplaceAttr: logredact.ReplaceAttr})).Info("x", "n", 3, "s", "plain")
	if !strings.Contains(out.String(), "n=3 s=plain") {
		t.Errorf("other values changed: %s", out.String())
	}
}
