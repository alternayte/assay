//go:build contract

package sync_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

func publicConnection(t *testing.T, env *testkit.Env, admin *testkit.Client, base string) int64 {
	t.Helper()
	return testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
}

func itemID(t *testing.T, env *testkit.Env, conn int64, external string) int64 {
	t.Helper()
	var id int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM source_items WHERE connection_id = $1 AND external_id = $2",
		conn, external).Scan(&id); err != nil {
		t.Fatalf("item %s: %v", external, err)
	}
	return id
}

// searchItems returns the item IDs that a search finds.
func searchItems(t *testing.T, cl *testkit.Client, q string) []int64 {
	t.Helper()
	var out struct {
		Results []struct {
			ItemID int64 `json:"itemId"`
		} `json:"results"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/search?limit=50&q="+url.QueryEscape(q), nil).JSON(t, &out)
	ids := make([]int64, len(out.Results))
	for i, r := range out.Results {
		ids[i] = r.ItemID
	}
	return ids
}

// fakeEmbed turns the embed function on with the fake model API and fills
// the space.
func fakeEmbed(t *testing.T, env *testkit.Env, admin *testkit.Client) {
	t.Helper()
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "embed", testkit.Function{Endpoint: ep, Model: "fake-embed-64"})
	if err := env.Server.Embedder().Run(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSCN_SYN_001_SyncIsIdempotent(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := publicConnection(t, env, admin, base)

	first := env.RunNow(admin, conn, "sync")
	if first.Status != "succeeded" || first.Created != 8 {
		t.Fatalf("first sync %+v", first)
	}
	second := env.RunNow(admin, conn, "sync")
	if second.Status != "succeeded" || second.Created != 0 || second.Updated != 0 {
		t.Fatalf("second sync %+v", second)
	}
	versions := env.Count("SELECT count(*) FROM source_versions WHERE connection_id = $1", conn)
	if versions != 8 {
		t.Fatalf("want 7 versions, got %d", versions)
	}

	p, _ := src.Page("1007")
	p.Body += "<p>The reminder service also sends a weekly digest.</p>"
	p.Updated = time.Time{}
	src.PutPage(p)
	third := env.RunNow(admin, conn, "sync")
	if third.Updated != 1 || third.Created != 0 {
		t.Fatalf("third sync %+v", third)
	}
	if n := env.Count("SELECT count(*) FROM source_versions WHERE connection_id = $1", conn); n != versions+1 {
		t.Fatalf("want %d versions, got %d", versions+1, n)
	}
	// The cursor overlap fetches the changed page again, without a version.
	fourth := env.RunNow(admin, conn, "sync")
	if fourth.Updated != 0 || fourth.Seen == 0 {
		t.Fatalf("fourth sync %+v", fourth)
	}
	if n := env.Count("SELECT count(*) FROM source_versions WHERE connection_id = $1", conn); n != versions+1 {
		t.Fatalf("an unchanged page added a version: %d", n)
	}
}

func TestSCN_SYN_002_ReconcileTombstonesDeletedItems(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := publicConnection(t, env, admin, base)
	env.RunNow(admin, conn, "sync")

	var before struct {
		PermissionsCheckedAt *time.Time `json:"permissionsCheckedAt"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d", conn), nil).JSON(t, &before)
	if before.PermissionsCheckedAt != nil {
		t.Fatal("permissions checked before any reconciliation")
	}
	src.DeletePage("1006")
	rec := env.RunNow(admin, conn, "reconcile")
	if rec.Status != "succeeded" || rec.Deleted != 1 {
		t.Fatalf("reconcile %+v", rec)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id = '1006'", conn); n != 0 {
		t.Fatal("the deleted page is still stored")
	}
	var after struct {
		PermissionsCheckedAt *time.Time `json:"permissionsCheckedAt"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d", conn), nil).JSON(t, &after)
	if after.PermissionsCheckedAt == nil {
		t.Fatal("reconcile did not set permissions_checked_at")
	}
}

func TestSCN_SYN_007_UnauthorizedPausesAndHides(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := publicConnection(t, env, admin, base)
	env.RunNow(admin, conn, "sync")
	item := itemID(t, env, conn, "1001")
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/items/%d", item), nil)
	if !slices.Contains(searchItems(t, admin, "Payments API overview"), item) {
		t.Fatal("search does not find the page before the pause")
	}

	src.RevokeToken()
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "failed" || !strings.Contains(run.Error, "401") {
		t.Fatalf("run %+v", run)
	}
	var c struct {
		Status string `json:"status"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d", conn), nil).JSON(t, &c)
	if c.Status != "paused" {
		t.Fatalf("status %s", c.Status)
	}
	admin.Must(http.StatusNotFound, http.MethodGet, fmt.Sprintf("/api/v1/items/%d", item), nil)
	if got := searchItems(t, admin, "Payments API overview"); len(got) != 0 {
		t.Fatalf("search finds %v from the paused connection", got)
	}

	// After resume the content stays hidden until a reconciliation succeeds.
	src.AllowToken()
	admin.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/paused", conn), map[string]any{"paused": false})
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d", conn), nil).JSON(t, &c)
	if c.Status != "resuming" {
		t.Fatalf("status after resume %s", c.Status)
	}
	admin.Must(http.StatusNotFound, http.MethodGet, fmt.Sprintf("/api/v1/items/%d", item), nil)
	var runs struct {
		Runs []testkit.Run `json:"runs"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/runs", conn), nil).JSON(t, &runs)
	if runs.Runs[0].Kind != "reconcile" || runs.Runs[0].Status != "queued" {
		t.Fatalf("resume did not queue a reconciliation: %+v", runs.Runs[0])
	}
	if got := searchItems(t, admin, "Payments API overview"); len(got) != 0 {
		t.Fatalf("search finds %v before the reconciliation", got)
	}
	if err := env.Server.Sync().Run(t.Context(), runs.Runs[0].ID); err != nil {
		t.Fatal(err)
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/items/%d", item), nil)
	if !slices.Contains(searchItems(t, admin, "Payments API overview"), item) {
		t.Fatal("search does not find the page after the reconciliation")
	}
}

func TestSCN_SYN_008_NotFoundTombstones(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY", "REM"}}})
	env.RunNow(admin, conn, "sync")
	fakeEmbed(t, env, admin)
	item := itemID(t, env, conn, "PAY-2")
	for _, table := range []string{"passages", "chunks", "claim_evidence"} {
		if env.Count("SELECT count(*) FROM "+table+" WHERE item_id = $1", item) == 0 {
			t.Fatalf("no %s before the delete", table)
		}
	}
	var space int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM embedding_spaces WHERE status = 'active'").Scan(&space); err != nil {
		t.Fatal(err)
	}
	vectors := fmt.Sprintf("SELECT count(*) FROM emb_%d v JOIN chunks c ON c.id = v.chunk_id WHERE c.item_id = $1", space)
	if env.Count(vectors, item) == 0 {
		t.Fatal("no embeddings before the delete")
	}
	var chunkIDs []int64
	rows, _ := env.Server.Pool().Query(t.Context(), "SELECT id FROM chunks WHERE item_id = $1", item)
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		chunkIDs = append(chunkIDs, id)
	}
	rows.Close()
	if !slices.Contains(searchItems(t, admin, "retry queue Payments API"), item) {
		t.Fatal("search does not find the issue before the delete")
	}
	var assigned int64
	if err := env.Server.Pool().QueryRow(t.Context(), `SELECT c.id FROM claims c JOIN claim_evidence e ON e.claim_id = c.id
		WHERE e.item_id = $1 AND c.predicate = 'assigned_to'`, item).Scan(&assigned); err != nil {
		t.Fatal(err)
	}

	// The issue is gone, but the search still lists it. The fetch gets 404.
	is, _ := src.Issue("PAY-2")
	src.Fail("/jira/rest/api/2/issue/PAY-2", http.StatusNotFound, 10, "")
	is.Updated = time.Time{}
	src.PutIssue(is)
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "succeeded" || run.Deleted != 1 {
		t.Fatalf("run %+v", run)
	}
	for _, table := range []string{"source_items WHERE id", "source_versions WHERE item_id", "passages WHERE item_id",
		"chunks WHERE item_id", "claim_evidence WHERE item_id"} {
		if n := env.Count("SELECT count(*) FROM "+table+" = $1", item); n != 0 {
			t.Fatalf("%s: %d rows after the tombstone", table, n)
		}
	}
	if n := env.Count("SELECT count(*) FROM claims WHERE id = $1 AND retracted_at IS NOT NULL", assigned); n != 1 {
		t.Fatal("the claim without evidence is not retracted")
	}
	if n := env.Count(fmt.Sprintf("SELECT count(*) FROM emb_%d WHERE chunk_id = ANY($1)", space), chunkIDs); n != 0 {
		t.Fatalf("%d embeddings after the tombstone", n)
	}
	if slices.Contains(searchItems(t, admin, "retry queue Payments API"), item) {
		t.Fatal("search finds the deleted issue")
	}
	admin.Must(http.StatusNotFound, http.MethodGet, fmt.Sprintf("/api/v1/items/%d", item), nil)
}

func TestSCN_SYN_010_LargeItemIsSkipped(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	src.PutPage(fixtures.Page{ID: "1099", Space: "ENG", Title: "Large export", Author: "sam.acme",
		Body: "<p>" + strings.Repeat("x", 600<<10) + "</p>"})
	conn := publicConnection(t, env, admin, base)
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "succeeded" || run.Created != 8 || len(run.Skipped) != 1 {
		t.Fatalf("run %+v", run)
	}
	if run.Skipped[0].ID != "1099" || run.Skipped[0].Reason != "too_large" {
		t.Fatalf("skipped %+v", run.Skipped)
	}
}

func TestSCN_CON_002_JiraClaimsWithFieldEvidence(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY", "REM"}}})
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "succeeded" || run.Created != 7 {
		t.Fatalf("run %+v", run)
	}
	item := itemID(t, env, conn, "PAY-2")
	var body string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT body FROM source_versions WHERE item_id = $1", item).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Comments", "Use the existing PostgreSQL queue.", "Agreed. See `ADR-001`."} {
		if !strings.Contains(body, want) {
			t.Fatalf("the body has no %q", want)
		}
	}

	type claim struct{ subject, predicate, object, quote string }
	rows, err := env.Server.Pool().Query(t.Context(), `
		SELECT s.key, c.predicate, coalesce(o.key, c.object_text), e.quote
		FROM claims c
		JOIN entities s ON s.id = c.subject_id
		LEFT JOIN entities o ON o.id = c.object_entity_id
		JOIN claim_evidence e ON e.claim_id = c.id
		WHERE e.kind = 'field' AND e.state = 'current' AND e.connection_id = $1
		  AND EXISTS (SELECT 1 FROM passages p WHERE p.item_id = e.item_id AND p.hash = e.passage_hash AND strpos(p.norm, e.quote) > 0)`, conn)
	if err != nil {
		t.Fatal(err)
	}
	got := map[claim]bool{}
	for rows.Next() {
		var c claim
		if err := rows.Scan(&c.subject, &c.predicate, &c.object, &c.quote); err != nil {
			t.Fatal(err)
		}
		host := strings.TrimPrefix(base, "http://")
		c.subject = strings.ReplaceAll(c.subject, host, "h")
		c.object = strings.ReplaceAll(c.object, host, "h")
		got[c] = true
	}
	rows.Close()
	for _, want := range []claim{
		{"jira:h:PAY-2", "status", "To Do", "Status: To Do"},
		{"jira:h:PAY-2", "assigned_to", "jira:h:user:priya.acme", "Assignee: Priya Acme"},
		{"jira:h:PAY-1", "target_date", "2027-03-31", "Due date: 2027-03-31"},
		{"jira:h:PAY-2", "blocks", "jira:h:PAY-3", "Blocks: PAY-3"},
		{"jira:h:PAY-2", "part_of", "jira:h:PAY-1", "Epic: PAY-1"},
		{"jira:h:REM-2", "part_of", "jira:h:REM-1", "Epic: REM-1"},
	} {
		if !got[want] {
			t.Errorf("no claim %+v with field evidence; have %v", want, got)
		}
	}
	// A sub-task of a task is not part of an initiative.
	for c := range got {
		if c.subject == "jira:h:REM-3" && c.predicate == "part_of" {
			t.Errorf("REM-3 is part of %s", c.object)
		}
	}
	var epicType, storyType string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT type FROM entities WHERE key LIKE '%:PAY-1'").Scan(&epicType); err != nil {
		t.Fatal(err)
	}
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT type FROM entities WHERE key LIKE '%:PAY-2'").Scan(&storyType); err != nil {
		t.Fatal(err)
	}
	if epicType != "initiative" || storyType != "issue" {
		t.Fatalf("PAY-1 is %s and PAY-2 is %s", epicType, storyType)
	}
}

func TestSCN_CON_008_RetryAfterAndRateLimit(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}, Rate: 5})
	src.Fail("/confluence/rest/api/content/1003", http.StatusTooManyRequests, 1, "2")
	start := time.Now()
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "succeeded" || run.Created != 8 {
		t.Fatalf("run %+v", run)
	}
	var first, retry time.Time
	var times []time.Time
	for _, h := range src.Hits() {
		if h.At.Before(start) {
			continue
		}
		times = append(times, h.At)
		if h.Path == "/confluence/rest/api/content/1003" {
			if first.IsZero() {
				first = h.At
			} else {
				retry = h.At
			}
		}
	}
	if retry.IsZero() || retry.Sub(first) < 2*time.Second {
		t.Fatalf("the retry came %v after the 429", retry.Sub(first))
	}
	// 5 per second: any window of one second holds at most 5 requests.
	for i := range times {
		n := 0
		for j := i; j < len(times) && times[j].Sub(times[i]) < time.Second; j++ {
			n++
		}
		if n > 5+1 {
			t.Fatalf("%d requests in one second at a rate of 5", n)
		}
	}
	if len(times) < 9 {
		t.Fatalf("only %d requests", len(times))
	}
}

func TestSCN_EVL_001_DemoCountsMatchManifest(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	m := src.Manifest()
	if len(m.Connections) < 3 || m.Canary != fixtures.Canary {
		t.Fatalf("manifest %+v", m)
	}
	ids := map[string]int64{}
	for _, mc := range m.Connections {
		id := testkit.CreateConnection(t, admin, testkit.Connection{Name: mc.Name, Kind: mc.Kind, BaseURL: base, Path: mc.Path, Scope: mc.Scope})
		ids[mc.Name] = id
		run := env.RunNow(admin, id, "sync")
		if run.Status != "succeeded" || run.Created != mc.Items || len(run.Skipped) != 0 {
			t.Fatalf("%s: run %+v, manifest %d items", mc.Name, run, mc.Items)
		}
		if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1", id); n != mc.Items {
			t.Fatalf("%s: %d items, manifest %d", mc.Name, n, mc.Items)
		}
	}
	rows, err := env.Server.Pool().Query(t.Context(), `SELECT DISTINCT connection_id FROM passages WHERE strpos(norm, $1) > 0
		UNION SELECT DISTINCT connection_id FROM source_items WHERE strpos(title, $1) > 0`, fixtures.Canary)
	if err != nil {
		t.Fatal(err)
	}
	var with []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		with = append(with, id)
	}
	rows.Close()
	if len(with) != 1 || with[0] != ids["acme-restricted"] {
		t.Fatalf("the canary is in connections %v; want only %d", with, ids["acme-restricted"])
	}
}

func blobHits(src *fixtures.Sources, since int) int {
	n := 0
	for _, h := range src.Hits()[since:] {
		if strings.Contains(h.Path, "/git/blobs/") || strings.Contains(h.Path, "/blobs/") {
			n++
		}
	}
	return n
}

func TestSCN_CON_004_SyncFetchesOnlyChangedBlobs(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api"}}})
	want := src.RepoItems("github", "acme/payments-api")

	first := env.RunNow(admin, conn, "sync")
	if first.Status != "succeeded" || first.Created != want {
		t.Fatalf("first sync %+v, want %d new", first, want)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id LIKE '%main.go'", conn); n != 0 {
		t.Fatal("a file outside the globs is stored")
	}
	if n := env.Count(`SELECT count(*) FROM claim_evidence WHERE connection_id = $1 AND source_type = 'catalog'`, conn); n == 0 {
		t.Fatal("the catalog file gave no catalog evidence")
	}
	if n := env.Count(`SELECT count(*) FROM claim_evidence WHERE connection_id = $1 AND source_type = 'codeowners'`, conn); n != 1 {
		t.Fatalf("CODEOWNERS gave %d evidence rows, want 1", n)
	}

	mark := len(src.Hits())
	second := env.RunNow(admin, conn, "sync")
	if second.Status != "succeeded" || second.Created != 0 || second.Updated != 0 || second.Unchanged != want {
		t.Fatalf("second sync %+v", second)
	}
	if n := blobHits(src, mark); n != 0 {
		t.Fatalf("an unchanged tree fetched %d blobs", n)
	}

	src.PutFile("github", "acme/payments-api", "docs/architecture.md", []byte("# Architecture\n\nThe Payments API is a Go service. It stores payments in PostgreSQL 17.\n"))
	mark = len(src.Hits())
	third := env.RunNow(admin, conn, "sync")
	if third.Updated != 1 || third.Created != 0 {
		t.Fatalf("third sync %+v", third)
	}
	if n := blobHits(src, mark); n != 1 {
		t.Fatalf("one changed file fetched %d blobs", n)
	}
	if n := env.Count("SELECT count(*) FROM source_versions WHERE connection_id = $1", conn); n != want+1 {
		t.Fatalf("want %d versions, got %d", want+1, n)
	}

	src.DeleteFile("github", "acme/payments-api", "docs/architecture.md")
	rec := env.RunNow(admin, conn, "reconcile")
	if rec.Status != "succeeded" || rec.Deleted != 1 {
		t.Fatalf("reconcile %+v", rec)
	}
}

func TestSCN_CON_005_AzdoApiVersionAndBasicAuth(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-azdo", Kind: "azdo", BaseURL: base,
		Path: "/azdo/" + fixtures.AzdoCollection, Scope: map[string]any{"repositories": []string{"Platform/ledger-service"}}})
	var v struct {
		User   string `json:"user"`
		Sample int    `json:"sample"`
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/test", map[string]any{"kind": "azdo",
		"baseUrl": base + "/azdo/" + fixtures.AzdoCollection, "connectionId": conn,
		"scope": map[string]any{"repositories": []string{"Platform/ledger-service"}}}).JSON(t, &v)
	want := src.RepoItems("azdo", "Platform/ledger-service")
	if v.User != "Assay Reader" || v.Sample != want {
		t.Fatalf("test %+v, want %d files", v, want)
	}

	run := env.RunNow(admin, conn, "sync")
	if run.Status != "succeeded" || run.Created != want || len(run.Skipped) != 0 {
		t.Fatalf("sync %+v, want %d new", run, want)
	}
	for _, f := range []string{"README.md", "docs/runbook.md", "catalog-info.yaml", "CODEOWNERS", "Chart.yaml", "Ledger.csproj"} {
		if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id = $2", conn,
			"Platform/ledger-service:"+f); n != 1 {
			t.Fatalf("%s is not indexed", f)
		}
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id LIKE '%.cs'", conn); n != 0 {
		t.Fatal("a file outside the globs is stored")
	}
	var url string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT url FROM source_items WHERE connection_id = $1 AND external_id = $2",
		conn, "Platform/ledger-service:docs/runbook.md").Scan(&url); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(url, "/Platform/_git/ledger-service?path=%2Fdocs%2Frunbook.md&version=GBmain") {
		t.Fatalf("url %s", url)
	}
	n := 0
	for _, h := range src.Hits() {
		if !strings.HasPrefix(h.Path, "/azdo/") {
			continue
		}
		n++
		if h.Auth != "Basic" {
			t.Fatalf("%s %s has auth %q", h.Method, h.Path, h.Auth)
		}
		if strings.Contains(h.Path, "/_apis/git/") && !strings.Contains(h.Query, "api-version=7.1") {
			t.Fatalf("%s?%s has no api-version=7.1", h.Path, h.Query)
		}
	}
	if n == 0 {
		t.Fatal("no request reached the Azure DevOps fixture")
	}
}
