package round1

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (w *world) merge(t *testing.T, keys ...string) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{"keys": keys}).JSON(t, &out)
	return out.ID
}

// A reviewer who cannot see an entity of a merge must not split it, and
// must get the same 404 as for a missing merge.
func TestRVW_1_06_SplitOfMergeWithHiddenEntity(t *testing.T) {
	w := start(t)
	m := w.merge(t, w.key(t, w.public, "1003"), w.key(t, w.restricted, "2003"))

	missing := w.reviewer.Do(http.MethodPost, "/api/v1/merges/999999/split", nil)
	hidden := w.reviewer.Do(http.MethodPost, fmt.Sprintf("/api/v1/merges/%d/split", m), nil)
	if hidden.Status != missing.Status || hidden.Status != http.StatusNotFound {
		t.Errorf("reviewer split of a merge with a hidden entity: %d %s; missing merge: %d", hidden.Status, hidden.Body, missing.Status)
	}
	if n := w.env.Count(`SELECT count(*) FROM merges WHERE id = $1 AND undone_at IS NOT NULL`, m); n != 0 {
		t.Errorf("the reviewer split a merge that the reviewer cannot see")
	}
}

// Merge {a, b} (winner a), then merge {a, c} where c is older (winner c, b
// follows a to c). Split of the first merge must make b active again, or
// refuse; it must not report success and leave b merged.
func TestRVW_1_07_SplitAfterLaterMergeLeavesLoserMerged(t *testing.T) {
	w := start(t)
	pick := func(n int) string {
		return w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
			WHERE s.connection_id = $1 ORDER BY e.id OFFSET $2 LIMIT 1`, w.public, n).(string)
	}
	a, b, c := pick(0), pick(1), pick(2)
	w.exec(t, `UPDATE entities SET created_at = now() - interval '3 days' WHERE key = $1`, c)
	w.exec(t, `UPDATE entities SET created_at = now() - interval '2 days' WHERE key = $1`, a)
	w.exec(t, `UPDATE entities SET created_at = now() - interval '1 days' WHERE key = $1`, b)
	m1 := w.merge(t, a, b)
	w.merge(t, a, c)

	r := w.owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/merges/%d/split", m1), nil)
	merged := w.env.Count(`SELECT count(*) FROM entities WHERE key = $1 AND merged_into IS NOT NULL`, b)
	if r.Status < 300 && merged != 0 {
		t.Errorf("split of merge %d returned %d, but %s is still merged", m1, r.Status, b)
	}
}

// A reviewer who sees both entities merges a registry thing with an entity
// that only acme-restricted gives, and sets the canonical name. The display
// name rule shows a canonical name only to a reader who sees every merged
// entity, but the merge also renames the public registry record, so every
// member reads the name (SI-16: a merge never shows a hidden name).
func TestRVW_1_15_MergeCanonicalNameRenamesPublicRegistryRecord(t *testing.T) {
	w := start(t)
	var rec struct {
		Key string `json:"key"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{
		"kind": "thing", "thing": map[string]any{"type": "decision", "name": "Vendor selection"}}).JSON(t, &rec)
	hidden := w.key(t, w.restricted, "2003")
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{
		"keys": []string{rec.Key, hidden}, "canonicalName": "ADR-009 Select Falcon Typesetting"})

	for _, p := range []string{"/api/v1/entities/" + url.PathEscape(rec.Key), "/api/v1/registry/records/" + url.PathEscape(rec.Key)} {
		r := w.member.Do(http.MethodGet, p, nil)
		if strings.Contains(string(r.Body), "Falcon") {
			t.Errorf("member: GET %s shows the canonical name of a merge with a hidden entity: %.300s", p, r.Body)
		}
	}
}
