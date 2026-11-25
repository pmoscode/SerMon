package update

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
)

// InstallMethod is how a node's agent was installed (its Hello, ADR 0036).
type InstallMethod uint8

// The install methods.
const (
	InstallUnknown InstallMethod = iota
	// InstallScript is install.sh (/usr/local/bin): the agent updates itself.
	InstallScript
	// InstallPackage is a package: the package manager updates the agent.
	InstallPackage
)

// Channel is where a group's agents take their releases from (ADR 0036).
type Channel uint8

// The channels.
const (
	ChannelStable Channel = iota + 1
	ChannelBeta
)

var channelNames = map[Channel]string{ChannelStable: "stable", ChannelBeta: "beta"}

// String is the channel's name, e.g. "beta".
func (c Channel) String() string { return channelNames[c] }

// Agent is a node's agent: its version, how it was installed and its architecture.
type Agent struct {
	Version string
	Method  InstallMethod
	Arch    string
}

// Target is the version the agent updates itself to from latest, the newest release of its
// channel: "" for an agent that doesn't update itself or is as new.
func (a Agent) Target(latest string) string {
	if a.Method != InstallScript || !Newer(latest, a.Version) {
		return ""
	}
	return latest
}

var semver = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// Newer reports whether version a is newer than b by SemVer precedence; false if either is no
// semantic version (as the root helper decides, ADR 0036).
func Newer(a, b string) bool {
	ma, mb := semver.FindStringSubmatch(a), semver.FindStringSubmatch(b)
	if ma == nil || mb == nil || len(a) > 64 || len(b) > 64 {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.ParseUint(ma[i], 10, 64)
		y, _ := strconv.ParseUint(mb[i], 10, 64)
		if x != y {
			return x > y
		}
	}
	return comparePre(ma[4], mb[4]) > 0
}

// comparePre orders pre-releases; none is the release itself and comes last.
func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(pa), len(pb)) {
		x, errX := strconv.ParseUint(pa[i], 10, 64)
		y, errY := strconv.ParseUint(pb[i], 10, 64)
		var c int
		switch {
		case errX == nil && errY == nil:
			c = cmp.Compare(x, y)
		case errX == nil:
			c = -1
		case errY == nil:
			c = 1
		default:
			c = strings.Compare(pa[i], pb[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(pa), len(pb))
}
