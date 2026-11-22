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
