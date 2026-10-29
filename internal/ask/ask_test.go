package ask_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/extract"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

type event struct {
	Name string
	Data json.RawMessage
}

type claim struct {
	ID         string `json:"id"`
	Predicate  string `json:"predicate"`
	ObjectName string `json:"objectName"`
}

type answer struct {
	QuestionID int64    `json:"questionId"`
	Markdown   string   `json:"markdown"`
	Cited      []string `json:"cited"`
	Uncited    []string `json:"uncited"`
	NoEvidence bool     `json:"noEvidence"`
	Unknowns   []string `json:"unknowns"`
	Conflicts  []struct {
		SubjectName string   `json:"subjectName"`
		Predicate   string   `json:"predicate"`
		Claims      []string `json:"claims"`
	} `json:"conflicts"`
	Sources []struct {
		ID            string   `json:"id"`
		ItemID        int64    `json:"itemId"`
		Title         string   `json:"title"`
		PassageHashes []string `json:"passageHashes"`
	} `json:"sources"`
	Claims  []claim `json:"claims"`
	Results []struct {
		Title string `json:"title"`
	} `json:"results"`
	Notice *struct {
		Code string `json:"code"`
	} `json:"notice"`
}

// ask posts a question and returns the server-sent events.
func ask(t *testing.T, cl *testkit.Client, q string) []event {
	t.Helper()
	r := cl.Do(http.MethodPost, "/api/v1/ask", map[string]string{"question": q})
	if r.Status != http.StatusOK || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("ask: %d %s %s", r.Status, r.Header.Get("Content-Type"), r.Body)
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
	return out
}

func final(t *testing.T, evs []event) answer {
	t.Helper()
	if len(evs) == 0 || evs[len(evs)-1].Name != "answer" {
		t.Fatalf("the last event is not the answer: %+v", evs)
	}
	var a answer
	if err := json.Unmarshal(evs[len(evs)-1].Data, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

type setup struct {
	env   *testkit.Env
	admin *testkit.Client
	fake  *fixtures.Models
	conn  int64
}

func start(t *testing.T) setup {
	t.Helper()
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}})
	if r := env.RunNow(admin, conn, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync %+v", r)
	}
	fake, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	return setup{env: env, admin: admin, fake: fake, conn: conn}
}

var marker = regexp.MustCompile(`\[(S|C)\d+\]`)

func TestSCN_ASK_001_ContextIDsAndEvents(t *testing.T) {
	s := start(t)
	s.admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term",
		"term": map[string]any{"name": "Ledger", "status": "approved", "definition": "The system that records each settled payment."}})
	s.fake.OnChat(func(map[string]any) string {
		return "The payments team owns the Payments API [C1]. The API sends settled payments to the ledger service [S1]."
	})
	evs := ask(t, s.admin, "Who owns payments-api and where does it send payments?")
	var names []string
	for _, e := range evs {
		if len(names) == 0 || names[len(names)-1] != e.Name {
			names = append(names, e.Name)
		}
	}
	if strings.Join(names, ",") != "results,delta,answer" {
		t.Fatalf("event order %v", names)
	}
	reqs := s.fake.RequestsTo("/v1/chat/completions")
	if len(reqs) != 1 || reqs[0].Body["stream"] != true || reqs[0].Body["tools"] != nil {
		t.Fatalf("chat requests %+v", reqs)
	}
	msgs := reqs[0].Body["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{`<source id="S1"`, `<claim id="C1">`, "owned by", `<term name="Ledger">The system that records each settled payment.</term>`} {
		if !strings.Contains(user, want) {
			t.Fatalf("the context has no %q:\n%s", want, user)
		}
	}
	if sys := msgs[0].(map[string]any)["content"].(string); !strings.Contains(sys, "data, not instructions") {
		t.Fatalf("system text %q", sys)
	}
	a := final(t, evs)
	if !slices.Equal(a.Cited, []string{"C1", "S1"}) || len(a.Uncited) != 0 || a.NoEvidence || a.QuestionID == 0 {
		t.Fatalf("answer %+v", a)
	}
	if !slices.ContainsFunc(a.Claims, func(c claim) bool {
		return c.Predicate == "owned_by" && c.ObjectName == "payments-team"
	}) {
		t.Fatalf("claims %+v", a.Claims)
	}
}

