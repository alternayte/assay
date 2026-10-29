// Package testkit gives integration tests a real PostgreSQL.
package testkit

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/auth"
)

// PostgresImage is the image that tests and Compose use.
const PostgresImage = "pgvector/pgvector:pg17"

var (
	once    sync.Once
	baseURL string
	errBoot error
	counter atomic.Int64
)

// server starts one container for the test process.
func server(t testing.TB) string {
	t.Helper()
	once.Do(func() {
		ctx := context.Background()
		c, err := tcpostgres.Run(ctx, PostgresImage,
			tcpostgres.WithDatabase("assay"),
			tcpostgres.WithUsername("assay"),
			tcpostgres.WithPassword("assay"),
			tcpostgres.BasicWaitStrategies(),
			testcontainers.WithCmdArgs("-c", "max_connections=500", "-c", "fsync=off"),
		)
		if err != nil {
			errBoot = err
			return
		}
		baseURL, errBoot = c.ConnectionString(ctx, "sslmode=disable")
	})
	if errBoot != nil {
		t.Fatalf("testkit: start PostgreSQL: %v", errBoot)
	}
	return baseURL
}

// EmptyDB creates an empty database for the test and returns its URL.
func EmptyDB(t testing.TB) string {
	t.Helper()
	name := fmt.Sprintf("t%d_%d", time.Now().UnixNano(), counter.Add(1))
	return createDB(t, name, "")
}

func createDB(t testing.TB, name, template string) string {
	t.Helper()
	admin := server(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testkit: connect: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if template != "" {
		stmt += " TEMPLATE " + pgx.Identifier{template}.Sanitize()
	}
	if _, err := conn.Exec(ctx, stmt); err != nil {
		t.Fatalf("testkit: %s: %v", stmt, err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

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
		tmplName = fmt.Sprintf("tmpl%d", time.Now().UnixNano())
		u := createDB(t, tmplName, "")
		tmplErr = migrate(context.Background(), u)
	})
	if tmplErr != nil {
		t.Fatalf("testkit: migrate the template: %v", tmplErr)
	}
	name := fmt.Sprintf("t%d_%d", time.Now().UnixNano(), counter.Add(1))
	return createDB(t, name, tmplName)
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
