package module

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pmoscode/SerMon/server/internal/domain/textsafe"
)

// Bounds, the same as in the protocol (agent.proto: AgentConfig, ModuleHealth, DiscoveryHint).
const (
	MaxModules         = 32
	MaxConfigBytes     = 16 * 1024
	MinInterval        = 10 * time.Second
	MaxInterval        = time.Hour
	MaxReasonBytes     = 256
	MaxHints           = 32
	MaxHintReasons     = 8
	MaxSuggestionBytes = 1024
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// ValidID reports whether id can name a module.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// ErrInvalid marks a module setting the server does not accept.
var ErrInvalid = errors.New("invalid module setting")

// ErrUnknown means the server knows no module of that ID.
var ErrUnknown = errors.New("unknown module")

// InvalidError says what is wrong, in the agent's words for the schema.
type InvalidError struct {
	Module string
	Reason string
}

func (e *InvalidError) Error() string { return e.Module + ": " + e.Reason }

// Unwrap makes errors.Is(err, ErrInvalid) true.
func (e *InvalidError) Unwrap() error { return ErrInvalid }

// Setting is the configuration of one module for a node group.
type Setting struct {
	Module  string
	Enabled bool
	// Interval is how often it collects; 0 takes the module's default.
	Interval time.Duration
	// Config is the configuration as compact JSON without credentials and defaults.
	Config string
}

// ForAgent keeps the settings of the modules an agent has built in: the server offers a node
// only those (ADR 0039).
func ForAgent(settings []Setting, builtin []string) []Setting {
	out := make([]Setting, 0, len(settings))
	for _, s := range settings {
		if slices.Contains(builtin, s.Module) {
			out = append(out, s)
		}
	}
	return out
}

// Info describes a module the server knows.
type Info struct {
	ID     string
	Schema *Schema
}

// Catalog are the modules the server knows: their schemas, copied from the agent's build.
type Catalog struct {
	modules map[string]*Schema
}

// NewCatalog returns a catalog of schemas by module ID (tests; the server uses Builtin).
func NewCatalog(schemas map[string]string) (*Catalog, error) {
	c := &Catalog{modules: map[string]*Schema{}}
	for id, raw := range schemas {
		if !ValidID(id) {
			return nil, fmt.Errorf("%s: not a module id", id)
		}
		s, err := ParseSchema(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		c.modules[id] = s
	}
	return c, nil
}

//go:embed schemas
var schemaFiles embed.FS

var builtin = sync.OnceValues(func() (*Catalog, error) { return load(schemaFiles) })

// Builtin returns the modules of the agent's build (agent/crates/sermon-modules/schemas, copied by
// just gen). A broken schema is a bug of the build; the catalog's test fails on it.
func Builtin() (*Catalog, error) { return builtin() }

func load(files fs.FS) (*Catalog, error) {
	entries, err := fs.ReadDir(files, "schemas")
	if err != nil {
		return nil, fmt.Errorf("module schemas: %w", err)
	}
	schemas := map[string]string{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if e.IsDir() || !ok {
			continue
		}
		raw, err := fs.ReadFile(files, "schemas/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("module schema %s: %w", id, err)
		}
		schemas[id] = string(raw)
	}
	return NewCatalog(schemas)
}

// Lookup returns the schema of a module.
func (c *Catalog) Lookup(id string) (*Schema, bool) {
	s, ok := c.modules[id]
	return s, ok
}

// All returns the modules sorted by ID.
func (c *Catalog) All() []Info {
	out := make([]Info, 0, len(c.modules))
	for id, s := range c.modules {
		out = append(out, Info{ID: id, Schema: s})
	}
	slices.SortFunc(out, func(a, b Info) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// Check validates a setting against its module's schema and the bounds of the protocol and
// returns it with the configuration normalized; secrets are the credentials that have a value.
func (c *Catalog) Check(s Setting, secrets []string) (Setting, error) {
	schema, ok := c.Lookup(s.Module)
	if !ok {
		return Setting{}, ErrUnknown
	}
	if s.Interval != 0 && (s.Interval < MinInterval || s.Interval > MaxInterval || s.Interval%time.Second != 0) {
		return Setting{}, &InvalidError{Module: s.Module, Reason: "the interval is 10 s to 1 h in whole seconds"}
	}
	if len(s.Config) > MaxConfigBytes {
		return Setting{}, &InvalidError{Module: s.Module, Reason: fmt.Sprintf("the configuration has more than %d bytes", MaxConfigBytes)}
	}
	normalized, err := schema.Normalize(s.Config, secrets)
	if err != nil {
		return Setting{}, &InvalidError{Module: s.Module, Reason: err.Error()}
	}
	if len(normalized) > MaxConfigBytes {
		return Setting{}, &InvalidError{Module: s.Module, Reason: fmt.Sprintf("the configuration has more than %d bytes", MaxConfigBytes)}
	}
	s.Config = normalized
	return s, nil
}

// State is how a module does, with the codes of the protocol (ModuleState).
type State uint8

// States.
const (
	StateUnspecified State = iota
	StateOK
	StateError
	StateTimeout
	StateHung
	StateCrashed
	StateInvalidConfig
	StateVetoed
)

// Valid reports whether s is a state an agent may report.
func (s State) Valid() bool { return s >= StateOK && s <= StateVetoed }

// Health is the state of one module on a node.
type Health struct {
	Module string
	State  State
	// Reason says why it doesn't run or failed; from the host: text only.
	Reason      string
	LastSuccess *time.Time
}

// HealthReport is what the agent reported about its enabled modules.
type HealthReport struct {
	CollectedAt time.Time
	Modules     []Health
}

// Hint suggests a module for a node.
type Hint struct {
	Module string
	// Reasons are what the agent saw (process:nginx, port:80, unit:nginx.service).
	Reasons []string
	// Suggested is a configuration to start the form with (compact JSON), empty for none.
	Suggested string
}

// Hints are the modules the agent suggests for its host.
type Hints struct {
	CollectedAt time.Time
	Hints       []Hint
}

var reasonPattern = regexp.MustCompile(`^(process|port|unit):[A-Za-z0-9@._:-]{1,128}$`)

// Normalized keeps the entries of modules the catalog knows, each once, with visible reasons
// within their bound.
func (r HealthReport) Normalized(c *Catalog) HealthReport {
	out := HealthReport{CollectedAt: r.CollectedAt}
	for _, h := range r.Modules {
		if _, known := c.Lookup(h.Module); !known || !h.State.Valid() || len(out.Modules) >= MaxModules ||
			slices.ContainsFunc(out.Modules, func(o Health) bool { return o.Module == h.Module }) {
			continue
		}
		h.Reason = cut(textsafe.Visible(h.Reason), MaxReasonBytes)
		out.Modules = append(out.Modules, h)
	}
	return out
}

// Normalized keeps the hints of modules the catalog knows, each once, with valid reasons; a
// suggestion outside the module's schema is left out.
func (h Hints) Normalized(c *Catalog) Hints {
	out := Hints{CollectedAt: h.CollectedAt}
	for _, hint := range h.Hints {
		schema, known := c.Lookup(hint.Module)
		if !known || len(out.Hints) >= MaxHints || slices.ContainsFunc(out.Hints, func(o Hint) bool { return o.Module == hint.Module }) {
			continue
		}
		var reasons []string
		for _, r := range hint.Reasons {
			if reasonPattern.MatchString(r) && !slices.Contains(reasons, r) && len(reasons) < MaxHintReasons {
				reasons = append(reasons, r)
			}
		}
		if len(reasons) == 0 {
			continue
		}
		suggested := ""
		if hint.Suggested != "" && len(hint.Suggested) <= MaxSuggestionBytes {
			if s, err := schema.NormalizePart(hint.Suggested); err == nil {
				suggested = s
			}
		}
		out.Hints = append(out.Hints, Hint{Module: hint.Module, Reasons: reasons, Suggested: suggested})
	}
	return out
}

// cut shortens s to at most maxBytes without splitting a character.
func cut(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
