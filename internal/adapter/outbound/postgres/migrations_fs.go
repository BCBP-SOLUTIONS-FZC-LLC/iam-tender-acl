package postgres

import "embed"

// MigrationsFS embeds internal/adapter/outbound/postgres/migrations for
// cmd/tender-acl's startup migration runner (LLD §7.4: migrations run via
// migrate.Runner at process startup, against the tender_acl_migrator
// role).
//
//go:embed migrations/*.sql
var MigrationsFS embed.FS
