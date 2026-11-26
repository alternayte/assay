package extract_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/extract"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_EXT_008_TeamsDecisionOnEntityPage(t *testing.T) {
	const pay = "backstage:component:default/payments-api"
	idp, ibase := testkit.FakeIdentity(t)
	env := testkit.Start(t, testkit.WithEntraAuthority(ibase+"/entra"))
	w := &world{env: env, admin: env.Setup("owner@acme.test")}
	var base string
	w.src, base = testkit.FakeSources(t)
	w.github = testkit.CreateConnection(t, w.admin, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api"}}})
	chat := testkit.CreateConnection(t, w.admin, testkit.Connection{Name: "acme-teams", Kind: "teams", BaseURL: ibase,
		Scope: map[string]any{"teams": []string{fixtures.TeamsPayments}, "channels": []string{fixtures.TeamsPayments + "/" + fixtures.TeamsDesign}}})
	w.sync(t, w.github, chat)

	var openai string
	w.fake, openai, _ = testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.admin, "extract", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	const quote = "the payments-api service keeps an Osprey refund request open for 7 days, then it cancels the request."
	w.fake.OnChat(func(body map[string]any) string {
		user := body["messages"].([]any)[1].(map[string]any)["content"].(string)
		if !strings.Contains(user, "We decided:") {
			return `{"claims":[]}`
		}
		return fmt.Sprintf(`{"claims":[{"subject":{"kind":"key","value":%q},"predicate":"stated","object":{"kind":"text","value":%q},`+
			`"scope":null,"stance":"supports","quote":%q}]}`, pay, "A refund request stays open for 7 days, then it is cancelled.", quote)
	})
	roots := idp.TeamsThreads(fixtures.TeamsPayments, fixtures.TeamsDesign)
	external := fixtures.TeamsPayments + "/" + fixtures.TeamsDesign + "/" + roots[1]
	item, hashes := w.passages(t, chat, external)
	if len(hashes) != 5 {
		t.Fatalf("the decision thread has %d passages, want one for each of its 5 messages", len(hashes))
	}
	items := make([]int64, len(hashes))
	for i := range items {
		items[i] = item
	}
	if _, err := env.Server.Extract().Queue(t.Context(), []int64{w.github, chat}, extract.TriggerAnswer, items, hashes); err != nil {
		t.Fatal(err)
	}
	if n := env.RunExtractJobs(); n == 0 {
		t.Fatal("no extraction job ran")
	}

	var page struct {
		Claims []struct {
			Predicate  string `json:"predicate"`
			ObjectText string `json:"objectText"`
			Evidence   []struct {
				SourceType string `json:"sourceType"`
				Title      string `json:"title"`
				Quote      string `json:"quote"`
			} `json:"evidence"`
		} `json:"claims"`
	}
	w.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(pay), nil).JSON(t, &page)
	for _, c := range page.Claims {
		if c.Predicate != "stated" || !strings.Contains(c.ObjectText, "7 days") {
			continue
		}
		for _, e := range c.Evidence {
			if e.SourceType == "teams" && strings.HasPrefix(e.Title, "Design reviews: ") && strings.Contains(e.Quote, "7 days") {
				return
			}
		}
		t.Fatalf("the decision has no evidence from the Teams thread: %+v", c)
	}
	t.Fatalf("the entity page of payments-api does not show the decision: %+v", page.Claims)
}
