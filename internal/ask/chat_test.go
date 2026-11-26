package ask_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// threadOf finds the context ID of a Slack thread of the payments channel.
var threadOf = regexp.MustCompile(`<source id="(S\d+)" title="#payments: [^"]*" type="slack"`)

func TestSCN_ASK_013_AnswerCitesChatThread(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-slack", Kind: "slack", BaseURL: base,
		Scope: map[string]any{"channels": []string{fixtures.SlackPayments}}})
	if r := env.RunNow(admin, conn, "sync"); r.Status != "succeeded" || r.Created == 0 {
		t.Fatalf("sync %+v", r)
	}
	fake, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	fake.OnChat(func(body map[string]any) string {
		msgs := body["messages"].([]any)
		m := threadOf.FindStringSubmatch(msgs[len(msgs)-1].(map[string]any)["content"].(string))
		if m == nil {
			return "Assay has no source for the retry decision."
		}
		return "The payments team keeps three retries with backoff for declined charges [" + m[1] + "]."
	})
	a := final(t, ask(t, admin, "What did the payments team decide about retries?"))
	if a.NoEvidence || len(a.Cited) != 1 {
		t.Fatalf("answer %+v", a)
	}
	var cited []string
	for _, s := range a.Sources {
		if s.ID == a.Cited[0] && strings.HasPrefix(s.Title, "#payments: ") {
			cited = s.PassageHashes
		}
	}
	if len(cited) == 0 {
		t.Fatalf("the answer does not cite a passage of the thread: %+v", a)
	}
	// The cited passage is one message of the thread, with its author and
	// date.
	var norm string
	if err := env.Server.Pool().QueryRow(t.Context(), `SELECT p.norm FROM passages p JOIN source_items i ON i.id = p.item_id
		WHERE i.connection_id = $1 AND i.kind = 'thread' AND encode(p.hash, 'hex') = ANY($2) AND p.norm LIKE '%three retries%'`,
		conn, cited).Scan(&norm); err != nil {
		t.Fatalf("no cited passage holds the decision: %v", err)
	}
	if !strings.HasPrefix(norm, "Lena Park, ") {
		t.Fatalf("the passage %q does not start with the author", norm)
	}
}
