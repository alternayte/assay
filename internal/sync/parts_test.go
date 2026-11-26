package sync_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	gosync "sync"
	"testing"

	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/testkit"
)

// partRow is one row of sync_parts.
type partRow struct {
	Part, Status, Host string
	Created            int
}

func partRows(t *testing.T, env *testkit.Env, run int64) []partRow {
	t.Helper()
	rows, err := env.Server.Pool().Query(t.Context(), "SELECT part, status, host, stats FROM sync_parts WHERE run_id = $1 ORDER BY part", run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []partRow
	for rows.Next() {
		var p partRow
		var stats []byte
		if err := rows.Scan(&p.Part, &p.Status, &p.Host, &stats); err != nil {
			t.Fatal(err)
		}
		var c struct {
			Created int `json:"created"`
		}
		_ = json.Unmarshal(stats, &c)
		p.Created = c.Created
		out = append(out, p)
	}
	return out
}

func runOf(t *testing.T, admin *testkit.Client, conn, run int64) testkit.Run {
	t.Helper()
	var runs struct {
		Runs []testkit.Run `json:"runs"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/runs", conn), nil).JSON(t, &runs)
	for _, r := range runs.Runs {
		if r.ID == run {
			return r
		}
	}
	t.Fatalf("run %d is not in the list", run)
	return testkit.Run{}
}

func TestSCN_SYN_018_PartsFanOutAndLastPartEndsRun(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	src.Bulk("OPS", 7)
	spaces := []string{"ENG", "OPS", "RST"}
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-wiki", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": spaces}})
	svc := env.Server.Sync()
	ctx := context.Background()
	run, err := svc.Queue(ctx, conn, "sync", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Split(ctx, run); err != nil {
		t.Fatal(err)
	}
	if n := env.Count("SELECT count(*) FROM river_job WHERE kind = 'sync_part' AND queue = 'sync' AND (args->>'run_id')::bigint = $1", run); n != 3 {
		t.Fatalf("%d part jobs, want 3", n)
	}
	parts := partRows(t, env, run)
	if len(parts) != 3 {
		t.Fatalf("parts %+v, want one per space", parts)
	}

	// The parts run at the same time, as on three workers. The last one
	// to end sets the status and the sums of the run.
	var wg gosync.WaitGroup
	errs := make(chan error, len(parts))
	for _, p := range parts {
		if p.Status != "queued" {
			t.Fatalf("part %+v before its job runs", p)
		}
		wg.Go(func() { errs <- svc.RunPart(ctx, run, p.Part) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	total, skipped := 0, 0
	for i, p := range partRows(t, env, run) {
		want := src.Count(p.Part) + src.AttachmentItems(p.Part)
		if p.Part != spaces[i] || p.Status != "succeeded" || p.Host == "" || p.Created != want {
			t.Fatalf("part %+v, want succeeded with %d new items", p, want)
		}
		total += p.Created
		skipped += src.AttachmentSkips(p.Part)
	}
	// The run also sees the attachments that it skips.
	r := runOf(t, admin, conn, run)
	if r.Status != "succeeded" || r.Created != total || r.Seen != total+skipped || len(r.Skipped) != skipped {
		t.Fatalf("run %+v, want succeeded with the %d new items of the parts", r, total)
	}
	if n := env.Count("SELECT count(*) FROM connection_cursors WHERE connection_id = $1 AND cursor <> ''", conn); n != 3 {
		t.Fatalf("%d part cursors, want 3", n)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND part = 'OPS'", conn); n != 7 {
		t.Fatalf("%d items in the part OPS, want 7", n)
	}

	// A part that fails makes the run fail. The other parts keep their
	// cursors.
	if _, err := env.Server.Pool().Exec(ctx, "UPDATE connection_cursors SET cursor = '' WHERE connection_id = $1", conn); err != nil {
		t.Fatal(err)
	}
	run, err = svc.Queue(ctx, conn, "sync", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Split(ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"ENG", "RST"} {
		if err := svc.RunPart(ctx, run, p); err != nil {
			t.Fatal(err)
		}
	}
	if r := runOf(t, admin, conn, run); r.Status != "running" {
		t.Fatalf("run %+v with a part left, want running", r)
	}
	src.Fail("/confluence/rest/api/content/search", http.StatusBadRequest, 1, "")
	if err := svc.RunPart(ctx, run, "OPS"); err != nil {
		t.Fatal(err)
	}
	if r := runOf(t, admin, conn, run); r.Status != "failed" || r.Error == "" || r.Unchanged != total-7 {
		t.Fatalf("run %+v, want failed with the counts of the parts that ran", r)
	}
	if n := env.Count("SELECT count(*) FROM connection_cursors WHERE connection_id = $1 AND cursor <> ''", conn); n != 2 {
		t.Fatalf("%d part cursors after the failed part, want the 2 of the parts that succeeded", n)
	}
}

func TestSCN_SYN_019_ReconcileKeepsItemThatMovedPart(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-wiki", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG", "RST"}}})
	if r := env.RunNow(admin, conn, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync %+v", r)
	}
	var moved int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '1001' AND part = 'ENG'",
		conn).Scan(&moved); err != nil {
		t.Fatal(err)
	}
	// Page 1001 moves from ENG to RST. Page 1002 is deleted.
	p, _ := src.Page("1001")
	p.Space = "RST"
	src.PutPage(p)
	src.DeletePage("1002")

	// The parts run in key order: ENG lists without page 1001 before RST
	// stores it.
	r := env.RunNow(admin, conn, "reconcile")
	if r.Status != "succeeded" || r.Deleted != 1 {
		t.Fatalf("reconcile %+v, want 1 deleted item", r)
	}
	var part string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT part FROM source_items WHERE id = $1", moved).Scan(&part); err != nil {
		t.Fatalf("the moved page is tombstoned: %v", err)
	}
	if part != "RST" {
		t.Fatalf("the moved page is in the part %q, want RST", part)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id = '1002'", conn); n != 0 {
		t.Fatal("the deleted page is not tombstoned")
	}

	// A space that leaves the scope takes its items with it. The change
	// queues a reconciliation.
	res := admin.Do(http.MethodPut, fmt.Sprintf("/api/v1/connections/%d", conn), map[string]any{
		"name": "acme-wiki", "kind": "confluence", "baseUrl": base + "/confluence", "scope": map[string]any{"spaces": []string{"ENG"}},
		"syncMinutes": 30, "reconcileHours": 24, "rateConcurrency": 4, "ratePerSecond": 100, "parallelParts": 4})
	if res.Status >= 300 {
		t.Fatalf("update: %d %s", res.Status, res.Body)
	}
	var queued int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM sync_runs WHERE connection_id = $1 AND status = 'queued'", conn).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if err := env.Server.Sync().Run(t.Context(), queued); err != nil {
		t.Fatal(err)
	}
	if r := runOf(t, admin, conn, queued); r.Status != "succeeded" || r.Deleted == 0 {
		t.Fatalf("reconcile after the scope change %+v", r)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND part <> 'ENG'", conn); n != 0 {
		t.Fatalf("%d items outside the scope", n)
	}
}

func TestSCN_SYN_020_ParallelPartsCapHoldsAcrossProcesses(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	src.Bulk("OPS", 3)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-wiki", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG", "OPS", "RST"}}})
	ctx := context.Background()
	if _, err := env.Server.Pool().Exec(ctx, "UPDATE connections SET parallel_parts = 1 WHERE id = $1", conn); err != nil {
		t.Fatal(err)
	}
	svc := env.Server.Sync()
	run, err := svc.Queue(ctx, conn, "sync", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Split(ctx, run); err != nil {
		t.Fatal(err)
	}
	// The part ENG runs on another worker process.
	if _, err := env.Server.Pool().Exec(ctx, "UPDATE sync_parts SET status = 'running', host = 'worker-2', started_at = now() WHERE run_id = $1 AND part = 'ENG'",
		run); err != nil {
		t.Fatal(err)
	}
	mark := len(src.Hits())
	err = svc.RunPart(ctx, run, "OPS")
	var snooze *river.JobSnoozeError
	if !errors.As(err, &snooze) {
		t.Fatalf("a part past the cap of 1: %v, want a snooze", err)
	}
	if n := len(src.Hits()) - mark; n != 0 {
		t.Fatalf("the held part sent %d requests", n)
	}
	if p := partRows(t, env, run); p[1].Part != "OPS" || p[1].Status != "queued" {
		t.Fatalf("parts %+v, want OPS queued", p)
	}

	// A cap of 2 lets the part run beside ENG.
	if _, err := env.Server.Pool().Exec(ctx, "UPDATE connections SET parallel_parts = 2 WHERE id = $1", conn); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunPart(ctx, run, "OPS"); err != nil {
		t.Fatal(err)
	}
	if p := partRows(t, env, run); p[1].Status != "succeeded" || p[1].Created != 3 {
		t.Fatalf("parts %+v, want OPS succeeded", p)
	}
}
