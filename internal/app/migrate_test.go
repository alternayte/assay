package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/testkit"
)

func testConfig(dbURL string) app.Config {
	return app.Config{
		DatabaseURL: dbURL,
		SecretKey:   bytes.Repeat([]byte{7}, 32),
		BaseURL:     "http://localhost:8080",
		Listen:      "127.0.0.1:0",
		Role:        app.RoleAll,
	}
}

func TestSCN_INS_004_ConcurrentStartMigratesOnce(t *testing.T) {
	ctx := context.Background()
	dbURL := testkit.EmptyDB(t)

	var logs [2]bytes.Buffer
	var servers [2]*app.Server
	var errs [2]error
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			log := slog.New(slog.NewJSONHandler(&logs[i], nil))
			servers[i], errs[i] = app.New(ctx, testConfig(dbURL), log, app.Options{})
		}()
	}
	wg.Wait()
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("server %d: %v", i, errs[i])
		}
		defer servers[i].Close()
	}

	applied := strings.Count(logs[0].String(), "migration applied") + strings.Count(logs[1].String(), "migration applied")
	first := strings.Count(logs[0].String(), "migration applied")
	second := strings.Count(logs[1].String(), "migration applied")
	if applied == 0 || (first != 0 && second != 0) {
		t.Fatalf("want exactly one server to apply migrations, got %d and %d", first, second)
	}

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, table := range []string{"goose_db_version", "authall_goose_db_version"} {
		var dup int
		q := "SELECT count(*) FROM (SELECT version_id FROM " + table + " GROUP BY version_id HAVING count(*) > 1) d"
		if err := conn.QueryRow(ctx, q).Scan(&dup); err != nil {
			t.Fatal(err)
		}
		if dup != 0 {
			t.Fatalf("%s has %d versions applied more than once", table, dup)
		}
	}

	for i, s := range servers {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("server %d: /readyz = %d %s", i, rec.Code, rec.Body)
		}
	}
}
