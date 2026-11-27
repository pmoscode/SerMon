package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/postgres"
	"github.com/pmoscode/SerMon/server/internal/app/access"
	"github.com/pmoscode/SerMon/server/internal/app/anomalies"
	"github.com/pmoscode/SerMon/server/internal/app/auth/session"
	"github.com/pmoscode/SerMon/server/internal/app/invitation"
	"github.com/pmoscode/SerMon/server/internal/app/kiosk"
	"github.com/pmoscode/SerMon/server/internal/app/liveview"
	"github.com/pmoscode/SerMon/server/internal/app/nodelife"
	"github.com/pmoscode/SerMon/server/internal/app/notify"
	"github.com/pmoscode/SerMon/server/internal/app/organization"
	"github.com/pmoscode/SerMon/server/internal/domain/retention"
)

// V1-11: the GDPR template (docs/privacy) names the periods operators document; each is taken
// from the code here, so changing a period fails until the template says the new one.
func TestPrivacyTemplate_PeriodsMatchTheCode(t *testing.T) {
	days := func(d time.Duration) int { return int(d / (24 * time.Hour)) }
	hours := func(d time.Duration) int { return int(d / time.Hour) }
	minutes := func(d time.Duration) int { return int(d / time.Minute) }
	facts := []struct{ de, en string }{
		{
			fmt.Sprintf("Logs | %d–%d Tage, ab Werk %d", retention.MinLogDays, retention.MaxLogDays, retention.MaxLogDays),
			fmt.Sprintf("Logs | %d–%d days, default %d", retention.MinLogDays, retention.MaxLogDays, retention.MaxLogDays),
		},
		{
			fmt.Sprintf("Verlauf der Alerts | %d–%d Tage, ab Werk %d", retention.MinEventDays, retention.MaxEventDays, retention.MaxEventDays),
			fmt.Sprintf("alert history | %d–%d days, default %d", retention.MinEventDays, retention.MaxEventDays, retention.MaxEventDays),
		},
		{
			fmt.Sprintf("Sitzungen | %d Minuten ohne Aktivität, höchstens %d Stunden", minutes(session.IdleTimeout), hours(session.AbsoluteTimeout)),
			fmt.Sprintf("Sessions | %d minutes without activity, at most %d hours", minutes(session.IdleTimeout), hours(session.AbsoluteTimeout)),
		},
		{
			fmt.Sprintf("Bestätigung (Step-up), die %d Minuten gilt", minutes(access.StepUpMaxAge)),
			fmt.Sprintf("confirmation (step-up) that holds for %d minutes", minutes(access.StepUpMaxAge)),
		},
		{
			fmt.Sprintf("Einladungen | %d Stunden gültig", hours(invitation.Lifetime)),
			fmt.Sprintf("Invitations | valid for %d hours", hours(invitation.Lifetime)),
		},
		{
			fmt.Sprintf("Zustellprotokoll der Benachrichtigungen | %d Tage", days(notify.Retention)),
			fmt.Sprintf("Delivery log of notifications | %d days", days(notify.Retention)),
		},
		{
			fmt.Sprintf("Anomalie-Werte | %d Tage", days(anomalies.Retention)),
			fmt.Sprintf("Anomaly values | %d days", days(anomalies.Retention)),
		},
		{
			fmt.Sprintf("Bekannte Anmeldequellen | %d Jahr nach der letzten Anmeldung", days(postgres.ForgetSourcesAfter)/365),
			fmt.Sprintf("Known login sources | %d year after the last sign-in", days(postgres.ForgetSourcesAfter)/365),
		},
		{
			fmt.Sprintf("Frist (ab Werk %d Tage, mindestens %d Stunden, `SERMON_NODE_DELETION_GRACE`)", days(nodelife.DefaultDeletionGrace), hours(nodelife.MinDeletionGrace)),
			fmt.Sprintf("(default %d days, at least %d hours, `SERMON_NODE_DELETION_GRACE`)", days(nodelife.DefaultDeletionGrace), hours(nodelife.MinDeletionGrace)),
		},
		{
			fmt.Sprintf("Frist (ab Werk %d Tage, mindestens %d Tage, `SERMON_ORG_DELETION_GRACE`)", days(organization.DefaultDeletionGrace), days(organization.MinDeletionGrace)),
			fmt.Sprintf("(default %d days, at least %d days, `SERMON_ORG_DELETION_GRACE`)", days(organization.DefaultDeletionGrace), days(organization.MinDeletionGrace)),
		},
		{
			fmt.Sprintf("(höchstens %d Minuten am Stück)", minutes(liveview.MaxDuration)),
			fmt.Sprintf("(at most %d minutes at a time)", minutes(liveview.MaxDuration)),
		},
		{
			fmt.Sprintf("Tokens laufen ab (%d bis %d Tage)", days(kiosk.MinValidity), days(kiosk.MaxValidity)),
			fmt.Sprintf("tokens expire (%d to %d days)", days(kiosk.MinValidity), days(kiosk.MaxValidity)),
		},
	}
	read := func(lang string) string {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "privacy", lang, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		// Line breaks and table padding don't count.
		return strings.Join(strings.Fields(string(raw)), " ")
	}
	de, en := read("de"), read("en")
	for _, f := range facts {
		if !strings.Contains(de, f.de) {
			t.Errorf("docs/privacy/de: missing %q", f.de)
		}
		if !strings.Contains(en, f.en) {
			t.Errorf("docs/privacy/en: missing %q", f.en)
		}
	}
}
