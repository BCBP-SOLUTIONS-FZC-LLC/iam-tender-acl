// Package testutil provides shared Testcontainers-based fixtures for the
// integration, RLS, and e2e suites under test/. It is not part of the
// production build. Every Start* function returns an explicit teardown
// function rather than registering via t.Cleanup, so it can be used from
// TestMain (which has no *testing.T) to share one container across an
// entire test binary. Mirrors iam-group-mapping's test/testutil/postgres.go.
package testutil

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	// Registers the "postgres" database/sql driver used throughout this
	// file.
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/outbound/postgres"
)

// PostgresFixture is a running PostgreSQL container with every
// internal/adapter/outbound/postgres/migrations migration already applied, exposing both
// the superuser ("admin", BYPASSRLS-equivalent) DSN used for out-of-band
// setup/verification and the tender_acl_app ("app", RLS-bound) DSN that
// production code and RLS tests must use.
type PostgresFixture struct {
	AdminDSN string
	AppDSN   string
}

// StartPostgres launches a postgres:16-alpine container and applies every
// embedded migration in internal/adapter/outbound/postgres.MigrationsFS. The returned
// teardown function must be called (typically via defer in TestMain) to
// terminate the container.
func StartPostgres(ctx context.Context) (*PostgresFixture, func(), error) {
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "tender_acl",
			"POSTGRES_USER":     "tender_acl",
			"POSTGRES_PASSWORD": "tender_acl",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("start postgres container: %w", err)
	}
	// Deliberately uses a fresh background context rather than the ctx
	// passed to StartPostgres: teardown typically runs from a deferred
	// TestMain call after that ctx's deadline (or the whole test run) has
	// already elapsed, and termination must still be attempted.
	teardown := func() { //nolint:contextcheck // see comment above
		if termErr := container.Terminate(context.Background()); termErr != nil {
			fmt.Fprintln(os.Stderr, "testutil: terminate postgres container:", termErr)
		}
	}

	host, err := container.Host(ctx)
	if err != nil {
		teardown()
		return nil, nil, fmt.Errorf("get postgres host: %w", err)
	}
	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		teardown()
		return nil, nil, fmt.Errorf("get postgres port: %w", err)
	}

	adminDSN := fmt.Sprintf("postgres://tender_acl:tender_acl@%s:%s/tender_acl?sslmode=disable", host, port.Port())
	if err := waitForPing(ctx, adminDSN); err != nil {
		teardown()
		return nil, nil, fmt.Errorf("wait for postgres readiness: %w", err)
	}

	if err := applyMigrations(adminDSN); err != nil {
		teardown()
		return nil, nil, fmt.Errorf("apply migrations: %w", err)
	}

	appDSN := fmt.Sprintf("postgres://tender_acl_app:tender_acl_app_dev_password@%s:%s/tender_acl?sslmode=disable", host, port.Port())

	return &PostgresFixture{AdminDSN: adminDSN, AppDSN: appDSN}, teardown, nil
}

func waitForPing(ctx context.Context, dsn string) error {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", dsn)
		if err == nil {
			lastErr = db.PingContext(ctx)
			if closeErr := db.Close(); closeErr != nil {
				fmt.Fprintln(os.Stderr, "testutil: close ping connection:", closeErr)
			}
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("postgres never became ready: %w", lastErr)
}

// applyMigrations runs the same embedded migration set the production
// binary self-migrates with (cmd/tender-acl/migrate.go), so the test
// fixture can never drift from what actually ships.
func applyMigrations(dsn string) error {
	src, err := iofs.New(postgres.MigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open db for migrations: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			fmt.Fprintln(os.Stderr, "testutil: close migration connection:", closeErr)
		}
	}()

	driver, err := migratepg.WithInstance(db, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("build migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return fmt.Errorf("build migrator: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
