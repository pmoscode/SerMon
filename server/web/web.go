// Package web embeds the built SvelteKit UI.
//
// `just build-web` builds web/ and copies the output into dist/. Without a build, dist/
// only contains .gitkeep and the server runs without UI (API and health endpoints only).
package web

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the built UI files with dist/ as root.
func Assets() (fs.FS, error) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded UI: %w", err)
	}
	return sub, nil
}
