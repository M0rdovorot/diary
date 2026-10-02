// Package migrations содержит SQL-миграции, вшитые в бинарник.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
