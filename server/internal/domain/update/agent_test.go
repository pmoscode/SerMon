package update_test

import (
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// The same order as the root helper (sermon-helper, minisign.rs).
func TestNewer_OrdersBySemVer(t *testing.T) {
	for _, c := range [][2]string{
		{"1.0.1", "1.0.0"},
		{"1.10.0", "1.9.9"},
		{"2.0.0", "1.99.99"},
		{"1.0.0", "1.0.0-rc.1"},
		{"1.0.0-rc.2", "1.0.0-rc.1"},
		{"1.0.0-rc.10", "1.0.0-rc.9"},
		{"1.0.0-rc.1.1", "1.0.0-rc.1"},
		{"1.0.0-beta", "1.0.0-alpha"},
		{"1.0.0-alpha", "1.0.0-1"},
		{"0.0.3-e2e", "0.0.1-e2e"},
	} {
		if !update.Newer(c[0], c[1]) || update.Newer(c[1], c[0]) {
			t.Errorf("%s vs %s", c[0], c[1])
		}
	}
	for _, bad := range []string{"e2e", "1.0", "1.0.0.0", "1.0.0-", "1.0.0-a..b", "1.0.0-a_b", ""} {
		if update.Newer(bad, "0.0.1") || update.Newer("9.9.9", bad) {
			t.Errorf("%q compared", bad)
		}
	}
	if update.Newer("1.0.0", "1.0.0") {
		t.Error("equal is newer")
	}
}

func TestAgent_TargetOnlyForANewerReleaseOfAScriptInstall(t *testing.T) {
	script := update.Agent{Version: "1.0.0", Method: update.InstallScript, Arch: "arm64"}
	if got := script.Target("1.1.0"); got != "1.1.0" {
		t.Errorf("Target = %q", got)
	}
	for name, c := range map[string]struct {
		agent  update.Agent
		latest string
	}{
		"as new":    {script, "1.0.0"},
		"older":     {script, "0.9.0"},
		"none":      {script, ""},
		"package":   {update.Agent{Version: "1.0.0", Method: update.InstallPackage}, "1.1.0"},
		"unknown":   {update.Agent{Version: "1.0.0"}, "1.1.0"},
		"no semver": {update.Agent{Version: "dev", Method: update.InstallScript}, "1.1.0"},
	} {
		if got := c.agent.Target(c.latest); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
	if update.ChannelBeta.String() != "beta" || update.ChannelStable.String() != "stable" {
		t.Error("channel names")
	}
}
