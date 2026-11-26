package updates

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/pmoscode/SerMon/server/internal/app/routing"
	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/domain/update"
)

// Mode is what an update policy does on its own (KONZEPT §14.2).
type Mode uint8

// The modes.
const (
	// ModeReport only reports; updates come by hand (M4-14).
	ModeReport Mode = iota + 1
	// ModeSecurityAuto installs the security updates in the window.
	ModeSecurityAuto
	// ModeAllAuto installs all updates in the window.
	ModeAllAuto
)

var modeNames = map[Mode]string{ModeReport: "report", ModeSecurityAuto: "security-auto", ModeAllAuto: "all-auto"}

// String is the name in YAML, e.g. "security-auto".
func (m Mode) String() string { return modeNames[m] }

// Bounds of a policy.
const (
	MinParallel    = 1
	MaxParallel    = 50
	MinHealthCheck = 5 * time.Minute
	MaxHealthCheck = 2 * time.Hour
	// MaxPolicyBytes bounds an imported YAML document.
	MaxPolicyBytes = 8 << 10
)

// ErrInvalidPolicy means an update policy is malformed.
var ErrInvalidPolicy = errors.New("invalid update policy")

func invalidPolicy(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, fmt.Sprintf(format, args...))
}

// AgentUpdate says whether a group updates its agents on its own (ADR 0036).
type AgentUpdate uint8

// The agent update modes.
const (
	// AgentManual updates agents by hand only.
	AgentManual AgentUpdate = iota + 1
	// AgentAuto updates the agents of script installations in the window, to the newest release
	// of the group's channel.
	AgentAuto
)

var agentUpdateNames = map[AgentUpdate]string{AgentManual: "manual", AgentAuto: "auto"}

// Rollout says how many nodes of the group update at once and when it stops.
type Rollout struct {
	MaxParallel   int
	StopOnFailure bool
	// HealthCheck is how long a node has to come back online without critical alerts.
	HealthCheck time.Duration
}

// Policy is the update policy of a node group.
type Policy struct {
	Group tenant.GroupID
	Mode  Mode
	// Window is when updates and reboots may run; nil only in ModeReport.
	Window     *routing.Schedule
	AutoReboot bool
	Rollout    Rollout
	// AgentUpdate and Channel: whether and from where the group's agents update themselves.
	AgentUpdate AgentUpdate
	Channel     update.Channel
	// UpdatedAt and UpdatedBy are zero for the default of a group without a policy.
	UpdatedAt time.Time
	UpdatedBy identity.UserID
}

// DefaultPolicy is the policy of a group nobody set one for: report only.
func DefaultPolicy(group tenant.GroupID) Policy {
	return Policy{
		Group: group, Mode: ModeReport, AgentUpdate: AgentManual, Channel: update.ChannelStable,
		Rollout: Rollout{MaxParallel: 1, StopOnFailure: true, HealthCheck: 10 * time.Minute},
	}
}

// Validate checks the policy.
func (p Policy) Validate() error {
	if _, ok := modeNames[p.Mode]; !ok {
		return invalidPolicy("mode must be report, security-auto or all-auto")
	}
	if _, ok := agentUpdateNames[p.AgentUpdate]; !ok {
		return invalidPolicy("agentUpdate must be manual or auto")
	}
	if p.Channel != update.ChannelStable && p.Channel != update.ChannelBeta {
		return invalidPolicy("channel must be stable or beta")
	}
	if p.Window == nil {
		if p.Mode != ModeReport {
			return invalidPolicy("%s needs a window", p.Mode)
		}
		if p.AgentUpdate == AgentAuto {
			return invalidPolicy("agentUpdate auto needs a window")
		}
		if p.AutoReboot {
			return invalidPolicy("autoReboot needs a window")
		}
	} else if err := p.Window.Validate(); err != nil {
		return invalidPolicy("window: %v", err)
	}
	r := p.Rollout
	switch {
	case r.MaxParallel < MinParallel || r.MaxParallel > MaxParallel:
		return invalidPolicy("maxParallel is %d to %d", MinParallel, MaxParallel)
	case r.HealthCheck < MinHealthCheck || r.HealthCheck > MaxHealthCheck || r.HealthCheck%time.Minute != 0:
		return invalidPolicy("healthCheck is %v to %v in minutes", MinHealthCheck, MaxHealthCheck)
	}
	return nil
}

// ModeFor is what the policy does on a node with the package manager m: Alpine has no security
// metadata (KONZEPT O6), so security-auto only reports there; the note says why.
func (p Policy) ModeFor(m update.Manager) (Mode, string) {
	if p.Mode == ModeSecurityAuto && m == update.Apk {
		return ModeReport, "Alpine has no security metadata: security-auto only reports, use all-auto"
	}
	return p.Mode, ""
}

