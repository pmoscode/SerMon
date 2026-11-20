package ruleset

import (
	"embed"
	"fmt"
	"io/fs"
)

// defaultFiles are the shipped rules: a copy of rules/ made by `just gen-rules`.
//
//go:embed defaults
var defaultFiles embed.FS

// Defaults returns the shipped default rules.
func Defaults() ([]Definition, error) {
	sub, err := fs.Sub(defaultFiles, "defaults")
	if err != nil {
		return nil, fmt.Errorf("default rules: %w", err)
	}
	return Load(sub)
}
