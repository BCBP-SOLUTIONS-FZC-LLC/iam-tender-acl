package postgres

import (
	"context"
	"io/fs"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/domain"
	pgmigrate "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/migrate"
)

// RunMigrations applies every pending migration embedded in MigrationsFS
// against dsn, which must be a role with BYPASSRLS (tender_acl_migrator —
// LLD §7.4), never the runtime app role. Migrations are additive-only, safe
// to run at pod startup during a rolling deploy (LLD §16).
//
// Uses platform-pgcommon's own migrate.Runner instead of calling
// golang-migrate/v4 directly, matching iam-org-membership's and
// iam-catalog-admin's identical RunMigrations. logger is optional — pass
// nil to run silently (as tests do); the composition root passes a
// domain.Logger adapter so each applied migration step is logged through
// the service's own structured logger, instead of nowhere.
func RunMigrations(ctx context.Context, dsn string, logger domain.Logger) error {
	// fs.Sub on an embedded FS with a known directory path is infallible;
	// an error here would be a build-time programming mistake.
	sub, _ := fs.Sub(MigrationsFS, "migrations") //nolint:errcheck // see comment above
	return (&pgmigrate.Runner{FS: sub, DSN: dsn, Logger: logger}).Up(ctx)
}
