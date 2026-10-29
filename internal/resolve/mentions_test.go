package resolve_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/extract"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

const (
	peer = "backstage:component:default/peer-review-service"
	pay  = "backstage:component:default/payments-api"
)

// mentionWorld syncs a public page with three mentions and extracts it:
// a description of the reminder service, a description of nothing known
// and the name Sam Acme, which two people share.
func mentionWorld(t *testing.T) (*world, *fixtures.Models) {
	t.Helper()
	w := start(t)
	w.src.PutPage(fixtures.Page{ID: "1099", Space: "ENG", Title: "Due date readers", Author: "jo.acme",
		Body: "<p>The service that sends reminder emails reads due dates from the Peer Review Service.</p>" +
			"<p>The nightly export job reads due dates from the Peer Review Service too.</p>" +
			"<p>The Payments API is owned by Sam Acme.</p>"})
	public := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: w.base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	testkit.ShareWithEveryone(t, w.env, w.owner, public)
	if r := w.env.RunNow(w.owner, public, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	fake, openai, decisions := testkit.FakeModels(t)
	chat := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "extract", testkit.Function{Endpoint: chat, Model: "fake-chat"})
	dec := testkit.CreateEndpoint(t, w.owner, "decide", "decisions", decisions, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "decide", testkit.Function{Endpoint: dec, Model: "fake-decide"})
	claim := func(subject, predicate, object, quote string) string {
		return fmt.Sprintf(`{"subject":%s,"predicate":%q,"object":%s,"scope":null,"stance":"supports","quote":%q}`, subject, predicate, object, quote)
	}
	fake.OnChat(func(body map[string]any) string {
		user := body["messages"].([]any)[1].(map[string]any)["content"].(string)
		switch {
		case strings.Contains(user, "The service that sends reminder emails"):
			return `{"claims":[` + claim(`{"kind":"description","value":"the service that sends reminder emails"}`, "depends_on",
				`{"kind":"key","value":"`+peer+`"}`, "The service that sends reminder emails reads due dates from the Peer Review Service.") + `]}`
		case strings.Contains(user, "The nightly export job"):
			return `{"claims":[` + claim(`{"kind":"description","value":"the nightly export job"}`, "depends_on",
				`{"kind":"key","value":"`+peer+`"}`, "The nightly export job reads due dates from the Peer Review Service too.") + `]}`
		case strings.Contains(user, "owned by Sam Acme"):
			return `{"claims":[` + claim(`{"kind":"key","value":"`+pay+`"}`, "owned_by",
				`{"kind":"name","value":"Sam Acme"}`, "The Payments API is owned by Sam Acme.") + `]}`
		}
		return `{"claims":[]}`
	})
	fake.OnDecide(func(_ string, q map[string]any, state map[string]any) map[string]any {
		if c, ok := state["candidate"].(string); ok {
			p := 0.1
			if strings.HasPrefix(c, "Name: Reminder service") && strings.Contains(fmt.Sprint(state["description"]), "reminder emails") {
				p = 0.95
			}
			return map[string]any{"type": "noul", "noul": p}
		}
		if q["type"] == "choice" {
			crit, _ := q["criteria"].(map[string]any)
			probs := map[string]any{}
			for k, v := range crit {
				probs[k] = 0.02
				if strings.HasPrefix(fmt.Sprint(v), "Name: sam.acme") {
					probs[k] = 0.94
				}
			}
			return map[string]any{"type": "choice", "probabilities": probs}
		}
		return nil
	})
	if _, err := w.env.Server.Resolve().Profiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := w.env.Server.Pool().Query(context.Background(),
		"SELECT p.item_id, p.hash FROM passages p JOIN source_items i ON i.id = p.item_id WHERE i.external_id = '1099'")
	if err != nil {
		t.Fatal(err)
	}
	var items []int64
	var hashes []string
	for rows.Next() {
		var id int64
		var h []byte
		if err := rows.Scan(&id, &h); err != nil {
			t.Fatal(err)
		}
		items, hashes = append(items, id), append(hashes, hex.EncodeToString(h))
	}
	if _, err := w.env.Server.Extract().Queue(context.Background(), []int64{public, w.github, w.azdo, w.jira}, extract.TriggerAnswer, items, hashes); err != nil {
		t.Fatal(err)
	}
	if n := w.env.RunExtractJobs(); n == 0 {
		t.Fatal("no extraction job ran")
	}
	if err := w.env.Server.Resolve().Mentions(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w, fake
}

func TestSCN_RES_006_DescriptionsLinkOrNameUnnamedThings(t *testing.T) {
	w, _ := mentionWorld(t)
	cs := cards(t, w.reviewer)
	var link, unnamed *card
	for i, c := range cs {
		switch c.Kind {
		case "mention_link":
			for _, s := range c.Subjects {
				if s.Key == "backstage:component:default/reminder-service" {
					link = &cs[i]
				}
			}
		case "unnamed":
			unnamed = &cs[i]
		}
	}
	if link == nil || link.Band != "high" {
		t.Fatalf("no High link card for the reminder service description: %+v", cs)
	}
	if unnamed == nil || len(unnamed.Subjects) != 1 || unnamed.Subjects[0].Name != "the nightly export job (unnamed)" {
		t.Fatalf("no unnamed card for the export job: %+v", unnamed)
	}
	if n := w.env.Count("SELECT count(*) FROM entities WHERE key = $1 AND status = 'proposed'", unnamed.Subjects[0].Key); n != 1 {
		t.Error("the unnamed entity is not proposed")
	}

	// Accepting the link card adds the claim with the linked entity.
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", link.ID), map[string]any{})
	if n := w.env.Count(`SELECT count(*) FROM claims c JOIN entities s ON s.id = c.subject_id JOIN entities o ON o.id = c.object_entity_id
JOIN claim_evidence e ON e.claim_id = c.id
WHERE s.key = 'backstage:component:default/reminder-service' AND c.predicate = 'depends_on' AND o.key = $1 AND e.kind = 'passage'`, peer); n != 1 {
		t.Errorf("%d linked claims after accept", n)
	}
	// Accepting the unnamed card makes the entity active.
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", unnamed.ID), map[string]any{"canonicalName": "Nightly export"})
	var name string
	if err := w.env.Server.Pool().QueryRow(context.Background(), `SELECT e.status || ' ' || a.alias FROM entities e
JOIN entity_aliases a ON a.entity_id = e.id AND a.is_name WHERE e.key = $1`, unnamed.Subjects[0].Key).Scan(&name); err != nil || name != "active Nightly export" {
		t.Errorf("the accepted unnamed entity is %q, %v", name, err)
	}

	// A second run makes no new cards.
	before := w.env.Count("SELECT count(*) FROM proposals")
	if err := w.env.Server.Resolve().Mentions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := w.env.Count("SELECT count(*) FROM proposals"); after != before {
		t.Errorf("a second run added %d cards", after-before)
	}
}

func TestSCN_EXT_004_NameMentionsGoToResolution(t *testing.T) {
	w, _ := mentionWorld(t)
	gh := "github:" + strings.ToLower(w.host) + ":user:sam.acme"
	var link *card
	cs := cards(t, w.reviewer)
	for i, c := range cs {
		if c.Kind == "mention_link" {
			for _, s := range c.Subjects {
				if s.Key == gh {
					link = &cs[i]
				}
			}
		}
	}
	if link == nil {
		t.Fatalf("the name Sam Acme gives no link card: %+v", cs)
	}
	if n := w.env.Count("SELECT count(*) FROM decisions WHERE kind = 'mention_link'"); n != 1 {
		t.Errorf("%d mention_link decisions", n)
	}
	if n := w.env.Count("SELECT count(*) FROM decisions WHERE kind = 'describes'"); n == 0 {
		t.Error("the descriptions made no describes decision")
	}
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", link.ID), map[string]any{})
	if n := w.env.Count(`SELECT count(*) FROM claims c JOIN entities s ON s.id = c.subject_id JOIN entities o ON o.id = c.object_entity_id
WHERE s.key = $1 AND c.predicate = 'owned_by' AND o.key = $2`, pay, gh); n != 1 {
		t.Errorf("%d owner claims after the link", n)
	}
	// The name becomes an alias of the linked person in the source.
	if n := w.env.Count(`SELECT count(*) FROM entity_aliases a JOIN entities e ON e.id = a.entity_id
JOIN connections c ON c.id = a.connection_id WHERE e.key = $1 AND a.alias_norm = 'sam acme' AND c.kind = 'confluence'`, gh); n != 1 {
		t.Error("the linked name is not an alias")
	}
}

func TestSCN_VOC_003_WorthyPhraseGivesNewTermCard(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	public := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: w.base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	testkit.ShareWithEveryone(t, w.env, w.owner, public)
	// The restricted space uses the phrase too. Its text must not reach a
	// draft (SI-17).
	w.src.PutPage(fixtures.Page{ID: "2099", Space: "RST", Title: "Falcon week", Author: "alex.acme",
		Body: "<p>Open Access Week hides " + fixtures.Canary + " from the board.</p>"})
	for _, id := range []int64{public, w.restricted} {
		if r := w.env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync: %+v", r)
		}
	}
	fake, openai, decisions := testkit.FakeModels(t)
	chat := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "extract", testkit.Function{Endpoint: chat, Model: "fake-chat"})
	dec := testkit.CreateEndpoint(t, w.owner, "decide", "decisions", decisions, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "decide", testkit.Function{Endpoint: dec, Model: "fake-decide"})
	fake.OnDecide(func(_ string, _ map[string]any, state map[string]any) map[string]any {
		phrase, ok := state["phrase"].(string)
		if !ok {
			return nil
		}
		p := 0.1
		if phrase == "Open Access Week" {
			p = 0.95
		}
		return map[string]any{"type": "noul", "noul": p}
	})
	fake.OnChat(func(map[string]any) string {
		return `{"definition":"The week when Acme freezes deploys for the open access campaign.","quote":"Team Hermes freezes deploys during Open Access Week."}`
	})
	r := w.env.Server.Resolve()
	if err := r.Terms(ctx); err != nil {
		t.Fatal(err)
	}
	var term *card
	cs := cards(t, w.reviewer)
	for i, c := range cs {
		if c.Kind == "new_term" {
			if term != nil {
				t.Errorf("a second new term card: %+v", c)
			}
			term = &cs[i]
		}
	}
	if term == nil {
		t.Fatal("no new term card")
	}
	p := term.Payload.(map[string]any)
	if p["phrase"] != "Open Access Week" || p["definition"] == "" || p["quote"] != "Team Hermes freezes deploys during Open Access Week." {
		t.Errorf("payload %+v", p)
	}
	for _, req := range fake.Requests() {
		if strings.Contains(fmt.Sprint(req.Body), fixtures.Canary) {
			t.Fatal("a model request holds restricted text")
		}
	}
	if n := w.env.Count("SELECT count(*) FROM proposals WHERE kind = 'new_term' AND $1 = ANY(connections)", w.restricted); n != 0 {
		t.Error("the card has restricted input")
	}

	// A draft whose quote is not in the passages has no definition.
	if n := w.env.Count("SELECT count(*) FROM decisions WHERE kind = 'term_worthy'"); n < 2 {
		t.Errorf("%d term_worthy decisions", n)
	}

	// Accepting the card creates an approved registry term with the draft.
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", term.ID), map[string]any{})
	if n := w.env.Count(`SELECT count(*) FROM claims c JOIN entities e ON e.id = c.subject_id JOIN claim_evidence ev ON ev.claim_id = c.id
WHERE e.key = 'registry:term:open-access-week' AND c.predicate = 'defined_as' AND ev.source_type = 'confluence'`); n != 1 {
		t.Error("the accepted term has no definition with the source quote")
	}
	before := w.env.Count("SELECT count(*) FROM decisions WHERE kind = 'term_worthy'")
	if err := r.Terms(ctx); err != nil {
		t.Fatal(err)
	}
	if after := w.env.Count("SELECT count(*) FROM decisions WHERE kind = 'term_worthy'"); after != before {
		t.Errorf("a second run asked %d more questions", after-before)
	}
}
