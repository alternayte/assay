package round2

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (w *world) merge(t *testing.T, keys []string, canonical string) int64 {
	t.Helper()
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{"keys": keys}
	if canonical != "" {
		body["canonicalName"] = canonical
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", body).JSON(t, &out)
	return out.ID
}

// The owner first merges a restricted entity into a public one (the public
// one is older and wins). Then the owner merges a registry record with the
// public entity and sets a canonical name. Both merged winners have a
// public source, but the restricted entity now also belongs to the merge.
// The public registry record must not take the canonical name (SI-16): a
// member does not see every merged entity.
func TestRVW_2_01_NestedMergeRenamesPublicRegistryRecord(t *testing.T) {
	w := start(t)
	public, hidden := w.key(t, w.public, "1003"), w.key(t, w.restricted, "2003")
	w.older(t, public, 3)
	w.merge(t, []string{public, hidden}, "")

	var rec struct {
		Key string `json:"key"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{
		"kind": "thing", "thing": map[string]any{"type": "decision", "name": "Vendor selection"}}).JSON(t, &rec)
	w.merge(t, []string{rec.Key, public}, "ADR-009 Select Falcon Typesetting")

	for _, p := range []string{"/api/v1/entities/" + url.PathEscape(rec.Key), "/api/v1/registry/records/" + url.PathEscape(rec.Key)} {
		r := w.member.Do(http.MethodGet, p, nil)
		if strings.Contains(string(r.Body), "Falcon") {
			t.Errorf("member: GET %s shows the canonical name of a merge that holds a hidden entity: %.300s", p, r.Body)
		}
	}
}

// Merge {a, b} (winner a), then merge {a, c} where c is older (winner c; b
// follows a to c). The split route says "Split the later merge first".
// After the split of the later merge, b must be merged into a again (the
// first merge is still active), and the first merge must split.
func TestRVW_2_02_SplitOfLaterMergeLeavesEarlierLoserUnderOtherWinner(t *testing.T) {
	w := start(t)
	pick := func(n int) string {
		return w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
			WHERE s.connection_id = $1 ORDER BY e.id OFFSET $2 LIMIT 1`, w.public, n).(string)
	}
	a, b, c := pick(0), pick(1), pick(2)
	w.older(t, c, 3)
	w.older(t, a, 2)
	w.older(t, b, 1)
	m1 := w.merge(t, []string{a, b}, "")
	m2 := w.merge(t, []string{a, c}, "")

	w.owner.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/merges/%d/split", m2), nil)
	into := w.scalar(t, `SELECT coalesce((SELECT x.key FROM entities e JOIN entities x ON x.id = e.merged_into WHERE e.key = $1), '')`, b)
	if into != a {
		t.Errorf("after the split of merge %d, %s is merged into %q, want %s (merge %d is still active)", m2, b, into, a, m1)
	}
	r := w.owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/merges/%d/split", m1), nil)
	if r.Status >= 300 {
		t.Errorf("split of merge %d after the split of the later merge: %d %s", m1, r.Status, r.Body)
	}
}

// The owner merges a public entity into an older restricted entity (the
// restricted entity wins). A reviewer who sees only public content then
// merges the public key with another public entity. The response names the
// winner, and it must not be the key of the restricted entity (SI-02).
func TestRVW_2_03_MergeResponseShowsHiddenWinnerKey(t *testing.T) {
	w := start(t)
	public, other, hidden := w.key(t, w.public, "1003"), w.key(t, w.public, "1004"), w.key(t, w.restricted, "2003")
	w.older(t, hidden, 3)
	w.merge(t, []string{public, hidden}, "")

	r := w.reviewer.Do(http.MethodPost, "/api/v1/merges", map[string]any{"keys": []string{public, other}})
	if strings.Contains(string(r.Body), hidden) {
		t.Errorf("reviewer: POST /api/v1/merges returns the restricted key %s: %d %s", hidden, r.Status, r.Body)
	}
	r = w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(other), nil)
	if strings.Contains(string(r.Body), hidden) {
		t.Errorf("reviewer: GET of the merged entity shows the restricted key %s", hidden)
	}
}

// The owner merges a restricted entity into a public one, then merges that
// public entity into an older public entity with a canonical name. The
// second merge lists only public entities as losers, but the restricted
// entity now belongs to it. The display name rule hides the canonical name
// from the member; the list of merges on the entity must hide it too.
func TestRVW_2_07_NestedMergeListShowsCanonicalNameOfHiddenEntity(t *testing.T) {
	w := start(t)
	a, b, hidden := w.key(t, w.public, "1003"), w.key(t, w.public, "1004"), w.key(t, w.restricted, "2003")
	w.older(t, a, 3)
	w.older(t, b, 2)
	w.merge(t, []string{b, hidden}, "")
	w.merge(t, []string{a, b}, "ADR-009 Select Falcon Typesetting")

	r := w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(a), nil)
	if strings.Contains(string(r.Body), "Falcon") {
		t.Errorf("member: GET of the entity shows the canonical name of a merge that holds a hidden entity: %.400s", r.Body)
	}
}
