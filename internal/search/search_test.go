package search_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/search"
	"github.com/alternayte/assay/internal/testkit"
)

type segment struct {
	Text string `json:"text"`
	Hit  bool   `json:"hit"`
}

type result struct {
	ItemID         int64     `json:"itemId"`
	Title          string    `json:"title"`
	SourceType     string    `json:"sourceType"`
	ConnectionID   int64     `json:"connectionId"`
	ConnectionName string    `json:"connectionName"`
	Snippet        []segment `json:"snippet"`
}

type response struct {
	Entities []struct {
		Key  string `json:"key"`
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"entities"`
	Results  []result `json:"results"`
	Degraded bool     `json:"degraded"`
	Reranked bool     `json:"reranked"`
}

// corpus syncs the public Confluence space, the Jira projects and the
// GitHub repositories of the fixture server.
type corpus struct {
	env                      *testkit.Env
	admin                    *testkit.Client
	confluence, jira, github int64
}

func setup(t *testing.T) corpus {
	t.Helper()
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	c := corpus{env: env, admin: admin}
	c.confluence = testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	c.jira = testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY", "REM"}}})
	c.github = testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}})
	for _, id := range []int64{c.confluence, c.jira, c.github} {
		if r := env.RunNow(admin, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %d: %+v", id, r)
		}
	}
	return c
}

func find(t *testing.T, cl *testkit.Client, q string, params ...string) response {
	t.Helper()
	v := url.Values{"q": {q}}
	for i := 0; i+1 < len(params); i += 2 {
		v.Set(params[i], params[i+1])
	}
	var out response
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/search?"+v.Encode(), nil).JSON(t, &out)
	return out
}

func titles(rs []result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

func TestSCN_SRC_001_TrigramFullTextAndFusion(t *testing.T) {
	c := setup(t)

	// A misspelled identifier: no word matches, the title is close.
	if n := c.env.Count(`SELECT count(*) FROM chunks WHERE tsv @@ websearch_to_tsquery('simple', 'reminder-servce')`); n != 0 {
		t.Fatalf("full-text matches the misspelled query in %d chunks", n)
	}
	typo := find(t, c.admin, "reminder-servce")
	if !slices.Contains(titles(typo.Results), "acme/reminder-service/README.md") {
		t.Fatalf("misspelled identifier: %v", titles(typo.Results))
	}
	for _, r := range typo.Results {
		if !strings.Contains(strings.ToLower(r.Title), "reminder") {
			t.Fatalf("%s matches the misspelled identifier", r.Title)
		}
	}
	if !typo.Degraded {
		t.Fatal("search without an embed function is not marked degraded")
	}

	// A phrase through full-text.
	phrase := find(t, c.admin, `"sends each settled payment to the ledger service"`)
	if len(phrase.Results) != 1 || phrase.Results[0].Title != "acme/payments-api/README.md" {
		t.Fatalf("phrase: %v", titles(phrase.Results))
	}

	// "Payments API" is in titles and in text: each item appears once.
	both := find(t, c.admin, "Payments API")
	seen := map[int64]bool{}
	for _, r := range both.Results {
		if seen[r.ItemID] {
			t.Fatalf("item %d (%s) appears twice", r.ItemID, r.Title)
		}
		seen[r.ItemID] = true
	}
	if len(both.Results) < 3 || both.Results[0].Title != "Payments API overview" {
		t.Fatalf("fused results: %v", titles(both.Results))
	}

	got := search.Fuse([][]string{{"a", "b", "c"}, {"c", "a"}, {"d"}}, 60, 10)
	if strings.Join(got, ",") != "a,c,d,b" {
		t.Fatalf("fuse order %v", got)
	}
}

func TestSCN_SRC_002_FiltersReturnOnlyMatchingItems(t *testing.T) {
	c := setup(t)
	pool := c.env.Server.Pool()

	for _, kind := range []string{"confluence", "jira", "github"} {
		res := find(t, c.admin, "payments", "sourceType", kind)
		if len(res.Results) == 0 {
			t.Fatalf("no %s results", kind)
		}
		for _, r := range res.Results {
			if r.SourceType != kind {
				t.Fatalf("source type %s: got %s (%s)", kind, r.SourceType, r.Title)
			}
		}
	}

	res := find(t, c.admin, "payments", "connection", fmt.Sprint(c.jira))
	for _, r := range res.Results {
		if r.ConnectionID != c.jira {
			t.Fatalf("connection filter: %s from %d", r.Title, r.ConnectionID)
		}
	}

	// Entity type: the catalog file is about services; the Jira epic is an
	// initiative.
	svc := find(t, c.admin, "payments", "entityType", "service")
	if len(svc.Results) == 0 {
		t.Fatal("no items about a service")
	}
	for _, r := range svc.Results {
		if !strings.HasSuffix(r.Title, "catalog-info.yaml") {
			t.Fatalf("entity type service: %s", r.Title)
		}
	}
	ini := find(t, c.admin, "payments", "entityType", "initiative")
	for _, r := range ini.Results {
		if r.SourceType != "jira" {
			t.Fatalf("entity type initiative: %s", r.Title)
		}
	}

	var domain int64
	if err := pool.QueryRow(t.Context(), "INSERT INTO domains (name) VALUES ('Finance') RETURNING id").Scan(&domain); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "UPDATE connections SET default_domain_id = $1 WHERE id = $2", domain, c.confluence); err != nil {
		t.Fatal(err)
	}
	dom := find(t, c.admin, "payments", "domain", fmt.Sprint(domain))
	if len(dom.Results) == 0 {
		t.Fatal("no items in the domain")
	}
	for _, r := range dom.Results {
		if r.ConnectionID != c.confluence {
			t.Fatalf("domain filter: %s", r.Title)
		}
	}

	since := find(t, c.admin, "payments", "since", "2026-02-02T00:00:00Z", "sourceType", "confluence")
	for _, r := range since.Results {
		if r.Title == "Payments API overview" {
			t.Fatal("an item updated before the since time is in the results")
		}
	}
	if len(since.Results) == 0 {
		t.Fatal("no item updated since the time")
	}
}

func TestSCN_SRC_003_EntitiesFirstAndHighlights(t *testing.T) {
	c := setup(t)
	// The query equals an alias of the component and of the repository.
	res := find(t, c.admin, "payments-api")
	if len(res.Entities) < 2 || len(res.Results) == 0 {
		t.Fatalf("entities %+v, %d results", res.Entities, len(res.Results))
	}
	found := false
	for _, e := range res.Entities[:2] {
		if e.Key == "backstage:component:default/payments-api" && e.Type == "service" && e.Name == "Payments API" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the component is not among the first entities: %+v", res.Entities)
	}

	hl := find(t, c.admin, "ledger")
	if len(hl.Results) == 0 {
		t.Fatal("no results for ledger")
	}
	for _, r := range hl.Results {
		marked := false
		for _, s := range r.Snippet {
			if s.Hit {
				marked = true
				if !strings.EqualFold(s.Text, "ledger") {
					t.Fatalf("%s highlights %q", r.Title, s.Text)
				}
			}
		}
		if !marked {
			t.Fatalf("%s has no highlight: %+v", r.Title, r.Snippet)
		}
	}
}

// queryModels returns the models of the embed requests that carry one input:
// query embeddings.
func queryModels(fake *fixtures.Models) []string {
	var out []string
	for _, r := range fake.RequestsTo("/v1/embeddings") {
		if in, ok := r.Body["input"].([]any); ok && len(in) == 1 && in[0] != "Assay dimension probe" {
			out = append(out, r.Body["model"].(string))
		}
	}
	return out
}

func TestSCN_MOD_005_SearchUsesOneSpaceAtATime(t *testing.T) {
	c := setup(t)
	ctx := t.Context()
	fake, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, c.admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, c.admin, "embed", testkit.Function{Endpoint: ep, Model: "fake-embed-64"})
	emb := c.env.Server.Embedder()
	if err := emb.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var first int64
	if err := c.env.Server.Pool().QueryRow(ctx, "SELECT id FROM embedding_spaces WHERE status = 'active'").Scan(&first); err != nil {
		t.Fatal(err)
	}
	chunks := c.env.Count("SELECT count(*) FROM chunks")
	if n := c.env.Count(fmt.Sprintf("SELECT count(*) FROM emb_%d", first)); n != chunks {
		t.Fatalf("%d vectors for %d chunks", n, chunks)
	}
	res := find(t, c.admin, "ledger")
	if res.Degraded || len(res.Results) == 0 {
		t.Fatalf("search with vectors: degraded %v, %d results", res.Degraded, len(res.Results))
	}

	// A new model builds a new space. Search stays on the old space.
	testkit.SetFunction(t, c.admin, "embed", testkit.Function{Endpoint: ep, Model: "fake-embed-32"})
	space, err := c.env.Server.Models().TargetSpace(ctx)
	if err != nil || space.Status != "building" || space.Dim != 32 {
		t.Fatalf("target space %+v %v", space, err)
	}
	fake.Reset()
	if res := find(t, c.admin, "ledger"); res.Degraded {
		t.Fatal("search is degraded while the new space builds")
	}
	if got := queryModels(fake); len(got) != 1 || got[0] != "fake-embed-64" {
		t.Fatalf("query embeddings while building: %v", got)
	}

	if err := emb.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := c.env.Count("SELECT count(*) FROM embedding_spaces WHERE status = 'active' AND id = $1", space.ID); n != 1 {
		t.Fatal("the new space is not active")
	}
	if n := c.env.Count("SELECT count(*) FROM pg_class WHERE relname = $1", fmt.Sprintf("emb_%d", first)); n != 0 {
		t.Fatal("the old table is not dropped")
	}
	fake.Reset()
	if res := find(t, c.admin, "ledger"); res.Degraded || len(res.Results) == 0 {
		t.Fatalf("search after the switch: %+v", res)
	}
	if got := queryModels(fake); len(got) != 1 || got[0] != "fake-embed-32" {
		t.Fatalf("query embeddings after the switch: %v", got)
	}
}

func TestSCN_MOD_006_RerankIsOptional(t *testing.T) {
	c := setup(t)
	fake, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, c.admin, "fake", "openai", openai, fixtures.FakeKey, nil)

	off := find(t, c.admin, "payments", "limit", "50")
	if off.Reranked || len(fake.RequestsTo("/v1/rerank")) != 0 {
		t.Fatal("rerank ran while the function is off")
	}
	docs := min(len(off.Results), search.RerankIn)
	if docs < 3 {
		t.Fatalf("only %d results", len(off.Results))
	}

	testkit.SetFunction(t, c.admin, "rerank", testkit.Function{Endpoint: ep, Model: "fake-rerank"})
	on := find(t, c.admin, "payments", "limit", "50")
	calls := fake.RequestsTo("/v1/rerank")
	if !on.Reranked || len(calls) != 1 || len(calls[0].Body["documents"].([]any)) != docs {
		t.Fatalf("reranked %v with %d calls", on.Reranked, len(calls))
	}
	// The fake ranks the documents in reverse order. The result order is
	// the rerank order.
	if len(on.Results) != min(docs, search.RerankOut) {
		t.Fatalf("%d results after rerank", len(on.Results))
	}
	for i, r := range on.Results {
		if want := off.Results[docs-1-i]; r.ItemID != want.ItemID {
			t.Fatalf("result %d is %s, want %s", i, r.Title, want.Title)
		}
	}
}
