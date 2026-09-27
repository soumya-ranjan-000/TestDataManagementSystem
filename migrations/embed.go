// Package migrations embeds the schema's .sql files into the binary, so
// `tdms serve` applies them no matter which directory it runs from.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
