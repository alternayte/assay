package app_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/testkit"
)

func runStatus(t *testing.T, env *testkit.Env, id int64) string {
	t.Helper()
	var s string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT status FROM sync_runs WHERE id = $1", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func start(t *testing.T, dbURL string, role app.Role) {
	t.Helper()
	cfg := testConfig(dbURL)
	cfg.Role = role
	s, err := app.New(context.Background(), cfg, app.NewLogger(io.Discard, 0), app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	addr := make(chan string, 1)
	go func() { done <- s.RunOn(ctx, addr) }()
	<-addr
	t.Cleanup(func() {
		cancel()
		<-done
		s.Close()
	})
}

func TestSCN_OPS_004_WebRunsNoJobWorkerSyncs(t *testing.T) {
	db := testkit.DB(t)
	env := testkit.StartOn(t, db)
	admin := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	run, err := env.Server.Sync().Queue(t.Context(), conn, "sync", "manual")
	if err != nil {
		t.Fatal(err)
	}

	start(t, db, app.RoleWeb)
	time.Sleep(3 * time.Second)
	if s := runStatus(t, env, run); s != "queued" {
		t.Fatalf("the web process worked the job: %s", s)
	}

	start(t, db, app.RoleWorker)
	deadline := time.Now().Add(30 * time.Second)
	for runStatus(t, env, run) != "succeeded" {
		if time.Now().After(deadline) {
			t.Fatalf("the worker did not finish the sync: %s", runStatus(t, env, run))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1", conn); n != 7 {
		t.Fatalf("the worker stored %d items", n)
	}
}
