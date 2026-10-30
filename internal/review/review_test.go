package review_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

type world struct {
	env                     *testkit.Env
	owner, reviewer, member *testkit.Client
	src                     *fixtures.Sources
	base                    string
}

// start syncs GitHub and Jira, shared with Everyone, and the restricted
// Confluence space of the owner.
func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.reviewer, _ = env.Invite(w.owner, "reviewer@acme.test", "reviewer", "reviewer-password-1")
	w.member, _ = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	src, base := testkit.FakeSources(t)
	w.src, w.base = src, base
	mk := func(name, kind, path string, scope map[string]any, share bool) {
		id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: name, Kind: kind, BaseURL: base, Path: path, Scope: scope})
		if share {
			testkit.ShareWithEveryone(t, env, w.owner, id)
		}
		if r := env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %s: %+v", name, r)
		}
	}
	mk("acme-github", "github", "", map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}, true)
	mk("acme-jira", "jira", "", map[string]any{"projects": []string{"PAY", "REM"}}, true)
	mk("acme-restricted", "confluence", "/confluence", map[string]any{"spaces": []string{"RST"}}, false)
	return w
}

func (w *world) scalar(t *testing.T, sql string, args ...any) any {
	t.Helper()
	var v any
	if err := w.env.Server.Pool().QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

type claim struct {
	ID         int64  `json:"id"`
	Predicate  string `json:"predicate"`
	ObjectKey  string `json:"objectKey"`
	ObjectText string `json:"objectText"`
	Status     string `json:"status"`
}

func entityClaims(t *testing.T, cl *testkit.Client, key string) []claim {
	t.Helper()
	var d struct {
		Claims []claim `json:"claims"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(key), nil).JSON(t, &d)
	return d.Claims
}

func find(cs []claim, predicate, object string) *claim {
	for i, c := range cs {
		if c.Predicate == predicate && (c.ObjectKey == object || c.ObjectText == object) {
			return &cs[i]
		}
	}
	return nil
}

type proposal struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func mine(t *testing.T, cl *testkit.Client, id int64) proposal {
	t.Helper()
	var out struct {
		Proposals []proposal `json:"proposals"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/proposals", nil).JSON(t, &out)
	for _, p := range out.Proposals {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("proposal %d is not in the list of the proposer: %+v", id, out.Proposals)
	return proposal{}
}

const (
	paymentsAPI   = "backstage:component:default/payments-api"
	paymentsTeam  = "backstage:group:default/payments-team"
	remindersTeam = "backstage:group:default/reminders-team"
)

func (w *world) report(t *testing.T, claimID int64, body map[string]any) int64 {
	t.Helper()
	var out struct {
		Proposal int64 `json:"proposal"`
	}
	w.member.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/problems", claimID), body).JSON(t, &out)
	return out.Proposal
}

func TestSCN_REV_003_ReasonsAndResults(t *testing.T) {
	w := start(t)
	owner := find(entityClaims(t, w.member, paymentsAPI), "owned_by", paymentsTeam)
	if owner == nil {
		t.Fatal("no owned_by claim of the Payments API")
	}
	wrong := map[string]any{"problem": "wrong_value", "reason": "The reminders team owns the service since the move.",
		"value": map[string]any{"key": remindersTeam}, "source": map[string]any{"note": "Team chart of the payments area."}}

	// A wrong value needs a value, and every report needs a reason.
	path := fmt.Sprintf("/api/v1/claims/%d/problems", owner.ID)
	w.member.Must(http.StatusUnprocessableEntity, http.MethodPost, path, map[string]any{"problem": "wrong_value", "reason": "Wrong."})
	w.member.Must(http.StatusUnprocessableEntity, http.MethodPost, path, map[string]any{"problem": "outdated", "reason": ""})

	// Reject and Needs evidence without a reason fail.
	first := w.report(t, owner.ID, wrong)
	card := fmt.Sprintf("/api/v1/cards/%d", first)
	w.reviewer.Must(http.StatusUnprocessableEntity, http.MethodPost, card+"/reject", map[string]any{"reason": ""})
	w.reviewer.Must(http.StatusUnprocessableEntity, http.MethodPost, card+"/needs-evidence", map[string]any{})
	if p := mine(t, w.member, first); p.Status != "open" {
		t.Fatalf("after failed decisions the card is %s", p.Status)
	}

	// The proposed value shows as Proposed until a decision.
	if c := find(entityClaims(t, w.member, paymentsAPI), "owned_by", remindersTeam); c == nil || c.Status != "proposed" {
		t.Fatalf("the proposed owner is %+v", c)
	}

	// With a reason, the proposer sees the result.
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, card+"/needs-evidence", map[string]any{"reason": "Add a link to the team chart."})
	if p := mine(t, w.member, first); p.Status != "needs_evidence" || p.Reason != "Add a link to the team chart." {
		t.Fatalf("needs evidence: %+v", p)
	}
	if c := find(entityClaims(t, w.member, paymentsAPI), "owned_by", remindersTeam); c != nil {
		t.Fatalf("a proposal that needs evidence still supports its value: %+v", c)
	}
	second := w.report(t, owner.ID, wrong)
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/reject", second), map[string]any{"reason": "The catalog is right."})
	if p := mine(t, w.member, second); p.Status != "rejected" || p.Reason != "The catalog is right." {
		t.Fatalf("rejected: %+v", p)
	}

	// An accepted correction gives a human claim that wins, and the old
	// claim is Superseded.
	third := w.report(t, owner.ID, wrong)
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", third), map[string]any{})
	if p := mine(t, w.member, third); p.Status != "accepted" {
		t.Fatalf("accepted: %+v", p)
	}
	cs := entityClaims(t, w.member, paymentsAPI)
	if c := find(cs, "owned_by", remindersTeam); c == nil || c.Status != "supported" {
		t.Fatalf("the new owner is %+v", c)
	}
	if c := find(cs, "owned_by", paymentsTeam); c == nil || c.Status != "superseded" {
		t.Fatalf("the old owner is %+v", c)
	}
	if n := w.scalar(t, "SELECT count(*) FROM resolutions WHERE chosen_claim_id IN (SELECT id FROM claims WHERE origin = 'human')"); n != int64(1) {
		t.Fatalf("%v resolutions for the human claim", n)
	}

	// An outdated claim with no value gets disputed on accept.
	stated := find(cs, "depends_on", "backstage:resource:default/payments-db")
	if stated == nil {
		t.Fatal("no depends_on claim of the Payments API")
	}
	fourth := w.report(t, stated.ID, map[string]any{"problem": "outdated", "reason": "The service moved to the ledger store."})
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", fourth), map[string]any{})
	if c := find(entityClaims(t, w.member, paymentsAPI), "depends_on", "backstage:resource:default/payments-db"); c == nil || c.Status != "conflicting" {
		t.Fatalf("the outdated claim is %+v", c)
	}
	if n := w.scalar(t, "SELECT count(*) FROM audit_log WHERE action LIKE 'cards.%'"); n != int64(4) {
		t.Fatalf("%v card audit rows, want 4", n)
	}
}

func TestSCN_REV_004_FindingNeedsVisibleItemsAndExactQuotes(t *testing.T) {
	w := start(t)
	item := w.scalar(t, "SELECT id FROM source_items WHERE external_id = 'PAY-1'").(int64)
	text := w.scalar(t, `SELECT p.text FROM passages p WHERE p.item_id = $1 AND length(p.text) > 40 ORDER BY p.ordinal LIMIT 1`, item).(string)
	quote := strings.TrimSpace(text[:30])
	hidden := w.scalar(t, "SELECT id FROM source_items WHERE external_id = '2002'").(int64)
	var es struct {
		Entities []struct {
			Key string `json:"key"`
		} `json:"entities"`
	}
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities?q=Payment%20retries&type=initiative", nil).JSON(t, &es)
	if len(es.Entities) == 0 {
		t.Fatal("no initiative")
	}
	subject := es.Entities[0].Key
	finding := func(itemID int64, q string) map[string]any {
		return map[string]any{"subject": subject, "predicate": "stated", "object": map[string]any{"text": "Retries back off for one hour."},
			"evidence": []map[string]any{{"itemId": itemID, "quote": q}}}
	}

	r := w.member.Do(http.MethodPost, "/api/v1/findings", finding(item, quote+" and more words that are not there"))
	if r.Status != http.StatusUnprocessableEntity || r.Code() != "quote" {
		t.Fatalf("wrong quote: %d %s", r.Status, r.Body)
	}
	w.member.Must(http.StatusNotFound, http.MethodPost, "/api/v1/findings", finding(hidden, "CANARY"))
	w.member.Must(http.StatusNotFound, http.MethodPost, "/api/v1/findings", map[string]any{"subject": "jira:nowhere:PAY-404",
		"predicate": "stated", "object": map[string]any{"text": "x"}, "evidence": []map[string]any{{"itemId": item, "quote": quote}}})
	w.member.Must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/findings", map[string]any{"subject": subject,
		"predicate": "owned_by", "object": map[string]any{"text": "x"}, "evidence": []map[string]any{{"itemId": item, "quote": quote}}})

	var out struct {
		Proposal int64 `json:"proposal"`
		Claim    int64 `json:"claim"`
	}
	w.member.Must(http.StatusCreated, http.MethodPost, "/api/v1/findings", finding(item, quote)).JSON(t, &out)
	if p := mine(t, w.member, out.Proposal); p.Kind != "finding" || p.Status != "open" {
		t.Fatalf("finding: %+v", p)
	}
	c := find(entityClaims(t, w.member, subject), "stated", "Retries back off for one hour.")
	if c == nil || c.Status != "proposed" || c.ID != out.Claim {
		t.Fatalf("the finding claim is %+v", c)
	}
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", out.Proposal), map[string]any{})
	if c := find(entityClaims(t, w.member, subject), "stated", "Retries back off for one hour."); c == nil || c.Status != "supported" {
		t.Fatalf("the accepted finding claim is %+v", c)
	}
}
