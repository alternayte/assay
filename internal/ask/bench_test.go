//go:build bench

package ask_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
	"github.com/alternayte/assay/internal/visibility"
)

// TestSCN_ASK_007_RetrievalP95 runs retrieval and context build for 200
// questions on 50,000 chunks with vectors, with the fake endpoint.
func TestSCN_ASK_007_RetrievalP95(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	ctx := t.Context()
	env.BenchCorpus(t, "owner@acme.test", 5000, 10)
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "embed", testkit.Function{Endpoint: ep, Model: "fake-embed-64"})
	space, err := env.Server.Models().TargetSpace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Generated vectors: the bench measures the query, not the embed job.
	if _, err := env.Server.Pool().Exec(ctx, fmt.Sprintf(`INSERT INTO emb_%d (ref_kind, ref_id, connection_id, chunk_id, embedding)
SELECT 'chunk', c.id, c.connection_id, c.id, (SELECT array_agg(sin(c.id * k + k))::vector FROM generate_series(1, 64) k)
FROM chunks c`, space.ID)); err != nil {
		t.Fatal(err)
	}
	if err := env.Server.Models().ActivateSpace(ctx, space.ID); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := env.Server.Pool().QueryRow(ctx, "SELECT id FROM auth_users WHERE email = 'owner@acme.test'").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	vis, err := visibility.For(ctx, env.Server.Pool(), owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Server.Ask().Build(ctx, vis, "warm up the payments ledger"); err != nil {
		t.Fatal(err)
	}
	var ds []time.Duration
	full := 0
	for i := range 200 {
		q := "Which " + testkit.BenchQuery(i) + " notes describe the owner?"
		start := time.Now()
		c, err := env.Server.Ask().Build(ctx, vis, q)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Messages(q)
		ds = append(ds, time.Since(start))
		if len(c.Sources) == 10 && !c.Degraded {
			full++
		}
	}
	slices.Sort(ds)
	p := ds[189]
	pass := p <= 800*time.Millisecond && full == 200
	testkit.BenchReport("SCN-ASK-007", pass, p.Milliseconds(), map[string]any{"questions": len(ds), "fullContexts": full})
	t.Logf("p95 %v over %d questions, %d full contexts", p, len(ds), full)
	if !pass {
		t.Fatalf("p95 %v, %d full contexts; want 800 ms or less and 200", p, full)
	}
}
