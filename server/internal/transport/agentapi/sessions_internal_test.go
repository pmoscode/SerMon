package agentapi

import (
	"testing"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
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
