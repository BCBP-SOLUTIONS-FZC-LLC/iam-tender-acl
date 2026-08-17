package main

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/postgres"
)

// runMigrations applies every pending migration in
// internal/adapter/outbound/postgres/migrations against migrationDSN, which must be a
// role with BYPASSRLS (tender_acl_migrator — LLD §7.4), never the runtime
// app role. Migrations are additive-only, safe to run at pod startup
// during a rolling deploy (LLD §16).
func runMigrations(migrationDSN string) error {
	src, err := iofs.New(postgres.MigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, migrationDSN)
	if err != nil {
		return fmt.Errorf("build migrator: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
