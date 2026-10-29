package extract_test

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/extract"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

type world struct {
	env            *testkit.Env
	admin          *testkit.Client
	src            *fixtures.Sources
	fake           *fixtures.Models
	public, github int64
	endpoint       int64
}

func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, admin: env.Setup("owner@acme.test")}
	var base string
	w.src, base = testkit.FakeSources(t)
	w.public = testkit.CreateConnection(t, w.admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	w.github = testkit.CreateConnection(t, w.admin, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api"}}})
	var openai string
	w.fake, openai, _ = testkit.FakeModels(t)
	w.endpoint = testkit.CreateEndpoint(t, w.admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.admin, "extract", testkit.Function{Endpoint: w.endpoint, Model: "fake-chat"})
	return w
}

func (w *world) sync(t *testing.T, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		if r := w.env.RunNow(w.admin, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %d: %+v", id, r)
		}
	}
}

func extractCalls(f *fixtures.Models) int {
	n := 0
	for _, r := range f.RequestsTo("/v1/chat/completions") {
		if rf, ok := r.Body["response_format"].(map[string]any); ok {
			if js, ok := rf["json_schema"].(map[string]any); ok && js["name"] == "extraction" {
				n++
			}
		}
	}
	return n
}

// passages returns the item ID and passage hashes of an item.
func (w *world) passages(t *testing.T, conn int64, external string) (int64, []string) {
	t.Helper()
	rows, err := w.env.Server.Pool().Query(t.Context(), `SELECT p.item_id, p.hash FROM passages p JOIN source_items i ON i.id = p.item_id
		WHERE i.connection_id = $1 AND i.external_id = $2 ORDER BY p.ordinal`, conn, external)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var item int64
	var hashes []string
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&item, &h); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, hex.EncodeToString(h))
	}
	return item, hashes
}

// queue queues the passages of an item with the visible set of the owner.
func (w *world) queue(t *testing.T, conn int64, external string) {
	t.Helper()
	item, hashes := w.passages(t, conn, external)
	items := make([]int64, len(hashes))
	for i := range items {
		items[i] = item
	}
	if _, err := w.env.Server.Extract().Queue(t.Context(), []int64{w.public, w.github}, extract.TriggerAnswer, items, hashes); err != nil {
		t.Fatal(err)
	}
}

func TestSCN_EXT_002_SameHashMakesNoCall(t *testing.T) {
	w := start(t)
	w.sync(t, w.public)
	w.queue(t, w.public, "1007")
	w.env.RunExtractJobs()
	_, hashes := w.passages(t, w.public, "1007")
	if got := extractCalls(w.fake); got != len(hashes) {
		t.Fatalf("%d calls for %d passages", got, len(hashes))
	}

	// A new version changes one paragraph. Only that passage is new.
	p, _ := w.src.Page("1007")
	p.Body += "<p>The reminder service also sends a weekly digest to editors.</p>"
	p.Updated = time.Time{}
	w.src.PutPage(p)
	w.sync(t, w.public)
	w.fake.Reset()
	w.queue(t, w.public, "1007")
	w.env.RunExtractJobs()
	if got := extractCalls(w.fake); got != 1 {
		t.Fatalf("%d calls after a one-paragraph change, want 1", got)
	}
}

func TestSCN_EXT_001_AdminTriggerShowsEstimateFirst(t *testing.T) {
	w := start(t)
	w.sync(t, w.public)
	testkit.SetFunction(t, w.admin, "extract", testkit.Function{Endpoint: w.endpoint, Model: "fake-chat", PriceIn: 2, PriceOut: 8})
	var e struct {
		Passages int64   `json:"passages"`
		CostUSD  float64 `json:"costUsd"`
		On       bool    `json:"on"`
	}
	path := fmt.Sprintf("/api/v1/connections/%d/extraction", w.public)
	w.admin.Must(http.StatusOK, http.MethodGet, path, nil).JSON(t, &e)
	total := w.env.Count("SELECT count(*) FROM passages WHERE connection_id = $1", w.public)
	if e.Passages != int64(total) || e.CostUSD <= 0 || !e.On {
		t.Fatalf("estimate %+v for %d passages", e, total)
	}
	if n := len(w.fake.Requests()); n != 0 {
		t.Fatalf("the estimate sent %d requests", n)
	}
	r := w.admin.Do(http.MethodPost, path, map[string]any{"passages": e.Passages + 1})
	if r.Status != http.StatusConflict || r.Code() != "estimate_changed" {
		t.Fatalf("wrong count: %d %s", r.Status, r.Body)
	}
	w.admin.Must(http.StatusAccepted, http.MethodPost, path, map[string]any{"passages": e.Passages})
	if n := w.env.RunExtractJobs(); n == 0 {
		t.Fatal("no job queued")
	}
	if got := extractCalls(w.fake); got != total {
		t.Fatalf("%d calls for %d passages", got, total)
	}
	w.admin.Must(http.StatusOK, http.MethodGet, path, nil).JSON(t, &e)
	if e.Passages != 0 {
		t.Fatalf("%d passages left", e.Passages)
	}
}

