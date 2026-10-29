package pages_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

type claim struct {
	ID        int64  `json:"id"`
	Predicate string `json:"predicate"`
	Status    string `json:"status"`
}

type page struct {
	Key           string  `json:"key"`
	Name          string  `json:"name"`
	VisibilityKey string  `json:"visibilityKey"`
	Facts         []claim `json:"facts"`
	Owners        []struct {
		Key string `json:"key"`
	} `json:"owners"`
	Related []struct {
		Key string `json:"key"`
	} `json:"related"`
	Sources []struct {
		Title string `json:"title"`
	} `json:"sources"`
	Summary struct {
		State     string `json:"state"`
		Markdown  string `json:"markdown"`
		Citations []struct {
			ID    string `json:"id"`
			Claim int64  `json:"-"`
		} `json:"citations"`
		Lint []struct {
			Rule string `json:"rule"`
		} `json:"lint"`
		CompiledAt *string `json:"compiledAt"`
		Links      struct {
			Links []struct {
				Terms []string `json:"terms"`
			} `json:"links"`
		} `json:"links"`
	} `json:"summary"`
}

type event struct {
	Name string
	Data json.RawMessage
}

type world struct {
	env                    *testkit.Env
	src                    *fixtures.Sources
	fake                   *fixtures.Models
	owner, member          *testkit.Client
	endpoint               int64
	public, github, secret int64
}

// start syncs the public Confluence space and GitHub, shared with Everyone,
// and the restricted space of the owner, and turns compile on.
func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.member, _ = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	var base string
	w.src, base = testkit.FakeSources(t)
	mk := func(name, kind string, scope map[string]any, share bool) int64 {
		id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: name, Kind: kind, BaseURL: base, Scope: scope})
		if share {
			testkit.ShareWithEveryone(t, env, w.owner, id)
		}
		if r := env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %s: %+v", name, r)
		}
		if r := env.RunNow(w.owner, id, "reconcile"); r.Status != "succeeded" {
			t.Fatalf("reconcile %s: %+v", name, r)
		}
		return id
	}
	w.public = mk("acme-public", "confluence", map[string]any{"spaces": []string{"ENG"}}, true)
	w.github = mk("acme-github", "github", map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}, true)
	w.secret = mk("acme-restricted", "confluence", map[string]any{"spaces": []string{"RST"}}, false)
	var openai string
	w.fake, openai, _ = testkit.FakeModels(t)
	w.endpoint = testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "compile", testkit.Function{Endpoint: w.endpoint, Model: "fake-chat"})
	return w
}

func pagePath(key string) string { return "/api/v1/entities/" + url.PathEscape(key) + "/page" }

func (w *world) key(t *testing.T, q, typ string) string {
	t.Helper()
	var out struct {
		Entities []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"entities"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/entities?type="+typ+"&q="+url.QueryEscape(q), nil).JSON(t, &out)
	for _, e := range out.Entities {
		if strings.EqualFold(e.Name, q) {
			return e.Key
		}
	}
	t.Fatalf("no %s named %q in %+v", typ, q, out.Entities)
	return ""
}

func get(t *testing.T, cl *testkit.Client, key string) page {
	t.Helper()
	var p page
	cl.Must(http.StatusOK, http.MethodGet, pagePath(key), nil).JSON(t, &p)
	return p
}

// compile compiles a page and returns its events. The last is the page.
func compile(t *testing.T, cl *testkit.Client, key string) ([]event, page) {
	t.Helper()
	r := cl.Do(http.MethodPost, pagePath(key)+"/compile", nil)
	if r.Status != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("compile: %d %s", r.Status, r.Body)
	}
	var out []event
	var cur event
	sc := bufio.NewScanner(bytes.NewReader(r.Body))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "event:"):
			cur.Name = strings.TrimSpace(l[len("event:"):])
		case strings.HasPrefix(l, "data:"):
			cur.Data = append(cur.Data, []byte(strings.TrimSpace(l[len("data:"):]))...)
		case l == "" && cur.Data != nil:
			out = append(out, cur)
			cur = event{}
		}
	}
	if len(out) == 0 || out[len(out)-1].Name != "page" {
		t.Fatalf("the last event is not the page: %s", r.Body)
	}
	var p page
	if err := json.Unmarshal(out[len(out)-1].Data, &p); err != nil {
		t.Fatal(err)
	}
	return out, p
}

