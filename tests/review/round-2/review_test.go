package round2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The owner proposes and accepts a finding about an entity that only the
// owner sees, with a quote from a public item. Then the owner reports a
// problem with that claim. The correction card has only the connections of
// the claim evidence, so a reviewer without the restricted connection gets
// it, with the key of the hidden subject (REQ-REV-002, SI-02).
func TestRVW_2_04_CorrectionCardNamesHiddenSubject(t *testing.T) {
	w := start(t)
	hidden := w.key(t, w.restricted, "2003")
	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '1003'`, w.public)
	quote := w.scalar(t, `SELECT left(p.text, 40) FROM passages p WHERE p.item_id = $1 ORDER BY p.ordinal LIMIT 1`, item).(string)
	var f struct {
		Proposal int64 `json:"proposal"`
		Claim    int64 `json:"claim"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/findings", map[string]any{
		"subject": hidden, "predicate": "decided", "object": map[string]any{"text": "Keep the hosted queue."},
		"evidence": []map[string]any{{"itemId": item, "quote": quote}}}).JSON(t, &f)
	w.owner.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", f.Proposal), map[string]any{})

	w.owner.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/problems", f.Claim), map[string]any{
		"problem": "outdated", "reason": "The vendor changed."})

	r := w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/cards", nil)
	if strings.Contains(string(r.Body), hidden) {
		t.Errorf("reviewer reads a correction card about an entity that the reviewer cannot see: %.600s", r.Body)
	}
}

// admin2 reads the audit log but does not see acme-restricted. The MCP
// audit entry of propose_finding drops quotes, but it keeps the subject key
// and the object text, and the merge entry keeps the keys and the
// canonical name. All of them name restricted content (SI-01, SI-16).
func TestRVW_2_05_AuditLogShowsRestrictedKeysAndNames(t *testing.T) {
	w := start(t)
	hidden := w.key(t, w.restricted, "2003")
	other := w.key(t, w.public, "1003")
	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '2003'`, w.restricted)

	s := w.mcp(t, token(t, w.owner, "read", "propose"))
	if _, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "propose_finding", Arguments: map[string]any{
		"subject": hidden, "predicate": "decided", "object": map[string]any{"text": "Acme selects Falcon Typesetting."},
		"evidence": []map[string]any{{"item_id": item, "quote": "Acme selects Falcon Typesetting as the vendor"}},
		"note":     "From the decision page.",
	}}); err != nil {
		t.Fatal(err)
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{
		"keys": []string{hidden, other}, "canonicalName": "Falcon Typesetting"})

	var log struct {
		Entries []struct {
			Action  string          `json:"action"`
			Target  string          `json:"target"`
			Details json.RawMessage `json:"details"`
		} `json:"entries"`
	}
	w.admin2.Must(http.StatusOK, http.MethodGet, "/api/v1/audit?limit=200", nil).JSON(t, &log)
	for _, e := range log.Entries {
		s := e.Target + " " + string(e.Details)
		if strings.Contains(s, hidden) || strings.Contains(s, "Falcon") {
			t.Errorf("admin2 reads restricted keys or names in the audit entry %s: %s", e.Action, s)
		}
	}
}
