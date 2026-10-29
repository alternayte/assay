package testkit

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/testdb"
)

// EmptyDB creates an empty database for the test and returns its URL.
func EmptyDB(t testing.TB) string { return testdb.EmptyDB(t) }

var (
	tmplOnce sync.Once
	tmplName string
	tmplErr  error
)

// DB creates a migrated database for the test and returns its URL. The
// migrations run once per test process into a template database.
func DB(t testing.TB) string {
	t.Helper()
	tmplOnce.Do(func() {
		tmplName = "tmpl" + testdb.Name()
		u := testdb.Create(t, tmplName, "")
		tmplErr = migrate(context.Background(), u)
	})
	if tmplErr != nil {
		t.Fatalf("testkit: migrate the template: %v", tmplErr)
	}
	return testdb.Create(t, testdb.Name(), tmplName)
}

func migrate(ctx context.Context, u string) error {
	pool, err := pgxpool.New(ctx, u)
	if err != nil {
		return err
	}
	defer pool.Close()
	log := slog.New(slog.DiscardHandler)
	a, err := auth.New(pool, "http://localhost:8080", log, auth.Options{})
	if err != nil {
		return err
	}
	return app.Migrate(ctx, pool, a, log)
}
