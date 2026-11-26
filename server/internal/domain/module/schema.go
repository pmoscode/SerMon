package module

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The schema of a module's configuration (ADR 0039): the subset of JSON Schema 2020-12 the agent
// checks (agent/crates/sermon-modules/src/schema.rs). Both share their test vectors; the agent's
// answer counts, the server checks first so an admin sees the problem at once. The messages are
// the agent's.
const (
	maxName       = 64
	maxProperties = 64
	maxStringLen  = 4096
	maxItems      = 256
	maxDurationMS = 3_600_000
)

// Schema is a checked schema.
type Schema struct {
	raw        string
	properties map[string]property
	required   []string
}

type kind uint8

const (
	kindString kind = iota + 1
	kindInteger
	kindNumber
	kindBoolean
	kindStrings
	kindIntegers
)

type property struct {
	kind       kind
	text       text // string, strings
	imin, imax int64
	fmin, fmax float64
	maxItems   int
	defaultV   any
	hasDefault bool
	secret     bool
}

type text struct {
	minLen, maxLen int
	pattern        *regexp.Regexp
	values         []string
	format         string
}

// decode reads one JSON value with numbers kept as written.
func decode(s string) (any, error) {
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("not JSON: trailing characters")
	}
	return v, nil
}

func asInt(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return i, err == nil
}

func asCount(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	u, err := strconv.ParseUint(n.String(), 10, 64)
	// Beyond any bound, like the agent's out of range.
	return int(min(u, 1<<31)), err == nil
}

func asFloat(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

func only(object map[string]any, keys []string, what string) error {
	for _, k := range slices.Sorted(maps.Keys(object)) {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("%s: %s is not in the subset", what, k)
		}
	}
	return nil
}

// ParseSchema checks text against the subset.
func ParseSchema(raw string) (*Schema, error) {
	v, err := decode(raw)
	if err != nil {
		return nil, err
	}
	root, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("the schema is no object")
	}
	if err := only(root, []string{"$schema", "$id", "title", "description", "type", "properties", "required", "additionalProperties"}, "the schema"); err != nil {
		return nil, err
	}
	if root["type"] != "object" {
		return nil, errors.New("the schema must be of type object")
	}
	if root["additionalProperties"] != false {
		return nil, errors.New("the schema must set additionalProperties to false")
	}
	rawProps, ok := root["properties"].(map[string]any)
	if !ok {
		return nil, errors.New("the schema has no properties")
	}
	if len(rawProps) > maxProperties {
		return nil, fmt.Errorf("more than %d properties", maxProperties)
	}
	s := &Schema{raw: raw, properties: map[string]property{}}
	for _, name := range slices.Sorted(maps.Keys(rawProps)) {
		if !validName(name) {
			return nil, fmt.Errorf("%s: not a property name", name)
		}
		p, err := parseProperty(rawProps[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if p.hasDefault {
			if p.secret {
				return nil, fmt.Errorf("%s: a credential has no default", name)
			}
			if err := check(p, p.defaultV); err != nil {
				return nil, fmt.Errorf("%s: default: %w", name, err)
			}
		}
		s.properties[name] = p
	}
	if r, present := root["required"]; present {
		list, ok := r.([]any)
		if !ok {
			return nil, errors.New("required is no list")
		}
		for _, item := range list {
			name, ok := item.(string)
			if _, known := s.properties[name]; !ok || !known {
				return nil, fmt.Errorf("required names an unknown property: %v", item)
			}
			s.required = append(s.required, name)
		}
	}
	return s, nil
}

func validName(name string) bool {
	if name == "" || len(name) > maxName || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := range len(name) {
		b := name[i]
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '_' {
			return false
		}
	}
	return true
}

var common = []string{"type", "title", "description", "default", "writeOnly"}

func with(extra ...string) []string { return append(slices.Clone(common), extra...) }

func parseProperty(v any) (property, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return property{}, errors.New("no object")
	}
	var p property
	switch w := m["writeOnly"].(type) {
	case nil:
	case bool:
		p.secret = w
	default:
		return property{}, errors.New("writeOnly is no boolean")
	}
	var err error
	switch m["type"] {
	case "string":
		if err = only(m, with("maxLength", "minLength", "pattern", "enum", "format"), "a string"); err == nil {
			p.kind = kindString
			p.text, err = parseText(m)
		}
	case "integer":
		if err = only(m, with("minimum", "maximum"), "an integer"); err == nil {
			p.kind = kindInteger
			p.imin, p.imax, err = intBounds(m)
		}
	case "number":
		if err = only(m, with("minimum", "maximum"), "a number"); err == nil {
			p.kind = kindNumber
			p.fmin, p.fmax, err = floatBounds(m)
		}
	case "boolean":
		p.kind = kindBoolean
		err = only(m, common, "a boolean")
	case "array":
		if err = only(m, with("maxItems", "items"), "an array"); err == nil {
			err = parseArray(m, &p)
		}
	default:
		return property{}, errors.New("type is string, integer, number, boolean or array")
	}
	if err != nil {
		return property{}, err
	}
	if p.secret && p.kind != kindString {
		return property{}, errors.New("only a string can be a credential")
	}
	p.defaultV, p.hasDefault = m["default"]
	return p, nil
}

