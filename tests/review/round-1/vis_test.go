package round1

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// An admin reads the audit log. The log must not carry source text of a
// connection outside the admin's visible set. The MCP audit entry stores
// the whole tool input, and the input of propose_finding holds quotes.
func TestRVW_1_01_AuditLogShowsRestrictedQuotesToOtherAdmin(t *testing.T) {
	w := start(t)
	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '2002'`, w.restricted)
	subject := w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 ORDER BY e.id LIMIT 1`, w.restricted)

	s := w.mcp(t, token(t, w.owner, "read", "propose"))
	if _, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "propose_finding", Arguments: map[string]any{
		"subject": subject, "predicate": "stated", "object": map[string]any{"text": "The debug log leaked card numbers."},
		"evidence": []map[string]any{{"item_id": item, "quote": "The incident CANARY-7Q4Z exposed test card numbers"}},
		"note":     "From the incident page.",
	}}); err != nil {
		t.Fatal(err)
	}

	// admin2 is an admin without acme-restricted in the visible set.
	r := w.admin2.Must(http.StatusOK, http.MethodGet, "/api/v1/audit?limit=200", nil)
	if hasCanary(r.Body) {
		t.Errorf("admin2 reads restricted source text in the audit log: …%s…", excerpt(r.Body))
	}
}

// A reviewer merges a public entity with an older restricted entity. The
// restricted entity wins (the oldest wins). A member who reads the public
// key must not learn the key of the restricted entity, nor that it exists.
func TestRVW_1_02_MergeIntoRestrictedWinnerShowsHiddenKey(t *testing.T) {
	w := start(t)
	// Two decision records: ADR-001 in acme-eng and ADR-009 in acme-restricted.
	public := w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 AND e.key LIKE '%:1003'`, w.public).(string)
	hidden := w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 AND e.key LIKE '%:2003'`, w.restricted).(string)
	w.exec(t, `UPDATE entities SET created_at = created_at - interval '1 day' WHERE key = $1`, hidden)

	type detail struct {
		Key     string `json:"key"`
		Claims  []any  `json:"claims"`
		Sources []any  `json:"sources"`
	}
	var before, after detail
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(public), nil).JSON(t, &before)

	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{"keys": []string{public, hidden}})

	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(public), nil).JSON(t, &after)
	if len(after.Claims) < len(before.Claims) || len(after.Sources) < len(before.Sources) {
		t.Errorf("member: the public entity had %d claims and %d sources before the merge, and %d and %d after",
			len(before.Claims), len(before.Sources), len(after.Claims), len(after.Sources))
	}
	for _, p := range []string{"/api/v1/entities/" + url.PathEscape(public), "/api/v1/entities?q=" + url.QueryEscape(public),
		"/api/v1/entities/" + url.PathEscape(public) + "/page"} {
		r := w.member.Do(http.MethodGet, p, nil)
		if strings.Contains(string(r.Body), hidden) {
			t.Errorf("GET %s as member: %d, shows the restricted key %s: %s", p, r.Status, hidden, r.Body)
		}
	}
}

// The owner proposes a finding about an entity that only the owner can see,
// with a quote from a public item. The card has only the public connection,
// so a reviewer without the restricted connection gets it. The card must
// not name the hidden entity (REQ-REV-002, SI-02).
func TestRVW_1_14_FindingCardNamesHiddenSubject(t *testing.T) {
	w := start(t)
	hidden := w.key(t, w.restricted, "2003")
	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '1003'`, w.public)
	quote := w.scalar(t, `SELECT left(p.text, 40) FROM passages p WHERE p.item_id = $1 ORDER BY p.ordinal LIMIT 1`, item).(string)
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/findings", map[string]any{
		"subject": hidden, "predicate": "decided", "object": map[string]any{"text": "Keep the hosted queue."},
		"evidence": []map[string]any{{"itemId": item, "quote": quote}}})

	r := w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/cards", nil)
	if strings.Contains(string(r.Body), hidden) || strings.Contains(string(r.Body), "hosted queue") {
		t.Errorf("reviewer reads a card about an entity that the reviewer cannot see: %s", r.Body)
	}
}
