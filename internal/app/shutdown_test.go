package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/testdb"
)

type blockArgs struct{}

func (blockArgs) Kind() string { return "test_block" }

type blockWorker struct {
	river.WorkerDefaults[blockArgs]
	started chan struct{}
}

func (w *blockWorker) Work(ctx context.Context, _ *river.Job[blockArgs]) error {
	close(w.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestSCN_OPS_006_GracefulShutdown(t *testing.T) {
	cfg := Config{
		DatabaseURL: testdb.EmptyDB(t),
		SecretKey:   bytes.Repeat([]byte{7}, 32),
		BaseURL:     "http://localhost:8080",
		Listen:      "127.0.0.1:0",
		Role:        RoleAll,
	}
	w := &blockWorker{started: make(chan struct{})}
	s, err := New(context.Background(), cfg, NewLogger(io.Discard, 0), Options{
		workers:  func(ws *river.Workers) { river.AddWorker(ws, w) },
		jobGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	inner := s.handler
	reached := make(chan struct{})
	s.handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(reached)
			time.Sleep(2 * time.Second)
			_, _ = rw.Write([]byte("done"))
			return
		}
		inner.ServeHTTP(rw, r)
	})
	s.addr = make(chan string, 1)

	ctx, stop := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(ctx) }()
	addr := <-s.addr

	job, err := s.river.Insert(context.Background(), blockArgs{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.started:
	case <-time.After(15 * time.Second):
		t.Fatal("the job did not start")
	}

	type result struct {
		body string
		err  error
	}
	slow := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err != nil {
			slow <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		slow <- result{body: string(b), err: err}
	}()
	<-reached

	start := time.Now()
	stop()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the process did not stop within 30 seconds")
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	if r := <-slow; r.err != nil || r.body != "done" {
		t.Fatalf("the open request did not complete: %q %v", r.body, r.err)
	}
	var state string
	if err := s.pool.QueryRow(context.Background(), "SELECT state::text FROM river_job WHERE id = $1", job.Job.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "retryable" && state != "available" {
		t.Fatalf("the job is %s, want it released to the queue", state)
	}
	if resp, err := http.Get("http://" + addr + "/healthz"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the server still accepts requests")
	}
}
