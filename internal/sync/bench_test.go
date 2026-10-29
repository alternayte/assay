//go:build bench

package sync_test

import (
	"testing"
	"time"

	"github.com/alternayte/assay/internal/testkit"
)

// TestSCN_SYN_011_FirstSyncRate measures the first sync of 2,000 fixture
// Confluence pages with no rate limit.
func TestSCN_SYN_011_FirstSyncRate(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	src.Bulk("BLK", 2000)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "bulk", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"BLK"}}})
	if _, err := env.Server.Pool().Exec(t.Context(), "UPDATE connections SET rate_per_second = 100000, rate_concurrency = 8 WHERE id = $1", conn); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	run := env.RunNow(admin, conn, "sync")
	d := time.Since(start)
	rate := float64(run.Created) / d.Seconds()
	pass := run.Status == "succeeded" && run.Created == 2000 && rate >= 20
	testkit.BenchReport("SCN-SYN-011", pass, d.Milliseconds(), map[string]any{"pagesPerSecond": rate})
	t.Logf("%d pages in %v: %.1f pages per second", run.Created, d, rate)
	if !pass {
		t.Fatalf("run %+v at %.1f pages per second; want 2000 at 20 or more", run.Status, rate)
	}
}
