package email_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/adapters/notify/email"
	"github.com/pmoscode/SerMon/server/internal/adapters/smtp"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/domain/alert"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var t0 = time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)

type fakeSender struct {
	sent []smtp.Message
	errs []error
}

func (f *fakeSender) Send(_ context.Context, m smtp.Message) error {
	f.sent = append(f.sent, m)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return err
	}
	return nil
}

func security(lang notify.Lang) notify.Message {
	return notify.Render(notify.Notification{
		Kind: notify.Firing, Severity: notify.Warning, Rule: "security.ssh.bruteforce",
		RuleName: notify.Text{De: "SSH-Brute-Force", En: "SSH brute force"},
		Node:     identity.NodeID(uuid.MustParse("0193f5a0-0000-7000-8000-000000000001")), Hostname: "web-01",
		Dimension: "ip=192.0.2.7\x1f", Value: 12, Unit: notify.UnitCount, At: t0,
		Fields: alert.Fields{"user": `<script>alert("x")</script><a href="javascript:evil()">`, "ip": "192.0.2.7"},
	}, lang, "https://sermon.example")
}

func config(to string) notify.Config {
	return notify.Config{Settings: map[string]string{"to": to}}
}

func TestEmail_SendsTextAndHTMLToEachRecipient(t *testing.T) {
	f := &fakeSender{}
	if err := email.New(f).Send(context.Background(), config("ops@example.org, Bea@Example.org"), security(notify.German)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || f.sent[0].To != "ops@example.org" || f.sent[1].To != "Bea@Example.org" {
		t.Fatalf("sent to %+v", f.sent)
	}
	m := f.sent[0]
	if m.Subject != "[SerMon] WARNUNG: SSH-Brute-Force – web-01" {
		t.Errorf("subject %q", m.Subject)
	}
	// The hostile user name stays text: no script, and the only link is SerMon's.
	if strings.Contains(m.HTML, "<script") || strings.Count(m.HTML, "<a ") != 1 || !strings.Contains(m.HTML, "&lt;script&gt;") {
		t.Errorf("the HTML lets the user name through as markup:\n%s", m.HTML)
	}
	golden(t, "security.de.txt", m.Text)
	golden(t, "security.de.html", m.HTML)
	f.sent = nil
	if err := email.New(f).Send(context.Background(), config("ops@example.org"), security(notify.English)); err != nil {
		t.Fatal(err)
	}
	golden(t, "security.en.html", f.sent[0].HTML)
}

func TestEmail_ClassifiesMailServerErrors(t *testing.T) {
	for _, tc := range []struct {
		err       error
		permanent bool
	}{
		{&textproto.Error{Code: 550, Msg: "mailbox unavailable"}, true},
		{&textproto.Error{Code: 451, Msg: "try again later"}, false},
		{smtp.ErrInvalidMessage, true},
		{errors.New("connection refused"), false},
	} {
		f := &fakeSender{errs: []error{tc.err}}
		err := email.New(f).Send(context.Background(), config("ops@example.org"), security(notify.English))
		if err == nil || notify.IsPermanent(err) != tc.permanent {
			t.Errorf("%v: %v, permanent %v", tc.err, err, notify.IsPermanent(err))
		}
	}
}

func TestEmail_ValidatesRecipients(t *testing.T) {
	n := email.New(&fakeSender{})
	for _, to := range []string{
		"", "not-an-address", "Bea <bea@example.org>", "bea@example.org\r\nBcc: eve@example.org", "a@example.org,,b@example.org",
		"a@example.org, a@example.org", eleven(),
	} {
		if err := n.Validate(config(to)); err == nil {
			t.Errorf("recipients %q accepted", to)
		}
	}
	if err := n.Validate(config("ops@example.org, bea@example.org")); err != nil {
		t.Errorf("valid recipients: %v", err)
	}
	if fields := n.Fields(); n.Type() != "email" || len(fields) != 1 || fields[0].Name != "to" || fields[0].Secret || !fields[0].Required {
		t.Errorf("fields %+v", fields)
	}
}

// eleven is one address more than allowed, all different.
func eleven() string {
	list := make([]string, email.MaxRecipients+1)
	for i := range list {
		list[i] = fmt.Sprintf("ops%d@example.org", i)
	}
	return strings.Join(list, ", ")
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}
