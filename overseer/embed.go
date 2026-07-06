// Package overseer holds module-root assets. The dbmate migrations are
// embedded here so the binary can apply them at startup while the same files
// stay runnable with the dbmate CLI (default dir: ./db/migrations).
package overseer

import "embed"

//go:embed db/migrations/*.sql
var MigrationsFS embed.FS
