package agentapi

import (
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
)

func live(node identity.NodeID, serial, ip string, ended *[]string) *liveSession {
	return &liveSession{
		ActiveSession: ActiveSession{Node: node, Serial: serial, RemoteIP: ip},
		end:           func(error) { *ended = append(*ended, serial) },
		commands:      make(chan struct{}, 1),
	}
}

// After a rotation the agent may reconnect with the new certificate before its old connection is
// cleaned up – from any address (M1-38). The certificate issued in the old session is its
// successor, not a second holder; any other certificate still is a conflict.
func TestClaim_TheSuccessorOfARotationReplacesTheOldSession(t *testing.T) {
	sessions := NewSessions(0)
	node := identity.NodeID(uuid.New())
	var ended []string
	old := live(node, "aa", "192.0.2.1", &ended)
	if result, _ := sessions.claim(old); result != claimed {
		t.Fatalf("first claim = %v", result)
	}
	old.rotatedTo("bb")

	if result, _ := sessions.claim(live(node, "cc", "198.51.100.9", &ended)); result != conflict {
		t.Errorf("an unknown certificate from another address = %v, want conflict", result)
	}
	if result, _ := sessions.claim(live(node, "bb", "198.51.100.9", &ended)); result != replaced {
		t.Errorf("the successor from another address = %v, want replaced", result)
	}
	if len(ended) != 1 || ended[0] != "aa" {
		t.Errorf("ended sessions = %v, want the old one", ended)
	}
}

// A lock of the organization ends the sessions of its nodes only (M2-33).
func TestSessions_TerminateOrganizationEndsOnlyItsNodes(t *testing.T) {
	sessions := NewSessions(0)
	orgA, orgB := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	var ended []string
	for i, org := range []tenant.OrgID{orgA, orgA, orgB} {
		l := live(identity.NodeID(uuid.New()), string(rune('a'+i)), "192.0.2.1", &ended)
		l.Org = org
		if result, _ := sessions.claim(l); result != claimed {
			t.Fatalf("claim %d = %v", i, result)
		}
	}
	sessions.TerminateOrganization(orgA)
	if len(ended) != 2 || len(sessions.Active()) != 1 || sessions.Active()[0].Org != orgB {
		t.Errorf("ended %v, active %+v", ended, sessions.Active())
	}
}

// A changed configuration (ADR 0030) is signalled to the sessions of its organization only, at
// most once while one is pending.
func TestSessions_ConfigChangedReachesOnlyTheOrganization(t *testing.T) {
	sessions := NewSessions(0)
	orgA, orgB := tenant.OrgID(uuid.New()), tenant.OrgID(uuid.New())
	var ended []string
	var all []*liveSession
	for i, org := range []tenant.OrgID{orgA, orgA, orgB} {
		l := live(identity.NodeID(uuid.New()), string(rune('a'+i)), "192.0.2.1", &ended)
		l.Org = org
		l.config = make(chan struct{}, 1)
		if result, _ := sessions.claim(l); result != claimed {
			t.Fatalf("claim %d = %v", i, result)
		}
		all = append(all, l)
	}
	sessions.ConfigChanged(orgA)
	sessions.ConfigChanged(orgA) // does not block while one is pending
	for i, l := range all {
		if got, want := len(l.config), map[bool]int{true: 1, false: 0}[l.Org == orgA]; got != want {
			t.Errorf("session %d: %d signals, want %d", i, got, want)
		}
	}
}