func TestSCN_EXT_006_BudgetPausesExtraction(t *testing.T) {
	w := start(t)
	w.sync(t, w.public)
	testkit.SetFunction(t, w.admin, "extract", testkit.Function{Endpoint: w.endpoint, Model: "fake-chat", PriceIn: 1, PriceOut: 1, Budget: 0.01})
	testkit.SetFunction(t, w.admin, "answer", testkit.Function{Endpoint: w.endpoint, Model: "fake-chat"})
	if _, err := w.env.Server.Pool().Exec(t.Context(),
		"INSERT INTO model_usage (function, model, tokens_in, tokens_out, cost_usd) VALUES ('extract', 'fake-chat', 9900, 0, 0.0099)"); err != nil {
		t.Fatal(err)
	}
	w.queue(t, w.public, "1007")
	var args extract.Args
	if err := w.env.Server.Pool().QueryRow(t.Context(), "SELECT args FROM river_job WHERE kind = 'extract'").Scan(&args); err != nil {
		t.Fatal(err)
	}
	err := w.env.Server.Extract().Run(t.Context(), args)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("the job did not pause: %v", err)
	}
	if extractCalls(w.fake) != 0 {
		t.Fatal("a request went out over the budget")
	}
	var st struct {
		OverBudget []string `json:"overBudget"`
	}
	w.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/models/status", nil).JSON(t, &st)
	if len(st.OverBudget) != 1 || st.OverBudget[0] != "extract" {
		t.Fatalf("status %+v", st)
	}
	// Ask still answers.
	r := w.admin.Do(http.MethodPost, "/api/v1/ask", map[string]string{"question": "What does the reminder service do?"})
	if r.Status != http.StatusOK || !strings.Contains(string(r.Body), "event: answer") || strings.Contains(string(r.Body), "notice") {
		t.Fatalf("ask: %d %s", r.Status, r.Body)
	}
}

func TestSCN_CLM_002_SameFactIsOneClaim(t *testing.T) {
	w := start(t)
	w.src.PutPage(fixtures.Page{ID: "1098", Space: "ENG", Title: "Payments ownership", Author: "sam.acme",
		Body: "<p>The payments team owns the Payments API and answers its pages.</p>"})
	w.sync(t, w.github, w.public)
	comp, team := "backstage:component:default/payments-api", "backstage:group:default/payments-team"
	w.fake.OnChat(func(body map[string]any) string {
		msgs := body["messages"].([]any)
		user := msgs[1].(map[string]any)["content"].(string)
		if !strings.Contains(user, "The payments team owns the Payments API") {
			return `{"claims":[]}`
		}
		return `{"claims":[{"subject":{"kind":"key","value":"` + comp + `"},"predicate":"owned_by","object":{"kind":"key","value":"` + team +
			`"},"scope":null,"stance":"supports","quote":"The payments team owns the Payments API"}]}`
	})
	before := w.env.Count(`SELECT count(*) FROM claims c JOIN entities s ON s.id = c.subject_id JOIN entities o ON o.id = c.object_entity_id
		WHERE s.key = $1 AND o.key = $2 AND c.predicate = 'owned_by'`, comp, team)
	if before != 1 {
		t.Fatalf("%d catalog owner claims", before)
	}
	w.queue(t, w.public, "1098")
	w.env.RunExtractJobs()
	var claims, rows int
	if err := w.env.Server.Pool().QueryRow(t.Context(), `SELECT count(DISTINCT c.id), count(e.id) FROM claims c
		JOIN entities s ON s.id = c.subject_id JOIN entities o ON o.id = c.object_entity_id
		JOIN claim_evidence e ON e.claim_id = c.id
		WHERE s.key = $1 AND o.key = $2 AND c.predicate = 'owned_by'`, comp, team).Scan(&claims, &rows); err != nil {
		t.Fatal(err)
	}
	if claims != 1 || rows != 2 {
		var reasons string
		_ = w.env.Server.Pool().QueryRow(t.Context(), "SELECT string_agg(reasons::text, ' ') FROM passage_extractions").Scan(&reasons)
		t.Fatalf("%d claims with %d evidence rows, want 1 with 2; rejections %s", claims, rows, reasons)
	}
	if n := w.env.Count("SELECT count(*) FROM claim_evidence WHERE kind = 'passage' AND source_type = 'confluence'"); n != 1 {
		t.Fatalf("%d passage evidence rows from Confluence", n)
	}
}