func parseArray(m map[string]any, p *property) error {
	n, ok := asCount(m["maxItems"])
	if !ok || n < 1 || n > maxItems {
		return fmt.Errorf("an array needs maxItems of 1 to %d", maxItems)
	}
	p.maxItems = n
	items, ok := m["items"].(map[string]any)
	if !ok {
		return errors.New("an array needs items")
	}
	var err error
	switch items["type"] {
	case "string":
		if err = only(items, []string{"type", "maxLength", "minLength", "pattern", "enum", "format"}, "items"); err == nil {
			p.kind = kindStrings
			p.text, err = parseText(items)
		}
	case "integer":
		if err = only(items, []string{"type", "minimum", "maximum"}, "items"); err == nil {
			p.kind = kindIntegers
			p.imin, p.imax, err = intBounds(items)
		}
	default:
		return errors.New("items are strings or integers")
	}
	return err
}

func intBounds(m map[string]any) (int64, int64, error) {
	lo, ok := asInt(m["minimum"])
	if !ok {
		return 0, 0, errors.New("an integer needs minimum")
	}
	hi, ok := asInt(m["maximum"])
	if !ok {
		return 0, 0, errors.New("an integer needs maximum")
	}
	if lo > hi {
		return 0, 0, errors.New("minimum above maximum")
	}
	return lo, hi, nil
}

func floatBounds(m map[string]any) (float64, float64, error) {
	lo, ok := asFloat(m["minimum"])
	if !ok {
		return 0, 0, errors.New("a number needs minimum")
	}
	hi, ok := asFloat(m["maximum"])
	if !ok {
		return 0, 0, errors.New("a number needs maximum")
	}
	if lo > hi {
		return 0, 0, errors.New("minimum above maximum")
	}
	return lo, hi, nil
}

func length(m map[string]any, key string) (int, bool, error) {
	v, present := m[key]
	if !present {
		return 0, false, nil
	}
	n, ok := asCount(v)
	if !ok {
		return 0, false, fmt.Errorf("%s is no count", key)
	}
	return n, true, nil
}

func parseText(m map[string]any) (text, error) {
	var t text
	maxLen, present, err := length(m, "maxLength")
	if err != nil {
		return text{}, err
	}
	if !present || maxLen < 1 || maxLen > maxStringLen {
		return text{}, fmt.Errorf("a string needs maxLength of 1 to %d", maxStringLen)
	}
	t.maxLen = maxLen
	if t.minLen, _, err = length(m, "minLength"); err != nil {
		return text{}, err
	}
	if t.minLen > t.maxLen {
		return text{}, errors.New("minLength above maxLength")
	}
	if p, present := m["pattern"]; present {
		s, ok := p.(string)
		if !ok || !strings.HasPrefix(s, "^") || !strings.HasSuffix(s, "$") {
			return text{}, errors.New("a pattern is anchored with ^ and $")
		}
		if t.pattern, err = regexp.Compile(s); err != nil {
			return text{}, fmt.Errorf("pattern: %w", err)
		}
	}
	if e, present := m["enum"]; present {
		list, ok := e.([]any)
		if !ok || len(list) == 0 {
			return text{}, errors.New("enum is a list of strings")
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return text{}, errors.New("enum holds strings")
			}
			t.values = append(t.values, s)
		}
	}
	if f, present := m["format"]; present {
		s, _ := f.(string)
		switch s {
		case "uri", "host-port", "path", "duration":
			t.format = s
		default:
			// The agent ignores a format that is no string; so does the server.
			if _, isString := f.(string); isString {
				return text{}, fmt.Errorf("format %s is not in the subset", s)
			}
		}
	}
	return t, nil
}