func (w *world) compileCalls() int {
	n := 0
	for _, r := range w.fake.RequestsTo("/v1/chat/completions") {
		msgs, _ := r.Body["messages"].([]any)
		if len(msgs) > 1 {
			if m, ok := msgs[1].(map[string]any); ok && strings.HasPrefix(fmt.Sprint(m["content"]), "<entity ") {
				n++
			}
		}
	}
	return n
}

// addRestricted adds a stated claim to the entity with evidence from the
// restricted page that names the canary.
func (w *world) addRestricted(t *testing.T, key string) int64 {
	t.Helper()
	var id int64
	err := w.env.Server.Pool().QueryRow(context.Background(), `
WITH s AS (SELECT id FROM entities WHERE key = $1),
     c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
           SELECT s.id, 'stated', 'A debug log exposed test card numbers', 'a debug log exposed test card numbers' FROM s RETURNING id),
     p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p JOIN source_items i ON i.id = p.item_id
           WHERE i.external_id = '2002' AND p.norm LIKE '%CANARY%' ORDER BY p.ordinal LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT (SELECT id FROM c), p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM p
RETURNING claim_id`, key).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSCN_VIS_007_PagesPerVisibilityKey(t *testing.T) {
	w := start(t)
	key := w.key(t, "Payments API", "service")
	secret := w.addRestricted(t, key)

	_, own := compile(t, w.owner, key)
	evs, mem := compile(t, w.member, key)
	if own.VisibilityKey == mem.VisibilityKey {
		t.Fatalf("owner and member share the key %s", own.VisibilityKey)
	}
	if own.Summary.State != "fresh" || mem.Summary.State != "fresh" {
		t.Fatalf("states %s and %s", own.Summary.State, mem.Summary.State)
	}
	if n := w.env.Count("SELECT count(*) FROM pages p JOIN entities e ON e.id = p.entity_id WHERE e.key = $1", key); n != 2 {
		t.Fatalf("%d cached pages, want 2", n)
	}
	if !slices.ContainsFunc(own.Facts, func(c claim) bool { return c.ID == secret }) {
		t.Fatal("the owner page has no restricted claim")
	}
	if slices.ContainsFunc(mem.Facts, func(c claim) bool { return c.ID == secret }) {
		t.Fatal("the member page has the restricted claim")
	}
	var raw bytes.Buffer
	for _, e := range evs {
		raw.Write(e.Data)
	}
	got := w.member.Must(http.StatusOK, http.MethodGet, pagePath(key), nil).Body
	if bytes.Contains(raw.Bytes(), []byte(fixtures.Canary)) || bytes.Contains(got, []byte(fixtures.Canary)) {
		t.Fatal("the member page shows the canary")
	}
	if strings.Contains(string(got), fmt.Sprintf(`"id":%d,`, secret)) {
		t.Fatal("the member page cites the restricted claim")
	}
	// The cached pages serve each reader without a new compile.
	before := w.compileCalls()
	if p := get(t, w.member, key); p.Summary.State != "fresh" || p.VisibilityKey != mem.VisibilityKey {
		t.Fatalf("member reread: %s %s", p.Summary.State, p.VisibilityKey)
	}
	if p := get(t, w.owner, key); p.Summary.State != "fresh" || p.VisibilityKey != own.VisibilityKey {
		t.Fatalf("owner reread: %s %s", p.Summary.State, p.VisibilityKey)
	}
	if w.compileCalls() != before {
		t.Fatal("a read compiled again")
	}
}

func TestSCN_PAG_002_SummaryNeedsClaimsAndCitations(t *testing.T) {
	w := start(t)
	pay := w.key(t, "Payments API", "service")
	team := get(t, w.owner, pay).Owners[0].Key
	var rec struct {
		Key string `json:"key"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "thing", "thing": map[string]any{
		"type": "service", "name": "Card Gateway", "description": "The gateway takes card payments.", "owner": team}}).JSON(t, &rec)

	// Two claims: not enough evidence, and no model call.
	if p := get(t, w.member, rec.Key); p.Summary.State != "insufficient" || len(p.Facts) != 2 {
		t.Fatalf("two claims give %s with %d facts", p.Summary.State, len(p.Facts))
	}
	if _, p := compile(t, w.member, rec.Key); p.Summary.State != "insufficient" {
		t.Fatalf("compile of two claims gives %s", p.Summary.State)
	}
	if n := w.compileCalls(); n != 0 {
		t.Fatalf("%d compile calls for two claims", n)
	}

	// An uncited sentence is rejected and regenerated.
	calls := 0
	w.fake.OnChat(func(map[string]any) string {
		calls++
		if calls == 1 {
			return "The page has no source. Assay records the owner [C1]."
		}
		return "Assay records the owner [C1]. Assay records the system [C2]."
	})
	evs, p := compile(t, w.member, pay)
	if calls != 2 || p.Summary.State != "fresh" {
		t.Fatalf("%d calls, state %s", calls, p.Summary.State)
	}
	if !slices.ContainsFunc(evs, func(e event) bool { return e.Name == "retry" && strings.Contains(string(e.Data), "uncited") }) {
		t.Fatal("no retry event names the uncited sentence")
	}
	if strings.Contains(p.Summary.Markdown, "no source") || len(p.Summary.Citations) != 2 {
		t.Fatalf("summary %q with %d citations", p.Summary.Markdown, len(p.Summary.Citations))
	}
	// The second request carries the draft and the failed checks.
	reqs := w.fake.RequestsTo("/v1/chat/completions")
	msgs, _ := reqs[len(reqs)-1].Body["messages"].([]any)
	if len(msgs) != 4 || !strings.Contains(fmt.Sprint(msgs[3]), "no claim") {
		t.Fatalf("the regeneration request: %v", msgs)
	}
}

func TestSCN_PAG_004_TwoLintFailuresNeedEdit(t *testing.T) {
	w := start(t)
	pay := w.key(t, "Payments API", "service")
	calls := 0
	w.fake.OnChat(func(map[string]any) string {
		calls++
		return "The team will leverage the owner [C1]. The system is robust [C2]."
	})
	_, p := compile(t, w.owner, pay)
	if calls != 2 || p.Summary.State != "needs_edit" || p.Summary.Markdown != "" {
		t.Fatalf("%d calls, state %s, text %q", calls, p.Summary.State, p.Summary.Markdown)
	}
	if !slices.ContainsFunc(p.Summary.Lint, func(l struct {
		Rule string `json:"rule"`
	}) bool {
		return l.Rule == "banned_word"
	}) {
		t.Fatalf("lint %+v", p.Summary.Lint)
	}
	if g := get(t, w.owner, pay); g.Summary.State != "needs_edit" {
		t.Fatalf("a read gives %s", g.Summary.State)
	}
	var cards struct {
		Cards []struct {
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
		} `json:"cards"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/cards?kind=needs_edit", nil).JSON(t, &cards)
	if len(cards.Cards) != 1 || cards.Cards[0].Payload["entityKey"] != pay {
		t.Fatalf("cards %+v", cards.Cards)
	}
	// A second failed compile adds no second card.
	compile(t, w.owner, pay)
	if n := w.env.Count("SELECT count(*) FROM proposals WHERE kind = 'needs_edit'"); n != 1 {
		t.Fatalf("%d cards", n)
	}
}

func TestSCN_PAG_005_MarkdownWithFootnotesAndTermLinks(t *testing.T) {
	w := start(t)
	pay := w.key(t, "Payments API", "service")
	w.addRestricted(t, pay)
	var term struct {
		Key string `json:"key"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term", "term": map[string]any{
		"name": "Ledger", "definition": "The record of all money movements.", "status": "approved"}}).JSON(t, &term)
	compile(t, w.member, pay)
	compile(t, w.owner, pay)

	md := string(w.member.Must(http.StatusOK, http.MethodGet, pagePath(pay)+".md", nil).Body)
	for _, want := range []string{"# Payments API", "## Summary", "## Key facts", "## Conflicts", "## Decisions", "## Timeline",
		"## Related", "## Sources", "[^1]: [", "](http", "/e/" + url.PathEscape(term.Key) + ")"} {
		if !strings.Contains(md, want) {
			t.Errorf("the Markdown has no %q:\n%s", want, md)
		}
	}
	if !strings.Contains(md, "Assay records that") || !strings.Contains(md, "[^") {
		t.Errorf("the summary has no footnotes:\n%s", md)
	}
	if strings.Contains(md, fixtures.Canary) {
		t.Fatal("the member Markdown has the canary")
	}
	if own := string(w.owner.Must(http.StatusOK, http.MethodGet, pagePath(pay)+".md", nil).Body); !strings.Contains(own, fixtures.Canary) {
		t.Fatal("the owner Markdown has no restricted evidence")
	}
	r := w.member.Must(http.StatusOK, http.MethodGet, "/e/"+url.PathEscape(pay)+".md", nil)
	if string(r.Body) != md || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/markdown") {
		t.Fatalf("/e/{key}.md differs: %s\n%s", r.Header.Get("Content-Type"), r.Body)
	}
	if r := w.env.Client().Do(http.MethodGet, "/e/"+url.PathEscape(pay)+".md", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("an anonymous request gives %d", r.Status)
	}
}

func extractCalls(fake *fixtures.Models) int {
	n := 0
	for _, r := range fake.RequestsTo("/v1/chat/completions") {
		if rf, ok := r.Body["response_format"].(map[string]any); ok {
			if js, ok := rf["json_schema"].(map[string]any); ok && js["name"] == "extraction" {
				n++
			}
		}
	}
	return n
}

func TestSCN_EXT_001_PageCompileExtractsNewest40(t *testing.T) {
	w := start(t)
	for i := range 60 {
		w.src.PutPage(fixtures.Page{ID: fmt.Sprintf("%d", 9000+i), Space: "ENG", Title: fmt.Sprintf("Release note %d", i),
			Author: "sam.acme", Body: fmt.Sprintf("<p>Release %d of the Payments API sends receipts.</p>", i)})
	}
	if r := w.env.RunNow(w.owner, w.public, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync %+v", r)
	}
	mentions := w.env.Count("SELECT count(*) FROM passages WHERE norm ILIKE '%payments api%' AND connection_id = ANY($1)",
		[]int64{w.public, w.github})
	if mentions < 60 {
		t.Fatalf("%d mentioning passages", mentions)
	}
	testkit.SetFunction(t, w.owner, "extract", testkit.Function{Endpoint: w.endpoint, Model: "fake-chat"})
	compile(t, w.member, w.key(t, "Payments API", "service"))
	w.env.RunExtractJobs()
	if n := extractCalls(w.fake); n != 40 {
		t.Fatalf("%d extract calls, want 40", n)
	}
	if n := w.env.Count(`SELECT count(*) FROM passage_extractions x JOIN source_items i ON i.id = x.item_id
		WHERE i.external_id ~ '^9[0-9]{3}$' AND i.external_id::int >= 9020`); n != 40 {
		t.Fatalf("%d of the 40 newest passages are extracted", n)
	}
}
