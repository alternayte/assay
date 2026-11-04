//go:build bench

package search_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// countTracer counts the statements that count rows.
type countTracer struct{ counts atomic.Int64 }

func (c *countTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "count(*)") {
		c.counts.Add(1)
	}
	return ctx
}

func (*countTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TestSCN_SRC_005_EmbedLoopRate embeds 6,400 chunks in 4 shards with the
// fake model, and counts the count(*) statements of the loop: one exact
// count at the start of shard 0 and one at activation, none between
// batches.
func TestSCN_SRC_005_EmbedLoopRate(t *testing.T) {
	db := testkit.DB(t)
	env := testkit.StartOn(t, db)
	admin := env.Setup("owner@acme.test")
	benchCorpus(t, env, "owner@acme.test", 640, 10)
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "embed", testkit.Function{Endpoint: ep, Model: "fake-embed-64"})

	cfg, err := pgxpool.ParseConfig(db)
	if err != nil {
		t.Fatal(err)
	}
	tr := &countTracer{}
	cfg.ConnConfig.Tracer = tr
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	e := &content.Embedder{Pool: pool, Models: env.Server.Models(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Shards: 4}

	start := time.Now()
	if err := e.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	chunks := env.Count("SELECT count(*) FROM chunks")
	rate := float64(chunks) / d.Seconds()
	counts := tr.counts.Load()
	pass := counts == 2 && env.Count("SELECT count(*) FROM embedding_spaces WHERE status = 'active'") == 1
	testkit.BenchReport("SCN-SRC-005", pass, d.Milliseconds(), map[string]any{"chunksPerSecond": rate, "countStatements": counts})
	t.Logf("%d chunks in %v: %.0f per second, %d count statements", chunks, d, rate, counts)
	if !pass {
		t.Fatalf("%d count statements for %d batches, want 2", counts, chunks/content.EmbedBatch)
	}
}
