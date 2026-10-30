package review_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/app"
)

func TestSCN_REV_006_DecisionsWriteAuditRows(t *testing.T) {
	w := start(t)
	owner := find(entityClaims(t, w.member, paymentsAPI), "owned_by", paymentsTeam)
	if owner == nil {
		t.Fatal("no owned_by claim of the Payments API")
	}
	report := map[string]any{"problem": "outdated", "reason": "The team changed its name."}
	first, second := w.report(t, owner.ID, report), w.report(t, owner.ID, report)
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/reject", first), map[string]any{"reason": "No source."})
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/needs-evidence", second), map[string]any{"reason": "Add a link."})

	// Admins filter by action.
	type row struct {
		Action string         `json:"action"`
		Target string         `json:"target"`
		Actor  string         `json:"actorEmail"`
		Detail map[string]any `json:"details"`
	}
	var out struct {
		Entries []row `json:"entries"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/audit?action=cards", nil).JSON(t, &out)
	got := map[string]row{}
	for _, e := range out.Entries {
		if !strings.HasPrefix(e.Action, "cards.") {
			t.Fatalf("the filter cards gave %s", e.Action)
		}
		got[e.Action+" "+e.Target] = e
	}
	rejected, ok := got[fmt.Sprintf("cards.rejected card:%d", first)]
	if !ok || rejected.Detail["reason"] != "No source." {
		t.Fatalf("no audit row for the rejection: %+v", out.Entries)
	}
	if _, ok := got[fmt.Sprintf("cards.needs_evidence card:%d", second)]; !ok {
		t.Fatalf("no audit row for Needs evidence: %+v", out.Entries)
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/audit?action=review.report", nil).JSON(t, &out)
	if len(out.Entries) != 2 {
		t.Fatalf("%d report rows, want 2", len(out.Entries))
	}
	w.reviewer.Must(http.StatusForbidden, http.MethodGet, "/api/v1/audit", nil)

	// No route changes or deletes audit rows.
	doc, err := app.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}
	for path, ops := range spec.Paths {
		if !strings.Contains(path, "audit") {
			continue
		}
		for method := range ops {
			if method != "get" {
				t.Errorf("%s %s changes the audit log", strings.ToUpper(method), path)
			}
		}
	}
}
