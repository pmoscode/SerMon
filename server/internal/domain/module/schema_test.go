package module_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pmoscode/SerMon/server/internal/domain/module"
)

// The test vectors the agent's checker runs too (ADR 0039): both must agree on every case.
var vectorsPath = filepath.Join("..", "..", "..", "..", "agent", "crates", "sermon-modules", "testdata", "schema-vectors.json")

type vectors struct {
	Schema      map[string]any `json:"schema"`
	SchemaCases []struct {
		Pointer string `json:"pointer"`
		Value   any    `json:"value"`
		Remove  bool   `json:"remove"`
		Error   string `json:"error"`
	} `json:"schema_cases"`
	ConfigCases []struct {
		Config   string         `json:"config"`
		Required []string       `json:"required"`
		Secrets  []string       `json:"secrets"`
		Error    string         `json:"error"`
		Result   map[string]any `json:"result"`
	} `json:"config_cases"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.SchemaCases) < 20 || len(v.ConfigCases) < 20 {
		t.Fatalf("%d schema and %d config cases", len(v.SchemaCases), len(v.ConfigCases))
	}
	return v
}

// clone copies a decoded JSON document.
func clone(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(v)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// patch sets (or removes) the value at a JSON pointer of objects.
func patch(root map[string]any, pointer string, value any, remove bool) {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	node := root
	for _, p := range parts[:len(parts)-1] {
		node, _ = node[p].(map[string]any)
	}
	key := parts[len(parts)-1]
	if remove {
		delete(node, key)
	} else {
		node[key] = value
	}
}

func text(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSchema_SharedSchemaCases(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.SchemaCases {
		schema := clone(t, v.Schema)
		patch(schema, c.Pointer, c.Value, c.Remove)
		_, err := module.ParseSchema(text(t, schema))
		switch {
		case c.Error == "" && err != nil:
			t.Errorf("%s: %v", c.Pointer, err)
		case c.Error != "" && (err == nil || !strings.Contains(err.Error(), c.Error)):
			t.Errorf("%s: error %v, want %q", c.Pointer, err, c.Error)
		}
	}
}

func TestSchema_SharedConfigurationCases(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.ConfigCases {
		schema := clone(t, v.Schema)
		if c.Required != nil {
			schema["required"] = c.Required
		}
		s, err := module.ParseSchema(text(t, schema))
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Validate(c.Config, c.Secrets)
		if c.Error != "" {
			if err == nil || !strings.Contains(err.Error(), c.Error) {
				t.Errorf("%s: error %v, want %q", c.Config, err, c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Config, err)
			continue
		}
		var want, have map[string]any
		_ = json.Unmarshal([]byte(text(t, c.Result)), &want)
		_ = json.Unmarshal([]byte(text(t, got)), &have)
		if !reflect.DeepEqual(have, want) {
			t.Errorf("%s: %v, want %v", c.Config, have, want)
		}
	}
}

func TestSchema_SecretsAndProperties(t *testing.T) {
	v := loadVectors(t)
	s, err := module.ParseSchema(text(t, v.Schema))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Secrets(); !reflect.DeepEqual(got, []string{"password"}) {
		t.Errorf("Secrets() = %v", got)
	}
	if !s.Has("port") || s.Has("nope") {
		t.Error("Has")
	}
}

// Configurations are stored without the defaults (a newer agent may have others) and compact.
func TestSchema_NormalizeKeepsWhatWasGiven(t *testing.T) {
	v := loadVectors(t)
	s, _ := module.ParseSchema(text(t, v.Schema))
	got, err := s.Normalize(" {\"url\": \"http://x/\",  \"port\": 80} ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"port":80,"url":"http://x/"}` {
		t.Errorf("Normalize() = %s", got)
	}
	if _, err := s.Normalize("", nil); err == nil || err.Error() != "url: required" {
		t.Errorf("empty: %v", err)
	}
	if _, err := s.Normalize(`{"url":"http://x/"} {}`, nil); err == nil {
		t.Error("trailing data accepted")
	}
}

func FuzzSchema_Validate(f *testing.F) {
	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		f.Fatal(err)
	}
	var v struct {
		Schema json.RawMessage `json:"schema"`
	}
	_ = json.Unmarshal(raw, &v)
	s, err := module.ParseSchema(string(v.Schema))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(`{"url":"http://x/"}`)
	f.Add(`{"codes":[1,2,3]}`)
	f.Fuzz(func(_ *testing.T, config string) {
		_, _ = s.Validate(config, []string{"password"})
		_, _ = module.ParseSchema(config)
	})
}
