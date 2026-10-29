//go:build bench

package search_test

import (
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/testkit"
)

func p95(ds []time.Duration) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[(len(s)*95+99)/100-1]
}

// TestSCN_SRC_004_SearchP95 runs 200 queries on 50,000 chunks without
// model calls.
func TestSCN_SRC_004_SearchP95(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	env.BenchCorpus(t, "owner@acme.test", 5000, 10)
	if n := env.Count("SELECT count(*) FROM chunks"); n != 50000 {
		t.Fatalf("%d chunks", n)
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/search?q=warmup", nil)
	var ds []time.Duration
	found := 0
	for i := range 200 {
		start := time.Now()
		var out struct {
			Results []struct{} `json:"results"`
		}
		admin.Must(http.StatusOK, http.MethodGet, "/api/v1/search?q="+url.QueryEscape(testkit.BenchQuery(i)), nil).JSON(t, &out)
		ds = append(ds, time.Since(start))
		if len(out.Results) > 0 {
			found++
		}
	}
	p := p95(ds)
	pass := p <= 500*time.Millisecond && found > 150
	testkit.BenchReport("SCN-SRC-004", pass, p.Milliseconds(), map[string]any{"queries": len(ds), "withResults": found})
	t.Logf("p95 %v over %d queries, %d with results", p, len(ds), found)
	if !pass {
		t.Fatalf("p95 %v, %d queries with results; want 500 ms or less", p, found)
	}
}
