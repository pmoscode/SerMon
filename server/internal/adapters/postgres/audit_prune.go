package postgres

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// PruneAction is the audit entry the retention appends after cutting the start of a chain
// (V1-17): it names the last dropped entry, so the chain stays verifiable from there.
const PruneAction = "audit.prune"

// pruneDetails names the last dropped entry.
func pruneDetails(seq int64, hash []byte) map[string]string {
	return map[string]string{"through_seq": strconv.FormatInt(seq, 10), "last_hash": hex.EncodeToString(hash)}
}

// chainLink is what the start of a chain needs of an entry.
type chainLink struct {
	seq     int64
	prev    []byte
	action  string
	details []byte
}

// chainStart returns the hash the first remaining entry must follow: empty for a chain from seq 1,
// else the hash an audit.prune entry of the same chain names for the entry before it. A cut
// without that entry (rows deleted by hand) has no start: ok is false.
func chainStart(links []chainLink) (prev []byte, ok bool) {
	if len(links) == 0 || links[0].seq == 1 {
		return []byte{}, true
	}
	first := links[0]
	want := pruneDetails(first.seq-1, first.prev)
	for _, l := range links {
		if l.action != PruneAction {
			continue
		}
		var d map[string]string
		if json.Unmarshal(l.details, &d) == nil && d["through_seq"] == want["through_seq"] && d["last_hash"] == want["last_hash"] {
			return first.prev, true
		}
	}
	return nil, false
}
