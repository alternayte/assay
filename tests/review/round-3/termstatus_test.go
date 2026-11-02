package round3

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

// The status of a term took the newest dated claim first, so an accepted
// correction, a human claim with no date, lost to the registry claim. The
// evidence of an open, rejected or needs_evidence proposal counted for the
// status. The preferred term named an entity that the reader cannot see.
func TestRVW_3_07_TermStatusIgnoresCorrectionsAndProposals(t *testing.T) {
	w := start(t)
	type term struct {
		Key       string `json:"key"`
		Status    string `json:"status"`
		Preferred string `json:"preferred"`
	}
	terms := func(cl *testkit.Client) map[string]term {
		t.Helper()
		var out struct {
			Terms []term `json:"terms"`
		}
		cl.Must(http.StatusOK, http.MethodGet, "/api/v1/terms", nil).JSON(t, &out)
		m := map[string]term{}
		for _, x := range out.Terms {
			m[x.Key] = x
		}
		return m
	}
	rec := func(cl *testkit.Client, fields map[string]any) string {
		t.Helper()
		var out struct {
			Key string `json:"key"`
		}
		cl.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term", "term": fields}).JSON(t, &out)
		return out.Key
	}

	window := rec(w.reviewer, map[string]any{"name": "Settlement Window", "status": "approved",
		"definition": "The time in which Acme sends card payments to the bank."})
	status := w.scalar(t, `SELECT c.id FROM claims c JOIN entities e ON e.id = c.subject_id
		WHERE e.key = $1 AND c.predicate = 'status' AND c.retracted_at IS NULL`, window).(int64)
	var card struct {
		ID int64 `json:"proposal"`
	}
	w.member.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/problems", status), map[string]any{
		"problem": "wrong_value", "reason": "Acme stopped the settlement window in May.", "value": map[string]any{"text": "deprecated"}}).JSON(t, &card)
	if card.ID == 0 {
		t.Fatal("the report gave no card")
	}
	if got := terms(w.member)[window].Status; got != "approved" {
		t.Errorf("with an open report the status is %q, want approved", got)
	}
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", card.ID), map[string]any{})
	if got := terms(w.member)[window].Status; got != "deprecated" {
		t.Errorf("after the accepted correction the status is %q, want deprecated", got)
	}

	// The definition of a second term comes from acme-eng. An open report
	// of a new definition must not change it.
	cycle := rec(w.reviewer, map[string]any{"name": "Clearing Cycle", "status": "approved"})
	def := w.scalar(t, `
WITH s AS (SELECT id FROM entities WHERE key = $1),
     c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
           SELECT s.id, 'defined_as', 'The daily run that sends card payments to the bank.',
                  'the daily run that sends card payments to the bank' FROM s RETURNING id),
     p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p
           WHERE p.connection_id = $2 ORDER BY p.item_id, p.ordinal LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT (SELECT id FROM c), p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM p
RETURNING claim_id`, cycle, w.public).(int64)
	w.member.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/problems", def), map[string]any{
		"problem": "wrong_value", "reason": "The run is hourly.", "value": map[string]any{"text": "The hourly run of card payments."}})
	var defs struct {
		Terms []struct {
			Key        string `json:"key"`
			Definition string `json:"definition"`
		} `json:"terms"`
	}
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/terms", nil).JSON(t, &defs)
	for _, x := range defs.Terms {
		if x.Key == cycle && x.Definition != "The daily run that sends card payments to the bank." {
			t.Errorf("with an open report the definition is %q", x.Definition)
		}
	}

	// A page of acme-eng names a preferred term of a registry term. Only
	// acme-restricted gives the preferred term.
	hidden := "confluence:term:clearing-hour"
	w.exec(t, `
WITH e AS (INSERT INTO entities (type, key) VALUES ('term', $1) RETURNING id),
     s AS (INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $2 FROM e)
INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name) SELECT id, $2, 'Clearing Hour', 'clearing hour', true FROM e`,
		hidden, w.restricted)
	cutoff := rec(w.reviewer, map[string]any{"name": "Cut-off Hour", "status": "deprecated"})
	w.exec(t, `
WITH c AS (INSERT INTO claims (subject_id, predicate, object_entity_id)
           SELECT s.id, 'preferred_term', o.id FROM entities s, entities o WHERE s.key = $1 AND o.key = $2 RETURNING id),
     p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p
           WHERE p.connection_id = $3 ORDER BY p.item_id, p.ordinal LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT (SELECT id FROM c), p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM p`,
		cutoff, hidden, w.public)
	if got := terms(w.owner)[cutoff].Preferred; got != hidden {
		t.Errorf("the owner sees the preferred term %q, want %s", got, hidden)
	}
	if got := terms(w.member)[cutoff].Preferred; got != "" {
		t.Errorf("the member sees the preferred term %q, which only acme-restricted gives", got)
	}
}
