package migrations

import (
	"embed"
	"io/fs"
)

// Files contains the ordered, forward-only PostgreSQL migrations.
//
//go:embed *.sql
var Files embed.FS

func Names() ([]string, error) {
	return fs.Glob(Files, "*.sql")
}
