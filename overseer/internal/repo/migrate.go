package repo

import (
	"fmt"
	"io/fs"
	"net/url"

	"github.com/amacneil/dbmate/v2/pkg/dbmate"
	_ "github.com/amacneil/dbmate/v2/pkg/driver/postgres"
)

// Migrate applies pending dbmate migrations at startup so a deploy can never
// run against a stale schema. The same files run with the dbmate CLI
// (`dbmate up` from overseer/, default dir ./db/migrations).
func Migrate(databaseURL string, migrations fs.FS) error {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	db := dbmate.New(u)
	db.FS = migrations
	db.MigrationsDir = []string{"db/migrations"}
	db.AutoDumpSchema = false
	if err := db.CreateAndMigrate(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
