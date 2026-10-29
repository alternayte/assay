package resolve_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

const reminder = "backstage:component:default/reminder-service"

// stated adds a stated claim of the reminder service with evidence on the
// passage of page 1099 that contains the quote.
func (w *world) stated(t *testing.T, text, quote string) int64 {
	t.Helper()
	var id int64
	err := w.env.Server.Pool().QueryRow(context.Background(), `
WITH c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
           SELECT id, 'stated', $1, lower($1) FROM entities WHERE key = $3 RETURNING id),
     p AS (SELECT p.item_id, p.hash, p.connection_id FROM passages p JOIN source_items i ON i.id = p.item_id
           WHERE i.external_id = '1099' AND strpos(p.norm, $2) > 0 LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, quote, source_type)
SELECT c.id, p.connection_id, p.item_id, p.hash, 'passage', $2, 'confluence' FROM c, p RETURNING claim_id`, text, quote, reminder).Scan(&id)
	if err != nil {
		t.Fatalf("claim %q: %v", text, err)
	}
	return id
}

func TestSCN_RES_008_StaleEvidenceMovesOnHigh(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	page := fixtures.Page{ID: "1099", Space: "ENG", Title: "Reminder timing", Author: "jo.acme",
		Body: "<p>The reminder service sends a reminder after seven days.</p><p>The reminder service runs at 06:00 UTC.</p>"}
	w.src.PutPage(page)
	public := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: w.base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	testkit.ShareWithEveryone(t, w.env, w.owner, public)
	sync := func() {
		if r := w.env.RunNow(w.owner, public, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync: %+v", r)
		}
	}
	sync()
	moved := w.stated(t, "sends a reminder after seven days", "The reminder service sends a reminder after seven days.")
	kept := w.stated(t, "runs at 06:00 UTC", "The reminder service runs at 06:00 UTC.")
	unused := w.stated(t, "sends reminders", "The reminder service sends a reminder")
	w.exec(t, `INSERT INTO questions (user_id, text, flags) SELECT id, 'When do reminders go out?', $1::jsonb FROM auth_users LIMIT 1`,
		fmt.Sprintf(`{"claimIds":[%d,%d]}`, moved, kept))

	page.Body = "<p>The reminder service sends a reminder after seven days and a second one after fourteen days.</p>" +
		"<p>The reminder service runs at 07:00 UTC.</p>"
	w.src.PutPage(page)
	sync()
	if n := w.env.Count("SELECT count(*) FROM claim_evidence WHERE state = 'stale' AND claim_id = ANY($1)", []int64{moved, kept, unused}); n != 3 {
		t.Fatalf("%d stale evidence rows after the edit, want 3", n)
	}

	fake, _, decisions := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "decide", "decisions", decisions, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "decide", testkit.Function{Endpoint: ep, Model: "fake-decide"})
	fake.OnDecide(func(_ string, _ map[string]any, state map[string]any) map[string]any {
		passage, _ := state["passage"].(string)
		if passage == "" {
			return nil
		}
		p := 0.1
		if strings.Contains(passage, "fourteen days") && strings.Contains(fmt.Sprint(state["claim"]), "seven days") {
			p = 0.95
		}
		return map[string]any{"type": "noul", "noul": p}
	})
	r := w.env.Server.Resolve()
	if err := r.StillSupports(ctx); err != nil {
		t.Fatal(err)
	}
	var state, quote string
	if err := w.env.Server.Pool().QueryRow(ctx, `SELECT e.state, e.quote FROM claim_evidence e WHERE e.claim_id = $1`, moved).Scan(&state, &quote); err != nil {
		t.Fatal(err)
	}
	if state != "current" || !strings.Contains(quote, "fourteen days") {
		t.Errorf("the High claim has evidence %s %q, want current on the new passage", state, quote)
	}
	if n := w.env.Count(`SELECT count(*) FROM claim_evidence e JOIN passages p ON p.item_id = e.item_id AND p.hash = e.passage_hash
WHERE e.claim_id = $1 AND strpos(p.norm, e.quote) > 0`, moved); n != 1 {
		t.Error("the moved quote is not part of the new passage")
	}
	if n := w.env.Count("SELECT count(*) FROM claim_evidence WHERE claim_id = $1 AND state = 'stale'", kept); n != 1 {
		t.Error("the Low claim does not stay stale")
	}
	if n := w.env.Count("SELECT count(*) FROM decisions WHERE kind = 'still_supports'"); n != 2 {
		t.Errorf("%d still_supports decisions, want 2: a claim not in use makes no call", n)
	}
	calls := len(fake.Requests())
	if err := r.StillSupports(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Requests()); n != calls {
		t.Errorf("a repeat run made %d calls", n-calls)
	}
}
