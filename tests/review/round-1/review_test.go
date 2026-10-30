package round1

import (
	"fmt"
	"net/http"
	"testing"
)

// Reject and "Needs evidence" need a reason (REQ-REV-004). A reason of
// spaces only is no reason.
func TestRVW_1_16_RejectWithBlankReason(t *testing.T) {
	w := start(t)
	adr := w.key(t, w.public, "1003")
	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '1003'`, w.public)
	quote := w.scalar(t, `SELECT left(p.text, 40) FROM passages p WHERE p.item_id = $1 ORDER BY p.ordinal LIMIT 1`, item).(string)
	var f struct {
		Proposal int64 `json:"proposal"`
	}
	w.member.Must(http.StatusCreated, http.MethodPost, "/api/v1/findings", map[string]any{
		"subject": adr, "predicate": "decided", "object": map[string]any{"text": "Use PostgreSQL."},
		"evidence": []map[string]any{{"itemId": item, "quote": quote}}}).JSON(t, &f)

	for _, action := range []string{"reject", "needs-evidence"} {
		r := w.reviewer.Do(http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/%s", f.Proposal, action), map[string]string{"reason": "   "})
		if r.Status < 400 {
			t.Errorf("%s with a blank reason: %d, want 422", action, r.Status)
			return
		}
	}
}