// Raw returns the schema as written (for the UI's form).
func (s *Schema) Raw() string { return s.raw }

// Has reports whether the schema has the property.
func (s *Schema) Has(name string) bool {
	_, ok := s.properties[name]
	return ok
}

// Secrets returns the properties marked writeOnly, sorted: the module's credentials.
func (s *Schema) Secrets() []string {
	var out []string
	for _, name := range slices.Sorted(maps.Keys(s.properties)) {
		if s.properties[name].secret {
			out = append(out, name)
		}
	}
	return out
}

// IsSecret reports whether name is a credential of the module.
func (s *Schema) IsSecret(name string) bool { return s.properties[name].secret }

// Validate checks a configuration (JSON text, empty for none) and fills in the defaults; secrets
// are the credentials that have a value (a required one must be among them).
func (s *Schema) Validate(raw string, secrets []string) (map[string]any, error) {
	config, err := s.check(raw, secrets)
	if err != nil {
		return nil, err
	}
	for name, p := range s.properties {
		if _, given := config[name]; p.hasDefault && !given {
			config[name] = p.defaultV
		}
	}
	return config, nil
}

// Normalize checks a configuration like Validate and returns it compact, without the defaults:
// they come from the agent's schema when it runs.
func (s *Schema) Normalize(raw string, secrets []string) (string, error) {
	config, err := s.check(raw, secrets)
	if err != nil {
		return "", err
	}
	return compact(config)
}

// NormalizePart checks the properties of a part of a configuration (a suggestion of the agent)
// without asking for the required ones.
func (s *Schema) NormalizePart(raw string) (string, error) {
	config, err := s.checkProperties(raw)
	if err != nil {
		return "", err
	}
	return compact(config)
}

