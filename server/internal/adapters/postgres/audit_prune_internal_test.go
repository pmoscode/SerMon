package postgres

import (
	"encoding/json"
	"testing"
)

// V1-17: a chain cut by the retention starts where its audit.prune entry says; any other cut
// has no start.
func TestChainStart(t *testing.T) {
	marker := func(seq int64, hash []byte) chainLink {
		d, _ := json.Marshal(pruneDetails(seq, hash))
		return chainLink{seq: 9, prev: []byte{8}, action: PruneAction, details: d}
	}
	cut := chainLink{seq: 5, prev: []byte{0xab, 0xcd}, action: "member.invite", details: []byte(`{}`)}
	for name, tc := range map[string]struct {
		links []chainLink
		ok    bool
		prev  []byte
	}{
		"empty":                  {nil, true, []byte{}},
		"from seq 1":             {[]chainLink{{seq: 1, prev: []byte{}}}, true, []byte{}},
		"cut with its marker":    {[]chainLink{cut, marker(4, []byte{0xab, 0xcd})}, true, []byte{0xab, 0xcd}},
		"no marker":              {[]chainLink{cut}, false, nil},
		"marker of another seq":  {[]chainLink{cut, marker(3, []byte{0xab, 0xcd})}, false, nil},
		"marker of another hash": {[]chainLink{cut, marker(4, []byte{0xab})}, false, nil},
		"not a prune entry":      {[]chainLink{cut, {seq: 6, action: "member.remove", details: mustJSON(pruneDetails(4, []byte{0xab, 0xcd}))}}, false, nil},
	} {
		prev, ok := chainStart(tc.links)
		if ok != tc.ok || string(prev) != string(tc.prev) {
			t.Errorf("%s: %x, %v", name, prev, ok)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
