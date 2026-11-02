package round3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// The visibility key of a page came only from the connections of the claim
// evidence, but the compile prompt also holds the definitions of approved
// terms. Two readers who see another definition of a term shared one cached
// summary. The connections of an answer also left out the terms and the page
// summary in the prompt, so the history showed the answer to a reader who
// cannot see them.
func TestRVW_3_06_TermAndOrientationConnectionsNotInKeys(t *testing.T) {
	w := start(t)
	fake, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "compile", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	testkit.SetFunction(t, w.owner, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat"})

	rec := func(kind string, fields map[string]any) string {
		t.Helper()
		var out struct {
			Key string `json:"key"`
		}
		w.reviewer.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": kind, kind: fields}).JSON(t, &out)
		return out.Key
	}
	team := rec("thing", map[string]any{"type": "team", "name": "Clearing Team"})
	ledger := rec("thing", map[string]any{"type": "service", "name": "Clearing Ledger"})
	gateway := rec("thing", map[string]any{"type": "service", "name": "Clearing Gateway", "owner": team,
		"description": "The Clearing Gateway closes the settlement window at noon.",
		"relations":   []map[string]string{{"predicate": "depends_on", "object": ledger}}})
	// The registry makes the term approved for all. Only acme-restricted
	// gives its definition.
	term := rec("term", map[string]any{"name": "Settlement Window", "status": "approved"})
	w.exec(t, `
WITH s AS (SELECT id FROM entities WHERE key = $1),
     c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
           SELECT s.id, 'defined_as', 'The hour in which Acme sends card payments to the bank.',
                  'the hour in which acme sends card payments to the bank' FROM s RETURNING id),
     p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p
           WHERE p.connection_id = $2 ORDER BY p.item_id, p.ordinal LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT (SELECT id FROM c), p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM p`, term, w.restricted)

	type page struct {
		VisibilityKey string `json:"visibilityKey"`
		Summary       struct {
			State string `json:"state"`
		} `json:"summary"`
	}
	compile := func(cl *testkit.Client) page {
		t.Helper()
		path := "/api/v1/entities/" + url.PathEscape(gateway) + "/page"
		if r := cl.Do(http.MethodPost, path+"/compile", nil); r.Status != http.StatusOK {
			t.Fatalf("compile: %d %s", r.Status, r.Body)
		}
		var p page
		cl.Must(http.StatusOK, http.MethodGet, path, nil).JSON(t, &p)
		return p
	}
	own, mem := compile(w.owner), compile(w.member)
	if own.Summary.State != "fresh" || mem.Summary.State != "fresh" {
		t.Fatalf("summary states %s and %s", own.Summary.State, mem.Summary.State)
	}
	if own.VisibilityKey == mem.VisibilityKey {
		t.Error("the owner and the member share a page key, but only the owner sees the definition of Settlement Window")
	}

	fake.OnChat(func(map[string]any) string { return "The Clearing Gateway closes the settlement window at noon [C1]." })
	r := w.owner.Do(http.MethodPost, "/api/v1/ask", map[string]string{"question": "When does the Clearing Gateway close?"})
	if r.Status != http.StatusOK {
		t.Fatalf("ask: %d %s", r.Status, r.Body)
	}
	var raw []byte
	if err := w.env.Server.Pool().QueryRow(context.Background(),
		`SELECT flags FROM questions ORDER BY id DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var flags struct {
		Connections []int64 `json:"connections"`
	}
	if err := json.Unmarshal(raw, &flags); err != nil {
		t.Fatal(err)
	}
	if n := w.env.Count(`SELECT count(*) FROM questions q, jsonb_array_elements(q.citations->'sources') s
		JOIN source_items i ON i.id = (s->>'itemId')::bigint WHERE i.connection_id = $1`, w.restricted); n != 0 {
		t.Fatalf("the answer has %d sources of acme-restricted; the test needs none", n)
	}
	if !slices.Contains(flags.Connections, w.restricted) {
		t.Errorf("the answer connections %v leave out acme-restricted, which gives the term definition in the prompt", flags.Connections)
	}
	user := ""
	for _, req := range fake.RequestsTo("/v1/chat/completions") {
		if msgs, ok := req.Body["messages"].([]any); ok && len(msgs) > 1 {
			if c, ok := msgs[1].(map[string]any)["content"].(string); ok && strings.HasPrefix(c, "Question:") {
				user = c
			}
		}
	}
	if !strings.Contains(user, "The hour in which Acme sends card payments") || !strings.Contains(user, "<orientation") {
		t.Errorf("the answer prompt has no restricted term definition or no orientation:\n%s", user)
	}
}
