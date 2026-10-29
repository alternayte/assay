// Package testdb starts one PostgreSQL container per test process and gives
// each test its own database.
package testdb

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
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
	return Create(t, Name(), "")
}

// Name returns a database name that is unique across test processes.
func Name() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("t%d_%x", counter.Add(1), b)
}

// Create creates a database, optionally from a template, and returns its URL.
func Create(t testing.TB, name, template string) string {
	t.Helper()
	admin := server(t)
	ctx := context.Background()
	conn, err := connect(ctx, admin)
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

// connect connects with a timeout per attempt. While many test processes
// start their containers, a connection can stall in the startup handshake;
// a new attempt then succeeds.
func connect(ctx context.Context, url string) (*pgx.Conn, error) {
	var err error
	for range 5 {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		var conn *pgx.Conn
		conn, err = pgx.Connect(c, url)
		cancel()
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}
