// Package migrations embeds the SQL schema migrations.
//
// Files are applied in lexical order and must never be modified once
// released: add a new numbered file instead.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
