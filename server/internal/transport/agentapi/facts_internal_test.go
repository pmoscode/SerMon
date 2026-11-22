package agentapi

import (
	"testing"

	agentv1 "github.com/pmoscode/SerMon/server/internal/gen/sermon/agent/v1"

	"github.com/pmoscode/SerMon/server/internal/domain/secevent"
)

// The security source of the Hello (M2-31): every value maps, older agents send none.
func TestFactsFrom_SecuritySource(t *testing.T) {
	for wire, want := range map[agentv1.SecuritySource]secevent.Source{
		agentv1.SecuritySource_SECURITY_SOURCE_UNSPECIFIED:     secevent.SourceUnknown,
		agentv1.SecuritySource_SECURITY_SOURCE_JOURNAL:         secevent.SourceJournal,
		agentv1.SecuritySource_SECURITY_SOURCE_FILE_VERIFIED:   secevent.SourceFileVerified,
		agentv1.SecuritySource_SECURITY_SOURCE_FILE_UNVERIFIED: secevent.SourceFileUnverified,
		agentv1.SecuritySource_SECURITY_SOURCE_NONE:            secevent.SourceNone,
		agentv1.SecuritySource(99):                             secevent.SourceUnknown,
	} {
		if got := factsFrom(&agentv1.HostFacts{SecuritySource: wire}).SecuritySource; got != want {
			t.Errorf("%v → %q, want %q", wire, got, want)
		}
	}
}

// A taken-over agent chooses its host name and OS strings; they are shown in the UI and in
// notifications, so they keep nothing that reorders or hides text (M2-28).
func TestFactsFrom_HostTextIsVisible(t *testing.T) {
	f := factsFrom(&agentv1.HostFacts{Hostname: "web-01\u202e", OsId: "deb\u200bian", OsVersion: "12\u2028", KernelRelease: "6.1\u2066"})
	if f.Hostname != "web-01\ufffd" || f.OSID != "deb\ufffdian" || f.OSVersion != "12\ufffd" || f.KernelRelease != "6.1\ufffd" {
		t.Errorf("facts %+v", f)
	}
}