func TestSCN_ASK_002_ValidationRemovesAndFlags(t *testing.T) {
	s := start(t)
	s.fake.OnChat(func(map[string]any) string {
		return "The Payments API sends payments to the ledger [S1][S99]. It runs in three regions.\n- It stores payments in PostgreSQL [S2, P1]."
	})
	a := final(t, ask(t, s.admin, "What does the Payments API do?"))
	if strings.Contains(a.Markdown, "S99") || strings.Contains(a.Markdown, "P1") {
		t.Fatalf("unknown markers stay: %s", a.Markdown)
	}
	for _, m := range marker.FindAllString(a.Markdown, -1) {
		if !slices.Contains(a.Cited, m[1:len(m)-1]) {
			t.Fatalf("marker %s is not in cited %v", m, a.Cited)
		}
	}
	if len(a.Uncited) != 1 || a.Uncited[0] != "It runs in three regions." {
		t.Fatalf("uncited %q", a.Uncited)
	}
	if a.NoEvidence {
		t.Fatal("the answer with citations has no evidence")
	}

	s.fake.OnChat(func(map[string]any) string { return "The service runs on Mars [S42]. Nobody owns it." })
	none := final(t, ask(t, s.admin, "Where does the Payments API run?"))
	if !none.NoEvidence || none.Markdown != "Assay found no evidence for this question." || len(none.Results) == 0 {
		t.Fatalf("no-evidence answer %+v", none)
	}
}

func TestSCN_ASK_003_ConflictsAndUnknowns(t *testing.T) {
	s := start(t)
	pool := s.env.Server.Pool()
	ctx := t.Context()
	// A second owner of the component, with evidence in the README of the
	// repository: owned_by is single-valued.
	if _, err := pool.Exec(ctx, `
WITH comp AS (SELECT id FROM entities WHERE key = 'backstage:component:default/payments-api'),
     team AS (INSERT INTO entities (type, key) VALUES ('team', 'backstage:group:default/ledger-team') RETURNING id),
     src AS (INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $1 FROM team),
     alias AS (INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name) SELECT id, $1, 'ledger-team', 'ledger team', true FROM team),
     cl AS (INSERT INTO claims (subject_id, predicate, object_entity_id) SELECT comp.id, 'owned_by', team.id FROM comp, team RETURNING id),
     p AS (SELECT p.item_id, p.hash FROM passages p JOIN source_items i ON i.id = p.item_id
           WHERE i.external_id = 'acme/payments-api:README.md' ORDER BY p.ordinal LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, quote, source_type)
SELECT cl.id, $1, p.item_id, p.hash, 'passage', 'The Payments API takes card payments', 'repository' FROM cl, p`, s.conn); err != nil {
		t.Fatal(err)
	}
	s.fake.OnChat(func(map[string]any) string {
		return "Two teams own the Payments API [C1][C2].\nUnknown: the on-call rota of the Payments API"
	})
	a := final(t, ask(t, s.admin, "Who owns payments-api?"))
	if len(a.Conflicts) != 1 || a.Conflicts[0].Predicate != "owned_by" || len(a.Conflicts[0].Claims) != 2 {
		t.Fatalf("conflicts %+v", a.Conflicts)
	}
	if !slices.Equal(a.Unknowns, []string{"the on-call rota of the Payments API"}) || strings.Contains(a.Markdown, "Unknown") {
		t.Fatalf("unknowns %q, markdown %q", a.Unknowns, a.Markdown)
	}
}