func compact(config map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(config); err != nil {
		return "", fmt.Errorf("encode configuration: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

func (s *Schema) check(raw string, secrets []string) (map[string]any, error) {
	config, err := s.checkProperties(raw)
	if err != nil {
		return nil, err
	}
	for _, name := range s.required {
		given := false
		if s.properties[name].secret {
			given = slices.Contains(secrets, name)
		} else {
			_, given = config[name]
		}
		if !given {
			return nil, fmt.Errorf("%s: required", name)
		}
	}
	return config, nil
}

func (s *Schema) checkProperties(raw string) (map[string]any, error) {
	var v any = map[string]any{}
	if strings.TrimSpace(raw) != "" {
		var err error
		if v, err = decode(raw); err != nil {
			return nil, err
		}
	}
	config, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("the configuration is no object")
	}
	for _, name := range slices.Sorted(maps.Keys(config)) {
		p, known := s.properties[name]
		if !known {
			return nil, fmt.Errorf("%s: unknown property", name)
		}
		if p.secret {
			return nil, fmt.Errorf("%s: a credential doesn't belong in the configuration", name)
		}
		if err := check(p, config[name]); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	return config, nil
}

func check(p property, v any) error {
	switch p.kind {
	case kindString:
		str, ok := v.(string)
		if !ok {
			return errors.New("not a string")
		}
		return checkText(p.text, str)
	case kindInteger:
		n, ok := asInt(v)
		if !ok {
			return errors.New("not an integer")
		}
		if n < p.imin || n > p.imax {
			return fmt.Errorf("%d is not within %d to %d", n, p.imin, p.imax)
		}
	case kindNumber:
		n, ok := asFloat(v)
		if !ok {
			return errors.New("not a number")
		}
		if n < p.fmin || n > p.fmax {
			return fmt.Errorf("%s is not within %s to %s", num(n), num(p.fmin), num(p.fmax))
		}
	case kindBoolean:
		if _, ok := v.(bool); !ok {
			return errors.New("not a boolean")
		}
	case kindStrings, kindIntegers:
		return checkItems(p, v)
	}
	return nil
}

func checkItems(p property, v any) error {
	items, ok := v.([]any)
	if !ok {
		return errors.New("not a list")
	}
	if len(items) > p.maxItems {
		return fmt.Errorf("more than %d items", p.maxItems)
	}
	for i, item := range items {
		if p.kind == kindStrings {
			str, ok := item.(string)
			if !ok {
				return errors.New("not a string")
			}
			if err := checkText(p.text, str); err != nil {
				return fmt.Errorf("item %d: %w", i+1, err)
			}
			continue
		}
		n, ok := asInt(item)
		if !ok {
			return errors.New("not an integer")
		}
		if n < p.imin || n > p.imax {
			return fmt.Errorf("item %d: %d is not within %d to %d", i+1, n, p.imin, p.imax)
		}
	}
	return nil
}

// num formats like Rust's Display of f64 (1, 0.5, 1e21 written out).
func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func checkText(t text, s string) error {
	n := utf8.RuneCountInString(s)
	if n < t.minLen || n > t.maxLen {
		return fmt.Errorf("length %d is not within %d to %d", n, t.minLen, t.maxLen)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return errors.New("contains control characters")
	}
	if t.values != nil && !slices.Contains(t.values, s) {
		return fmt.Errorf("%q is not one of %s", s, strings.Join(t.values, ", "))
	}
	if t.pattern != nil && !t.pattern.MatchString(s) {
		return errors.New("doesn't match the pattern")
	}
	switch t.format {
	case "uri":
		return uri(s)
	case "host-port":
		_, _, err := HostPort(s)
		return err
	case "path":
		if !strings.HasPrefix(s, "/") || slices.Contains(strings.Split(s, "/"), "..") {
			return errors.New("an absolute path without .. segments")
		}
	case "duration":
		return duration(s)
	}
	return nil
}

func uri(s string) error {
	rest, ok := strings.CutPrefix(s, "http://")
	if !ok {
		if rest, ok = strings.CutPrefix(s, "https://"); !ok {
			return errors.New("an http or https URL")
		}
	}
	authority := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority = rest[:i]
	}
	if strings.Contains(authority, "@") {
		return errors.New("a URL without user and password")
	}
	if authority == "" || strings.Contains(s, " ") {
		return errors.New("not a URL")
	}
	return nil
}

// HostPort splits host:port ([::1]:5432 for IPv6) as the agent does.
func HostPort(s string) (string, uint16, error) {
	var host, port string
	if rest, ok := strings.CutPrefix(s, "["); ok {
		var found bool
		if host, port, found = strings.Cut(rest, "]:"); !found {
			return "", 0, errors.New("not [address]:port")
		}
	} else {
		i := strings.LastIndexByte(s, ':')
		if i < 0 {
			return "", 0, errors.New("not host:port")
		}
		host, port = s[:i], s[i+1:]
	}
	// Rust's integer parsing takes one leading +.
	p, err := strconv.ParseUint(strings.TrimPrefix(port, "+"), 10, 16)
	if err != nil || p == 0 {
		return "", 0, errors.New("the port is 1 to 65535")
	}
	if host == "" || strings.ContainsFunc(host, func(r rune) bool {
		return r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(".-:", r))
	}) {
		return "", 0, errors.New("not a host")
	}
	return host, uint16(p), nil
}

func duration(s string) error {
	split := strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' })
	if split < 0 {
		return errors.New("a number and a unit (ms, s, m, h)")
	}
	n, err := strconv.ParseUint(s[:split], 10, 64)
	if err != nil {
		return errors.New("not a number")
	}
	var factor uint64
	switch s[split:] {
	case "ms":
		factor = 1
	case "s":
		factor = 1000
	case "m":
		factor = 60_000
	case "h":
		factor = 3_600_000
	default:
		return errors.New("the unit is ms, s, m or h")
	}
	if n > maxDurationMS/factor {
		return errors.New("at most an hour")
	}
	return nil
}
