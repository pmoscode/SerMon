package metric

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
)

// The metrics of the agent's modules (ADR 0039) come from one file per module,
// agent/crates/sermon-modules/metrics/<id>.json, which `just gen` copies here: a module adds its
// metrics there, next to its code, and the server knows them from the next build. Each is named
// sermon_<id>_…, in the area of the modules; a broken file leaves out that module's metrics and
// fails the catalog's test.
//
//go:embed modules
var moduleFiles embed.FS

var (
	moduleID     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	metricName   = regexp.MustCompile(`^sermon_[a-z0-9_]{1,120}$`)
	labelName    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	moduleUnits  = []Unit{UnitRatioPPM, UnitBytes, UnitBytesPerSecond, UnitBitsPerSecond, UnitPerSecond, UnitPagesPerSecond, UnitCount, UnitSeconds, UnitTimestamp, UnitCelsius, UnitLoad, UnitBoolean}
	moduleAggs   = []Aggregation{Avg, Max, Last}
	maxModLabels = 8
)

// moduleFile is the format of a module's catalog file.
type moduleFile struct {
	Metrics []struct {
		Name        string   `json:"name"`
		Unit        Unit     `json:"unit"`
		Aggregation string   `json:"aggregation"`
		Labels      []string `json:"labels"`
	} `json:"metrics"`
}

// ParseModuleCatalog reads the catalog file of a module (named <id>.json).
func ParseModuleCatalog(file string, content []byte) ([]Definition, error) {
	id, ok := strings.CutSuffix(file, ".json")
	if !ok || !moduleID.MatchString(id) {
		return nil, fmt.Errorf("%s: not a module id", file)
	}
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()
	var f moduleFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	out := make([]Definition, 0, len(f.Metrics))
	for _, m := range f.Metrics {
		switch {
		case !metricName.MatchString(m.Name) || !strings.HasPrefix(m.Name, "sermon_"+id+"_"):
			return nil, fmt.Errorf("%s: %s is not named sermon_%s_…", file, m.Name, id)
		case !slices.Contains(moduleUnits, m.Unit):
			return nil, fmt.Errorf("%s: %s: unit %q", file, m.Name, m.Unit)
		case !slices.Contains(moduleAggs, Aggregation(m.Aggregation)):
			return nil, fmt.Errorf("%s: %s: aggregation %q", file, m.Name, m.Aggregation)
		case len(m.Labels) > maxModLabels:
			return nil, fmt.Errorf("%s: %s: more than %d labels", file, m.Name, maxModLabels)
		case slices.ContainsFunc(out, func(d Definition) bool { return d.Name == m.Name }):
			return nil, fmt.Errorf("%s: %s listed twice", file, m.Name)
		}
		for _, l := range m.Labels {
			if !labelName.MatchString(l) {
				return nil, fmt.Errorf("%s: %s: label %q", file, m.Name, l)
			}
		}
		d := def(m.Name, m.Unit, Aggregation(m.Aggregation), AreaModule, slices.Clone(m.Labels)...)
		d.Module = id
		out = append(out, d)
	}
	return out, nil
}

var moduleDefinitions, moduleErrors = loadModules(moduleFiles)

func loadModules(files fs.FS) ([]Definition, []error) {
	entries, err := fs.ReadDir(files, "modules")
	if err != nil {
		return nil, []error{err}
	}
	var defs []Definition
	var errs []error
	for _, e := range entries {
		if e.IsDir() || e.Name() == ".gitkeep" {
			continue
		}
		content, err := fs.ReadFile(files, "modules/"+e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		parsed, err := ParseModuleCatalog(e.Name(), content)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		defs = append(defs, parsed...)
	}
	return defs, errs
}

// ModuleErrors returns what was wrong with the modules' catalog files (for the test).
func ModuleErrors() []error {
	errs := slices.Clone(moduleErrors)
	for _, d := range moduleDefinitions {
		if base := slices.IndexFunc(definitions, func(o Definition) bool { return o.Name == d.Name && o.Module != d.Module }); base >= 0 {
			errs = append(errs, errors.New(d.Name+": defined twice"))
		}
	}
	return errs
}