// The YAML form (KONZEPT §14.2).
type (
	policyDoc struct {
		Kind string     `yaml:"kind"`
		Spec policySpec `yaml:"spec"`
	}
	policySpec struct {
		Mode        string      `yaml:"mode"`
		Window      *windowSpec `yaml:"window,omitempty"`
		AutoReboot  bool        `yaml:"autoReboot"`
		Rollout     rolloutSpec `yaml:"rollout"`
		AgentUpdate string      `yaml:"agentUpdate,omitempty"`
		Channel     string      `yaml:"channel,omitempty"`
	}
	windowSpec struct {
		Days     []string `yaml:"days,flow"`
		Start    string   `yaml:"start"`
		Duration string   `yaml:"duration"`
		TimeZone string   `yaml:"timezone"`
	}
	rolloutSpec struct {
		MaxParallel   int    `yaml:"maxParallel"`
		StopOnFailure *bool  `yaml:"stopOnFailure"`
		HealthCheck   string `yaml:"healthCheck"`
	}
)

var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

var clock = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// ParsePolicy reads a policy for group from YAML; unknown fields, a second document and
// anything Validate refuses are ErrInvalidPolicy.
func ParsePolicy(group tenant.GroupID, data []byte) (Policy, error) {
	if len(data) > MaxPolicyBytes {
		return Policy{}, invalidPolicy("document too large (%d bytes, at most %d)", len(data), MaxPolicyBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var doc policyDoc
	if err := decoder.Decode(&doc); err != nil {
		return Policy{}, invalidPolicy("%v", err)
	}
	var more any
	if err := decoder.Decode(&more); !errors.Is(err, io.EOF) {
		return Policy{}, invalidPolicy("a document holds exactly one policy")
	}
	if doc.Kind != "UpdatePolicy" {
		return Policy{}, invalidPolicy("kind must be UpdatePolicy")
	}
	p := DefaultPolicy(group)
	p.Mode = 0
	for m, name := range modeNames {
		if doc.Spec.Mode == name {
			p.Mode = m
		}
	}
	p.AutoReboot = doc.Spec.AutoReboot
	if doc.Spec.AgentUpdate != "" {
		p.AgentUpdate = 0
		for a, name := range agentUpdateNames {
			if doc.Spec.AgentUpdate == name {
				p.AgentUpdate = a
			}
		}
	}
	if doc.Spec.Channel != "" {
		p.Channel = 0
		for _, c := range []update.Channel{update.ChannelStable, update.ChannelBeta} {
			if doc.Spec.Channel == c.String() {
				p.Channel = c
			}
		}
	}
	r := doc.Spec.Rollout
	if r.MaxParallel != 0 {
		p.Rollout.MaxParallel = r.MaxParallel
	}
	// Left out, the default (true) holds like for the other fields (M4-31).
	if r.StopOnFailure != nil {
		p.Rollout.StopOnFailure = *r.StopOnFailure
	}
	if r.HealthCheck != "" {
		d, err := time.ParseDuration(r.HealthCheck)
		if err != nil {
			return Policy{}, invalidPolicy("healthCheck: %v", err)
		}
		p.Rollout.HealthCheck = d
	}
	if w := doc.Spec.Window; w != nil {
		window, err := w.schedule()
		if err != nil {
			return Policy{}, err
		}
		p.Window = &window
	}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func (w windowSpec) schedule() (routing.Schedule, error) {
	s := routing.Schedule{TimeZone: w.TimeZone}
	for _, d := range w.Days {
		i := slices.Index(dayNames, strings.ToLower(d))
		if i < 0 {
			return routing.Schedule{}, invalidPolicy("window: unknown day %q (sun to sat)", d)
		}
		s.Days = append(s.Days, time.Weekday(i))
	}
	m := clock.FindStringSubmatch(w.Start)
	if m == nil {
		return routing.Schedule{}, invalidPolicy("window: start is a time of day like 03:00")
	}
	hours, _ := strconv.Atoi(m[1])
	minutes, _ := strconv.Atoi(m[2])
	s.Start = time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
	d, err := time.ParseDuration(w.Duration)
	if err != nil {
		return routing.Schedule{}, invalidPolicy("window: duration: %v", err)
	}
	s.Duration = d
	return s, nil
}

// duration writes d as YAML does in KONZEPT §14.2: 2h, 90m, 10m.
func duration(d time.Duration) string {
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
}

// YAML writes the policy as ParsePolicy reads it.
func (p Policy) YAML() []byte {
	doc := policyDoc{Kind: "UpdatePolicy", Spec: policySpec{
		Mode: p.Mode.String(), AutoReboot: p.AutoReboot, AgentUpdate: agentUpdateNames[p.AgentUpdate], Channel: p.Channel.String(),
		Rollout: rolloutSpec{MaxParallel: p.Rollout.MaxParallel, StopOnFailure: &p.Rollout.StopOnFailure, HealthCheck: duration(p.Rollout.HealthCheck)},
	}}
	if w := p.Window; w != nil {
		spec := &windowSpec{
			Start:    fmt.Sprintf("%02d:%02d", int(w.Start/time.Hour), int(w.Start%time.Hour/time.Minute)),
			Duration: duration(w.Duration), TimeZone: w.TimeZone,
		}
		for _, d := range w.Days {
			spec.Days = append(spec.Days, dayNames[d])
		}
		doc.Spec.Window = spec
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	_ = encoder.Encode(doc) // plain structs of strings, numbers and booleans
	_ = encoder.Close()
	return out.Bytes()
}
