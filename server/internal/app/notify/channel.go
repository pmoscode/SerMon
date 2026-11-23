package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/pmoscode/SerMon/server/internal/domain/identity"
	"github.com/pmoscode/SerMon/server/internal/domain/tenant"
	"github.com/pmoscode/SerMon/server/internal/platform/safehttp"
	"github.com/pmoscode/SerMon/server/internal/secrets"
)

var (
	// ErrUnknownType means no notifier implements the channel type.
	ErrUnknownType = errors.New("unknown channel type")
	// ErrInvalidChannel means a channel's name, language or configuration is invalid. Messages
	// name the field, never its value.
	ErrInvalidChannel = errors.New("invalid channel")
)

// ChannelType names a kind of channel (email, webhook, slack, …).
type ChannelType string

// ChannelID identifies a channel.
type ChannelID uuid.UUID

// String returns the UUID.
func (id ChannelID) String() string { return uuid.UUID(id).String() }

// FieldKind is how a configuration field is entered and checked.
type FieldKind string

// Kinds of configuration fields.
const (
	FieldText   FieldKind = "text"
	FieldURL    FieldKind = "url"    // http(s), checked by safehttp when stored
	FieldChoice FieldKind = "choice" // one of Choices
	FieldNumber FieldKind = "number" // a non-negative integer
)

// Field describes one configuration field of a channel type; the UI builds its form from them.
// Secret fields are sealed, shown as set or not set and never returned.
type Field struct {
	Name     string
	Kind     FieldKind
	Secret   bool
	Required bool
	Choices  []string
	MaxLen   int // 0: 1024 for text, 2048 for URLs, 4096 for secrets
}

// MaxLength is the longest allowed value in bytes.
func (f Field) MaxLength() int { return f.maxLen() }

func (f Field) maxLen() int {
	switch {
	case f.MaxLen > 0:
		return f.MaxLen
	case f.Secret:
		return 4096
	case f.Kind == FieldURL:
		return 2048
	}
	return 1024
}

// Config is a channel's configuration as a notifier sees it, with the secrets opened.
type Config struct {
	Settings map[string]string
	Secrets  map[string]string
}

// Notifier delivers messages over one channel type. Send uses the safehttp client it was built
// with (or the SMTP adapter); it returns Permanent errors for what retrying won't fix and
// *RateLimitedError when the service asks to wait.
type Notifier interface {
	Type() ChannelType
	Fields() []Field
	// Validate checks what only the type knows (token formats, chat IDs); the generic checks
	// of the fields ran before. Its errors reach the UI: they name fields, never values.
	Validate(c Config) error
	Send(ctx context.Context, c Config, m Message) error
}

// Registry holds the notifiers by type.
type Registry struct {
	byType map[ChannelType]Notifier
}

var fieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// NewRegistry checks the notifiers' field descriptions.
func NewRegistry(notifiers ...Notifier) (*Registry, error) {
	r := &Registry{byType: map[ChannelType]Notifier{}}
	for _, n := range notifiers {
		if _, dup := r.byType[n.Type()]; dup || !fieldName.MatchString(string(n.Type())) {
			return nil, fmt.Errorf("notifier type %q is invalid or registered twice", n.Type())
		}
		seen := map[string]bool{}
		for _, f := range n.Fields() {
			if seen[f.Name] || !fieldName.MatchString(f.Name) || (f.Kind == FieldChoice) != (len(f.Choices) > 0) {
				return nil, fmt.Errorf("notifier %s: field %q is invalid or described twice", n.Type(), f.Name)
			}
			seen[f.Name] = true
		}
		r.byType[n.Type()] = n
	}
	return r, nil
}

// Get returns the notifier of a type.
func (r *Registry) Get(t ChannelType) (Notifier, bool) {
	n, ok := r.byType[t]
	return n, ok
}

// Types returns the notifiers by type name.
func (r *Registry) Types() []Notifier {
	out := slices.Collect(maps.Values(r.byType))
	slices.SortFunc(out, func(a, b Notifier) int { return strings.Compare(string(a.Type()), string(b.Type())) })
	return out
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidChannel, fmt.Sprintf(format, args...))
}

// checkConfig runs the generic checks of the fields: known names, secrets only in secret fields,
// required ones present, lengths, URLs allowed by the outbound policy, choices, numbers.
func checkConfig(n Notifier, c Config, outbound safehttp.Policy) error {
	fields := map[string]Field{}
	for _, f := range n.Fields() {
		fields[f.Name] = f
	}
	for _, part := range []struct {
		values map[string]string
		secret bool
	}{{c.Settings, false}, {c.Secrets, true}} {
		for name, value := range part.values {
			f, ok := fields[name]
			switch {
			case !ok:
				return invalid("%s has no field %q", n.Type(), name)
			case f.Secret != part.secret:
				return invalid("%s belongs among the %s", name, map[bool]string{true: "secrets", false: "settings"}[f.Secret])
			}
			if err := checkValue(f, value, outbound); err != nil {
				return err
			}
		}
	}
	for _, f := range n.Fields() {
		values := c.Settings
		if f.Secret {
			values = c.Secrets
		}
		if _, ok := values[f.Name]; f.Required && !ok {
			return invalid("%s is required", f.Name)
		}
	}
	if err := n.Validate(c); err != nil {
		return invalid("%v", err)
	}
	return nil
}

