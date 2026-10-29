package vocab_test

import (
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
	public, restricted      int64
}

const manuscript = "registry:term:manuscript"

// start syncs the public space and the restricted space, and creates the
// approved term Manuscript with the synonym Submission and the deprecated
// term Paper that prefers it.
func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.reviewer, _ = env.Invite(w.owner, "reviewer@acme.test", "reviewer", "reviewer-password-1")
	w.member, _ = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	var base string
	w.src, base = testkit.FakeSources(t)
	w.src.PutPage(fixtures.Page{ID: "2098", Space: "RST", Title: "Falcon manuscripts", Author: "alex.acme",
		Body: "<p>Each manuscript of Project Falcon stays private.</p>"})
	w.public = testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	testkit.ShareWithEveryone(t, env, w.owner, w.public)
	w.restricted = testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-restricted", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"RST"}}})
	for _, id := range []int64{w.public, w.restricted} {
		if r := env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync: %+v", r)
		}
	}
	return w
}

func (w *world) term(t *testing.T, term map[string]any) {
	t.Helper()
	w.reviewer.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term", "term": term})
}

func (w *world) vocabulary(t *testing.T) {
	t.Helper()
	w.term(t, map[string]any{"name": "Manuscript", "synonyms": []string{"Submission"}, "status": "approved",
		"definition": "A paper that an author sends to a journal for review."})
	w.term(t, map[string]any{"name": "Paper", "status": "deprecated", "preferred": manuscript, "definition": "An old word for manuscript."})
}

func TestSCN_VOC_007_PromptTermsAndLint(t *testing.T) {
	w := start(t)
	w.vocabulary(t)
	var names []string
	for i := 1; i <= 22; i++ {
		name := fmt.Sprintf("Acme Code %02d", i)
		names = append(names, name)
		w.term(t, map[string]any{"name": name, "status": "approved", "definition": fmt.Sprintf("Code number %d of Acme.", i)})
	}
	fake, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	fake.OnChat(func(map[string]any) string { return "A manuscript is a paper that an author sends [S1]." })

	question := "What is a manuscript? Also " + strings.Join(names, ", ") + "."
	r := w.member.Do(http.MethodPost, "/api/v1/ask", map[string]string{"question": question})
	if r.Status != http.StatusOK {
		t.Fatalf("ask: %d %s", r.Status, r.Body)
	}
	var prompt string
	for _, req := range fake.RequestsTo("/v1/chat/completions") {
		msgs := req.Body["messages"].([]any)
		prompt = msgs[1].(map[string]any)["content"].(string)
	}
	if !strings.Contains(prompt, `<term name="Manuscript">A paper that an author sends to a journal for review.</term>`) {
		t.Errorf("the prompt has no Manuscript definition:\n%s", prompt)
	}
	if n := strings.Count(prompt, "<term name="); n != 20 {
		t.Errorf("the prompt has %d term definitions, want 20", n)
	}
	body := string(r.Body)
	answer := body[strings.LastIndex(body, "event: answer"):]
	if !strings.Contains(answer, `"lint":[{"form":"paper","preferred":"Manuscript"`) {
		t.Errorf("the lint does not flag the deprecated term: %s", answer)
	}
	if !strings.Contains(answer, `"form":"manuscript","terms":["`+manuscript+`"]`) {
		t.Errorf("the answer does not link Manuscript: %s", answer)
	}
}

type results struct {
	Results []struct {
		Title string `json:"title"`
	} `json:"results"`
}

func (w *world) search(t *testing.T, q string) []string {
	t.Helper()
	var r results
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil).JSON(t, &r)
	var titles []string
	for _, x := range r.Results {
		titles = append(titles, x.Title)
	}
	return titles
}

func TestSCN_VOC_008_SearchExpandsSynonyms(t *testing.T) {
	w := start(t)
	if got := w.search(t, "submission"); len(got) != 0 {
		t.Fatalf("submission finds %v before the term exists", got)
	}
	w.vocabulary(t)
	got := w.search(t, "submission")
	if !strings.Contains(strings.Join(got, "|"), "Acme glossary") {
		t.Errorf("submission finds %v, want the glossary that says manuscript", got)
	}
}

type usage struct {
	Usage []struct {
		ConnectionName  string `json:"connectionName"`
		Items           int    `json:"items"`
		DeprecatedItems int    `json:"deprecatedItems"`
	} `json:"usage"`
	DeprecatedForms []string `json:"deprecatedForms"`
}

func (w *world) usage(t *testing.T, cl *testkit.Client) map[string][2]int {
	t.Helper()
	var u usage
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/terms/"+url.PathEscape(manuscript), nil).JSON(t, &u)
	if strings.Join(u.DeprecatedForms, ",") != "Paper" {
		t.Errorf("deprecated forms %v", u.DeprecatedForms)
	}
	out := map[string][2]int{}
	for _, x := range u.Usage {
		out[x.ConnectionName] = [2]int{x.Items, x.DeprecatedItems}
	}
	return out
}

func TestSCN_VOC_009_UsagePerConnection(t *testing.T) {
	w := start(t)
	w.vocabulary(t)
	member := w.usage(t, w.member)
	if got := member["acme-public"]; got[0] != 1 || got[1] != 1 {
		t.Errorf("public usage %v, want 1 item with the term and 1 with a deprecated form", got)
	}
	if _, ok := member["acme-restricted"]; ok {
		t.Error("the member sees usage in the restricted space")
	}
	if got := w.usage(t, w.owner)["acme-restricted"]; got[0] != 1 {
		t.Errorf("the owner sees restricted usage %v, want 1", got)
	}
}
