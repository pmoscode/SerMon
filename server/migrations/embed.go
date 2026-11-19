// Package migrations embeds the SQL schema migrations (goose format), applied by sermon-server.
package migrations

import "embed"

// FS contains all migration files (NNNNN_name.sql).
//
//go:embed *.sql
var FS embed.FS
