// migrations/embed.go
package migrations

import "embed"

//go:embed sql/*.sql
var FS embed.FS
