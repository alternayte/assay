package round1

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

// claim inserts a claim with one supporting evidence row on the first
// passage of an item of the connection, and returns the claim ID.
func (w *world) claim(t *testing.T, subject, predicate, object string, conn int64, item string) int64 {
	t.Helper()
	return w.scalar(t, `WITH c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
		SELECT id, $2, $3::text, lower($3::text) FROM entities WHERE key = $1 RETURNING id),
	p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p JOIN source_items i ON i.id = p.item_id
		WHERE i.connection_id = $4 AND i.external_id = $5 ORDER BY p.ordinal LIMIT 1)
	INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
	SELECT c.id, p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM c, p
	RETURNING claim_id`, subject, predicate, object, conn, item).(int64)
}

// key returns the key of the entity of a connection whose key ends with
// :suffix, such as a page ID.
func (w *world) key(t *testing.T, conn int64, suffix string) string {
	t.Helper()
	return w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 AND e.key LIKE '%:' || $2::text`, conn, suffix).(string)
}

type entityClaim struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// statusOf returns the status of a claim on the entity as the client reads
// it, or "absent".
func statusOf(t *testing.T, cl *testkit.Client, key string, id int64) string {
	t.Helper()
	var d struct {
		Claims   []entityClaim `json:"claims"`
		Timeline []entityClaim `json:"timeline"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(key), nil).JSON(t, &d)
	for _, c := range append(d.Claims, d.Timeline...) {
		if c.ID == id {
			return c.Status
		}
	}
	return "absent"
}

// A resolution chooses a claim that only the owner can see. For a member
// who sees only the public claim, that claim must not become Superseded:
// the status would show that hidden evidence exists.
func TestRVW_1_03_HiddenResolutionSupersedesVisibleClaim(t *testing.T) {
	w := start(t)
	adr := w.key(t, w.public, "1003")
	public := w.claim(t, adr, "decided", "Use PostgreSQL for job queues.", w.public, "1003")
	hidden := w.claim(t, adr, "decided", "Use a hosted queue.", w.restricted, "2003")
	if got := statusOf(t, w.member, adr, public); got != "supported" {
		t.Fatalf("before the resolution the member sees %s", got)
	}

	w.owner.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/resolution", hidden), nil)

	if got := statusOf(t, w.member, adr, public); got != "supported" {
		t.Errorf("member: the public claim is %s after a resolution for a claim the member cannot see, want supported", got)
	}
}

// A member proposes a finding "D2 supersedes D1". Before any reviewer
// decides, the claims of D1 must not become Superseded: the only evidence
// is an open proposal, so the claim is Proposed.
func TestRVW_1_04_OpenSupersedesProposalSupersedesDecision(t *testing.T) {
	w := start(t)
	d1, d2 := w.key(t, w.public, "1003"), w.key(t, w.public, "1004")
	fact := w.claim(t, d1, "decided", "Use PostgreSQL for job queues.", w.public, "1003")
	if got := statusOf(t, w.member, d1, fact); got != "supported" {
		t.Fatalf("before the finding the member sees %s", got)
	}
	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '1004'`, w.public)
	quote := w.scalar(t, `SELECT left(p.text, 40) FROM passages p WHERE p.item_id = $1 ORDER BY p.ordinal LIMIT 1`, item).(string)

	w.member.Must(http.StatusCreated, http.MethodPost, "/api/v1/findings", map[string]any{
		"subject": d2, "predicate": "supersedes", "object": map[string]any{"key": d1},
		"evidence": []map[string]any{{"itemId": item, "quote": quote}}, "note": "The retry policy replaces it.",
	})

	for _, cl := range []*testkit.Client{w.member, w.owner} {
		if got := statusOf(t, cl, d1, fact); got != "supported" {
			t.Errorf("an open supersedes proposal makes a claim of D1 %s, want supported", got)
		}
	}
}

// A reviewer resolves one value of a multi-valued predicate. The other
// values do not conflict with it and must not become Superseded.
func TestRVW_1_05_ResolutionOnMultiValuedPredicateSupersedesSiblings(t *testing.T) {
	w := start(t)
	adr := w.key(t, w.public, "1003")
	a := w.claim(t, adr, "rationale", "Jobs and data share one transaction.", w.public, "1003")
	b := w.claim(t, adr, "rationale", "The team already runs PostgreSQL.", w.public, "1003")

	w.owner.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/resolution", a), nil)

	if got := statusOf(t, w.member, adr, b); got != "supported" {
		t.Errorf("a second rationale is %s after a resolution of the first, want supported", got)
	}
}
