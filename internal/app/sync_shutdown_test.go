package app_test

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/testkit"
)

// worker starts a worker process and returns its stop function, which
// waits for the process to end.
func worker(t *testing.T, dbURL string) func() {
	t.Helper()
	cfg := testConfig(dbURL)
	cfg.Role = app.RoleWorker
	s, err := app.New(context.Background(), cfg, app.NewLogger(io.Discard, 0), app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	addr := make(chan string, 1)
	go func() { done <- s.RunOn(ctx, addr) }()
	<-addr
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("worker: %v", err)
		}
		s.Close()
	}
	t.Cleanup(stop)
	return stop
}

func waitRun(t *testing.T, env *testkit.Env, id int64, want string, within time.Duration) {
	t.Helper()
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if runStatus(t, env, id) == want {
			return
		}
	}
	t.Fatalf("run %d is %s, want %s", id, runStatus(t, env, id), want)
}

func TestSCN_SYN_013_ShutdownRequeuesRunningSync(t *testing.T) {
	db := testkit.DB(t)
	env := testkit.StartOn(t, db)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	// The source asks the client to wait five minutes, so the run is still
	// working when the process stops.
	src.Fail("/confluence/rest/api/content/", http.StatusServiceUnavailable, 1, "300")
	run, err := env.Server.Sync().Queue(t.Context(), conn, "sync", "manual")
	if err != nil {
		t.Fatal(err)
	}

	// Syncs have their own queue, so that model jobs never hold them back.
	if n := env.Count("SELECT count(*) FROM river_job WHERE kind = 'sync_run' AND queue = 'sync'"); n != 1 {
		t.Fatalf("%d sync jobs on the sync queue, want 1", n)
	}

	stop := worker(t, db)
	waitRun(t, env, run, "running", 15*time.Second)
	// The run must have sent its first request, so that the fault is used
	// before the stop.
	for end := time.Now().Add(15 * time.Second); len(src.Hits()) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the run sent no request")
		}
	}
	stop()
	if s := runStatus(t, env, run); s != "queued" {
		t.Fatalf("after the shutdown the run is %s", s)
	}

	worker(t, db)
	waitRun(t, env, run, "succeeded", 30*time.Second)
}
