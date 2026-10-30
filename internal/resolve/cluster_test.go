package resolve_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
)

// pairCard adds a same_entity decision with a merge card for two keys.
func (w *world) pairCard(t *testing.T, a, b string, p float64, band string) {
	t.Helper()
	w.exec(t, `
WITH d AS (INSERT INTO decisions (kind, subject_refs, input_hash, probs, raw_p, band, connections)
           VALUES ('same_entity', ARRAY[$1, $2], sha256(random()::text::bytea), '{}', $3, $4, ARRAY[$5::bigint]) RETURNING id)
INSERT INTO proposals (kind, target, payload, via, connections, decision_id)
SELECT 'merge', 'entity:' || $1, '{}', 'system', ARRAY[$5::bigint], id FROM d`, a, b, p, band, w.jira)
}

// service adds a service entity with one name from Jira.
func (w *world) service(t *testing.T, key, name string) {
	t.Helper()
	w.exec(t, `
WITH e AS (INSERT INTO entities (type, key) VALUES ('service', $1) RETURNING id),
     s AS (INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $3 FROM e)
INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name) SELECT id, $3, $2, lower($2), true FROM e`,
		key, name, w.jira)
}

func TestSCN_REV_008_ClusterCard(t *testing.T) {
	w := start(t)
	a, b, c := "jira:test:service:ledger-a", "jira:test:service:ledger-b", "jira:test:service:ledger-c"
	w.service(t, a, "Ledger")
	w.service(t, b, "Ledger service")
	w.service(t, c, "The ledger")
	w.pairCard(t, a, b, 0.97, "high")
	w.pairCard(t, b, c, 0.6, "medium")
	w.pairCard(t, a, c, 0.55, "medium")

	// Three linked duplicates give one cluster card.
	var list struct {
		Cards []struct {
			ID      int64     `json:"id"`
			Band    string    `json:"band"`
			Members []subject `json:"members"`
			Pairs   []int64   `json:"pairs"`
		} `json:"cards"`
	}
	w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/cards?kind=merge", nil).JSON(t, &list)
	var cluster int64
	for _, cd := range list.Cards {
		keys := []string{}
		for _, m := range cd.Members {
			keys = append(keys, m.Key)
		}
		if slices.Contains(keys, a) || slices.Contains(keys, b) || slices.Contains(keys, c) {
			if cluster != 0 {
				t.Fatalf("more than one card for the cluster: %+v", list.Cards)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, []string{a, b, c}) || len(cd.Pairs) != 3 || cd.Band != "medium" {
				t.Fatalf("the cluster card is %+v", cd)
			}
			cluster = cd.ID
		}
	}
	if cluster == 0 {
		t.Fatal("no cluster card")
	}

	// Members outside the cluster, or fewer than two, fail.
	path := fmt.Sprintf("/api/v1/cards/%d/accept", cluster)
	w.reviewer.Must(http.StatusUnprocessableEntity, http.MethodPost, path, map[string]any{"members": []string{a}})
	w.reviewer.Must(http.StatusUnprocessableEntity, http.MethodPost, path, map[string]any{"members": []string{a, "backstage:component:default/payments-api"}})

	// Accepting two with a canonical name merges those two.
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, path, map[string]any{"members": []string{a, b}, "canonicalName": "Ledger Service"})
	var winner, name string
	var losers []string
	if err := w.env.Server.Pool().QueryRow(context.Background(), `
SELECT w.key, m.canonical_name, array(SELECT e.key FROM entities e WHERE e.id = ANY(m.loser_ids) ORDER BY e.key)
FROM merges m JOIN entities w ON w.id = m.winner_id WHERE m.undone_at IS NULL`).Scan(&winner, &name, &losers); err != nil {
		t.Fatal(err)
	}
	if name != "Ledger Service" || len(losers) != 1 || !slices.Contains([]string{a, b}, winner) || !slices.Contains([]string{a, b}, losers[0]) {
		t.Fatalf("merge: winner %s, losers %v, name %q", winner, losers, name)
	}
	if n := w.env.Count(`SELECT count(*) FROM entities WHERE key = $1 AND merged_into IS NULL`, c); n != 1 {
		t.Fatal("the third duplicate was merged")
	}
	if n := w.env.Count(`SELECT count(*) FROM proposals WHERE kind = 'merge' AND status = 'accepted'`); n != 1 {
		t.Fatalf("%d accepted pair cards, want 1", n)
	}
	if n := w.env.Count(`SELECT count(*) FROM decisions WHERE kind = 'same_entity' AND outcome = 'accepted'`); n != 1 {
		t.Fatalf("%d accepted decisions, want 1", n)
	}
}