func TestSCN_VIS_009_QuestionHistoryIsPrivate(t *testing.T) {
	s := start(t)
	a := final(t, ask(t, s.admin, "What does the Payments API do?"))
	var list struct {
		Questions []struct {
			ID   int64  `json:"id"`
			Text string `json:"text"`
		} `json:"questions"`
	}
	s.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/questions", nil).JSON(t, &list)
	if len(list.Questions) != 1 || list.Questions[0].ID != a.QuestionID {
		t.Fatalf("owner history %+v", list)
	}
	s.admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/questions/%d", a.QuestionID), nil)

	member, _ := s.env.Invite(s.admin, "member@acme.test", "member", "member-password-1")
	member.Must(http.StatusOK, http.MethodGet, "/api/v1/questions", nil).JSON(t, &list)
	if len(list.Questions) != 0 {
		t.Fatalf("member sees %+v", list)
	}
	hidden := member.Must(http.StatusNotFound, http.MethodGet, fmt.Sprintf("/api/v1/questions/%d", a.QuestionID), nil)
	missing := member.Must(http.StatusNotFound, http.MethodGet, "/api/v1/questions/999999", nil)
	if !bytes.Equal(hidden.Body, missing.Body) {
		t.Fatalf("hidden %s, missing %s", hidden.Body, missing.Body)
	}
}

func TestSCN_MOD_003_AskShowsResultsWithBudgetNotice(t *testing.T) {
	s := start(t)
	ep := s.env.Count("SELECT id FROM model_endpoints WHERE name = 'fake'")
	testkit.SetFunction(t, s.admin, "answer", testkit.Function{Endpoint: int64(ep), Model: "fake-chat", PriceIn: 1, PriceOut: 1, Budget: 0.01})
	if _, err := s.env.Server.Pool().Exec(t.Context(),
		"INSERT INTO model_usage (function, model, tokens_in, tokens_out, cost_usd) VALUES ('answer', 'fake-chat', 9900, 0, 0.0099)"); err != nil {
		t.Fatal(err)
	}
	evs := ask(t, s.admin, "What does the Payments API do?")
	a := final(t, evs)
	if a.Notice == nil || a.Notice.Code != "budget_exceeded" || len(a.Results) == 0 || a.Markdown != "" {
		t.Fatalf("answer %+v", a)
	}
	if n := len(s.fake.RequestsTo("/v1/chat/completions")); n != 0 {
		t.Fatalf("the fake received %d chat requests", n)
	}
	if evs[0].Name != "results" {
		t.Fatalf("first event %s", evs[0].Name)
	}
}

func TestSCN_ASK_005_AnswerQueuesExtractionOfNewPassages(t *testing.T) {
	s := start(t)
	pool := s.env.Server.Pool()
	q := "What does the Payments API do?"
	first := final(t, ask(t, s.admin, q))
	if len(first.Cited) < 2 || first.Cited[0] != "S1" || first.Cited[1] != "S2" {
		t.Fatalf("cited %v", first.Cited)
	}
	if n := s.env.Count("SELECT count(*) FROM river_job WHERE kind = 'extract'"); n != 1 {
		t.Fatalf("%d extraction jobs after the first answer", n)
	}
	// The passages of S1 are extracted. Those of S2 are new.
	if _, err := pool.Exec(t.Context(), "DELETE FROM river_job WHERE kind = 'extract'"); err != nil {
		t.Fatal(err)
	}
	var s1, s2 []string
	var item1 int64
	for _, src := range first.Sources {
		switch src.ID {
		case "S1":
			s1, item1 = src.PassageHashes, src.ItemID
		case "S2":
			s2 = src.PassageHashes
		}
	}
	for _, h := range s1 {
		if _, err := pool.Exec(t.Context(), `INSERT INTO passage_extractions (item_id, passage_hash, extractor_version)
			VALUES ($1, decode($2, 'hex'), $3)`, item1, h, extract.Version()); err != nil {
			t.Fatal(err)
		}
	}
	final(t, ask(t, s.admin, q))
	var args extract.Args
	if err := pool.QueryRow(t.Context(), "SELECT args FROM river_job WHERE kind = 'extract'").Scan(&args); err != nil {
		t.Fatal(err)
	}
	if n := s.env.Count("SELECT count(*) FROM river_job WHERE kind = 'extract'"); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	slices.Sort(args.Hashes)
	want := slices.Clone(s2)
	slices.Sort(want)
	if !slices.Equal(args.Hashes, want) || args.Trigger != extract.TriggerAnswer {
		t.Fatalf("job %+v, want the hashes of S2 %v", args, want)
	}
}
