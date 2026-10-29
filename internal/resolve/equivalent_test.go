package resolve_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// decision adds a decision entity with decided claims of the texts, each
// with evidence on a Jira passage.
func (w *world) decision(t *testing.T, key string, texts ...string) {
	t.Helper()
	w.exec(t, `WITH e AS (INSERT INTO entities (type, key) VALUES ('decision', $1) RETURNING id)
INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $2 FROM e`, key, w.jira)
	for _, text := range texts {
		w.exec(t, `
WITH c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
           SELECT id, 'decided', $2, $3 FROM entities WHERE key = $1 ON CONFLICT DO NOTHING RETURNING id),
     cid AS (SELECT id FROM c UNION ALL SELECT cl.id FROM claims cl JOIN entities e ON e.id = cl.subject_id
             WHERE e.key = $1 AND cl.object_norm = $3 LIMIT 1),
     p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p WHERE p.connection_id = $4 ORDER BY p.id LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, quote, source_type)
SELECT (SELECT id FROM cid LIMIT 1), p.connection_id, p.item_id, p.hash, 'passage', left(p.norm, 20), 'jira' FROM p
ON CONFLICT DO NOTHING`, key, text, strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer("-", " ").Replace(text)), " ")), w.jira)
	}
}

func statuses(t *testing.T, cl *testkit.Client, key string) []string {
	t.Helper()
	var d struct {
		Claims []struct {
			Predicate string `json:"predicate"`
			Status    string `json:"status"`
		} `json:"claims"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(key), nil).JSON(t, &d)
	var out []string
	for _, c := range d.Claims {
		if c.Predicate == "decided" {
			out = append(out, c.Status)
		}
	}
	return out
}

func TestSCN_RES_007_EquivalentTextValues(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	fake, _, decisions := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "decide", "decisions", decisions, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "decide", testkit.Function{Endpoint: ep, Model: "fake-decide"})
	fake.OnDecide(func(_ string, _ map[string]any, state map[string]any) map[string]any {
		a, _ := state["first"].(string)
		b, _ := state["second"].(string)
		if a == "" {
			return nil
		}
		p := 0.05
		if (a == "Q1 2027" && b == "end of March 2027") || (a == "end of March 2027" && b == "Q1 2027") {
			p = 0.96
		}
		return map[string]any{"type": "noul", "noul": p}
	})
	w.decision(t, "jira:acme:decision:launch", "Q1 2027", "end of March 2027")
	w.decision(t, "jira:acme:decision:review", "Q1", "Q3")
	w.decision(t, "jira:acme:decision:state", "In progress", "in-progress")

	r := w.env.Server.Resolve()
	if err := r.Equivalence(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.env.Count("SELECT count(*) FROM decisions WHERE kind = 'equivalent'"); n != 2 {
		t.Errorf("%d equivalent decisions, want 2: normalized equal values need none", n)
	}
	for key, want := range map[string]string{"jira:acme:decision:launch": "supported", "jira:acme:decision:review": "conflicting",
		"jira:acme:decision:state": "supported"} {
		for _, got := range statuses(t, w.owner, key) {
			if got != want {
				t.Errorf("%s has a claim with status %s, want %s", key, got, want)
			}
		}
	}
	if got := statuses(t, w.owner, "jira:acme:decision:state"); len(got) != 1 {
		t.Errorf("In progress and in-progress are %d claims", len(got))
	}

	calls := len(fake.Requests())
	if err := r.Equivalence(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Requests()); n != calls {
		t.Errorf("a repeat check made %d calls", n-calls)
	}
}