func checkValue(f Field, value string, outbound safehttp.Policy) error {
	switch {
	case value == "":
		return invalid("%s is empty (leave it out instead)", f.Name)
	case len(value) > f.maxLen() || !utf8.ValidString(value):
		return invalid("%s is longer than %d bytes or not UTF-8", f.Name, f.maxLen())
	case strings.IndexFunc(value, unicode.IsControl) >= 0:
		return invalid("%s contains control characters", f.Name)
	}
	switch f.Kind {
	case FieldURL:
		if _, err := outbound.ValidateURL(value); err != nil {
			// The policy's errors name no URL.
			return invalid("%s: %v", f.Name, err)
		}
	case FieldChoice:
		if !slices.Contains(f.Choices, value) {
			return invalid("%s must be one of %v", f.Name, f.Choices)
		}
	case FieldNumber:
		if n, err := strconv.ParseUint(value, 10, 31); err != nil || n > 1<<30 {
			return invalid("%s must be a non-negative integer", f.Name)
		}
	case FieldText:
	}
	return nil
}

// Channel is a configured channel as shown: its settings, and only the names of the secrets
// that are set.
type Channel struct {
	ID          ChannelID
	Type        ChannelType
	Name        string
	Lang        Lang
	Enabled     bool
	Settings    map[string]string
	SecretNames []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CreatedBy   identity.UserID
}

// StoredChannel is a channel with its sealed secrets (JSON object, sealed in SecretContext).
type StoredChannel struct {
	Channel
	SealedSecrets []byte
}

// SecretContext binds the sealed secrets to their organization and channel: a ciphertext copied
// to another record does not open.
func SecretContext(org tenant.OrgID, id ChannelID) []byte {
	return []byte("notification_channel:" + org.String() + ":" + id.String())
}

func sealSecrets(box *secrets.Box, org tenant.OrgID, id ChannelID, values map[string]string) ([]byte, error) {
	if values == nil {
		values = map[string]string{}
	}
	plain, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode channel secrets: %w", err)
	}
	sealed, err := box.Seal(plain, SecretContext(org, id))
	if err != nil {
		return nil, fmt.Errorf("seal channel secrets: %w", err)
	}
	return sealed, nil
}

// OpenSecrets opens the secrets of a stored channel.
func OpenSecrets(box *secrets.Box, org tenant.OrgID, c StoredChannel) (map[string]string, error) {
	plain, err := box.Open(c.SealedSecrets, SecretContext(org, c.ID))
	if err != nil {
		return nil, fmt.Errorf("open channel secrets: %w", err)
	}
	var values map[string]string
	if err := json.Unmarshal(plain, &values); err != nil {
		return nil, fmt.Errorf("decode channel secrets: %w", err)
	}
	return values, nil
}

// liftSecrets moves values of secret fields that older versions kept among the settings (the
// webhook URL until M2-39) into the secrets; a value already among the secrets wins. It returns
// copies.
func liftSecrets(n Notifier, settings, secrets map[string]string) (map[string]string, map[string]string) {
	settings, secrets = cloneMap(settings), cloneMap(secrets)
	if secrets == nil {
		secrets = map[string]string{}
	}
	for _, f := range n.Fields() {
		v, ok := settings[f.Name]
		if !f.Secret || !ok {
			continue
		}
		delete(settings, f.Name)
		if _, set := secrets[f.Name]; !set {
			secrets[f.Name] = v
		}
	}
	return settings, secrets
}

// view is a channel as the API shows it: values of secret fields never appear among the
// settings, their names among the secret names.
func (s *Service) view(c Channel) Channel {
	n, ok := s.d.Registry.Get(c.Type)
	if !ok {
		return c
	}
	settings, lifted := liftSecrets(n, c.Settings, nil)
	for name := range lifted {
		if !slices.Contains(c.SecretNames, name) {
			c.SecretNames = append(slices.Clone(c.SecretNames), name)
		}
	}
	slices.Sort(c.SecretNames)
	c.Settings = settings
	return c
}

const maxName = 100

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxName || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", invalid("the name needs 1 to %d characters without control characters", maxName)
	}
	return name, nil
}

func checkLang(l Lang) error {
	if l != German && l != English {
		return invalid("language must be de or en")
	}
	return nil
}
