package webapi

import "testing"

// Labels come from the hosts (device and interface names): shown only as visible text (M2-28).
func TestLabelsView_KeepsHostTextVisible(t *testing.T) {
	got := labelsView(map[string]string{"device": "vda\u202e1", "mode": "user"})
	if got["device"] == "vda\u202e1" || got["mode"] != "user" {
		t.Errorf("labels = %q", got)
	}
}
