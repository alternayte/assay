package app

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"testing/fstest"

	"github.com/alternayte/auth-all/migrations"
	"github.com/alternayte/auth-all/schema"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/alternayte/assay/internal/auth"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLock is the advisory lock key that serializes migrations across
// processes.
const migrationLock int64 = 0x617373617901

const authallVersionTable = "authall_goose_db_version"

// Migrate applies the auth-all units, the Assay migrations and the River
// migrations under one advisory lock. A second process waits for the first
// and then finds nothing to apply.
func Migrate(ctx context.Context, pool *pgxpool.Pool, a *auth.Auth, log *slog.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return fmt.Errorf("migrate: take the lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLock)
	}()

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	authFS, err := authallMigrations(a)
	if err != nil {
		return err
	}
	sets := []struct {
		name  string
		fsys  fs.FS
		table string
	}{
		{"auth-all", authFS, authallVersionTable},
		{"assay", assayMigrations(), goose.DefaultTablename},
	}
	for _, set := range sets {
		p, err := goose.NewProvider(goose.DialectPostgres, db, set.fsys, goose.WithTableName(set.table))
		if err != nil {
			return fmt.Errorf("migrate %s: %w", set.name, err)
		}
		results, err := p.Up(ctx)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", set.name, err)
		}
		for _, r := range results {
			log.Info("migration applied", "set", set.name, "version", r.Source.Version, "ms", r.Duration.Milliseconds())
		}
	}

	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}
	res, err := m.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}
	for _, v := range res.Versions {
		log.Info("migration applied", "set", "river", "version", v.Version)
	}
	return nil
}

// MigrationsCurrent reports whether every Assay migration is applied.
func MigrationsCurrent(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	p, err := goose.NewProvider(goose.DialectPostgres, db, assayMigrations())
	if err != nil {
		return false, err
	}
	pending, err := p.HasPending(ctx)
	return !pending && err == nil, err
}

func assayMigrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

func authallMigrations(a *auth.Auth) (fs.FS, error) {
	files, err := a.ExportMigrations(schema.Postgres, migrations.Goose)
	if err != nil {
		return nil, fmt.Errorf("migrate auth-all: export: %w", err)
	}
	fsys := fstest.MapFS{}
	for _, f := range files {
		fsys[f.Name] = &fstest.MapFile{Data: []byte(f.Content)}
	}
	return fsys, nil
}
