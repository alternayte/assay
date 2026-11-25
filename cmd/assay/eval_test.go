package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/eval"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_EVL_003_EvalWritesReport(t *testing.T) {
	env := testkit.Start(t)
	owner := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	for _, c := range []testkit.Connection{
		{Name: "acme-github", Kind: "github", BaseURL: base, Scope: map[string]any{"repositories": []string{"acme/payments-api"}}},
		{Name: "acme-eng", Kind: "confluence", BaseURL: base, Path: "/confluence", Scope: map[string]any{"spaces": []string{"ENG"}}},
	} {
		id := testkit.CreateConnection(t, owner, c)
		if r := env.RunNow(owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync: %+v", r)
		}
	}
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, owner, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat", PriceIn: 1, PriceOut: 2})
	var tok struct {
		Value string `json:"value"`
	}
	owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "eval"}).JSON(t, &tok)

	dir := t.TempDir()
	set := filepath.Join(dir, "five.yaml")
	if err := os.WriteFile(set, []byte(`
- id: payments-purpose
  question: What does the Payments API do?
  sources: [Payments API overview, acme/payments-api/README.md]
  facts: [card payments]
- id: payments-system
  question: Which system is the Payments API part of?
  sources: [Payments API overview]
  facts: [Billing]
- id: restart-payments
  question: How do I restart the Payments API?
  sources: [Payments API runbook]
  facts: [Drain the queue]
- id: payments-language
  question: In which language is the Payments API written?
  sources: [acme/payments-api/docs/architecture.md]
  facts: [Go]
- id: none-cafeteria
  question: What is on the cafeteria menu on Fridays?
  no_evidence: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report.md")
	var out, errOut bytes.Buffer
	if code := run([]string{"eval", "--url", env.HTTP.URL, "--token", tok.Value, "--file", set, "--out", report}, nil, &out, &errOut); code != 0 {
		t.Fatalf("eval: exit %d: %s", code, errOut.String())
	}
	b, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)
	for _, want := range []string{"| Questions | 5 |", "| Citation precision |", "| Citation recall |", "| Fact recall |",
		"| No-evidence accuracy |", "| Latency p50 |", "| Latency p95 |", "| Cost |", "| payments-purpose |", "| none-cafeteria |"} {
		if !strings.Contains(md, want) {
			t.Errorf("the report has no %q:\n%s", want, md)
		}
	}
	// The fake answers cite real sources and cost something.
	if regexp.MustCompile(`\| Citation precision \| 0\.00 \|`).MatchString(md) || strings.Contains(md, "| Cost | 0.0000 USD |") {
		t.Errorf("the run scored nothing:\n%s", md)
	}
	if strings.Contains(md, "status 401") || strings.Contains(md, "answer_off") {
		t.Fatalf("the questions failed:\n%s", md)
	}
}

func TestSCN_ASK_011_FromFeedbackWritesAQuestionSet(t *testing.T) {
	env := testkit.Start(t)
	owner := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, owner, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api"}}})
	if r := env.RunNow(owner, conn, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	fake, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, owner, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	questionID := regexp.MustCompile(`"questionId":(\d+)`)
	ask := func(q string) string {
		t.Helper()
		r := owner.Must(http.StatusOK, http.MethodPost, "/api/v1/ask", map[string]string{"question": q})
		m := questionID.FindAllSubmatch(r.Body, -1)
		if len(m) == 0 {
			t.Fatalf("no answer: %s", r.Body)
		}
		return string(m[len(m)-1][1])
	}
	mark := func(id, rating string) {
		owner.Must(http.StatusOK, http.MethodPut, "/api/v1/questions/"+id+"/feedback", map[string]string{"rating": rating})
	}
	mark(ask("What does the Payments API do?"), "up")
	mark(ask("Which team owns the Payments API?"), "down")
	fake.OnChat(func(map[string]any) string { return "Nobody knows." })
	mark(ask("What is on the cafeteria menu on Fridays?"), "down")
	ask("Is this question rated?")

	var tok struct {
		Value string `json:"value"`
	}
	owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "export"}).JSON(t, &tok)
	set := filepath.Join(t.TempDir(), "feedback.yaml")
	var out, errOut bytes.Buffer
	if code := run([]string{"eval", "--from-feedback", "--url", env.HTTP.URL, "--token", tok.Value, "--out", set}, nil, &out, &errOut); code != 0 {
		t.Fatalf("export: exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "real questions") {
		t.Fatalf("the command does not say that the file holds real questions: %s", out.String())
	}
	b, err := os.ReadFile(set)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := eval.Parse(b)
	if err == nil || !strings.Contains(err.Error(), "give sources and facts") {
		t.Fatalf("Parse accepts the set without facts: %v\n%s", err, b)
	}
	if len(qs) != 3 || qs[0].Question != "What does the Payments API do?" || len(qs[0].Sources) == 0 || !qs[2].NoEvidence {
		t.Fatalf("questions %+v\n%s", qs, b)
	}
	// A person adds the sources and facts. Then the set parses.
	filled := strings.ReplaceAll(strings.ReplaceAll(string(b), "facts: []", "facts: [card payments]"), "sources: []", "sources: [README]")
	if _, err := eval.Parse([]byte(filled)); err != nil {
		t.Fatalf("the filled set does not parse: %v\n%s", err, filled)
	}

	// The file never goes into the repository.
	inRepo := filepath.Join("..", "..", "tmp", "feedback.yaml")
	if code := run([]string{"eval", "--from-feedback", "--url", env.HTTP.URL, "--token", tok.Value, "--out", inRepo}, nil, &out, &errOut); code != 2 {
		t.Fatalf("an export into the repository exits %d", code)
	}
	if _, err := os.Stat(inRepo); err == nil {
		t.Fatal("the export wrote into the repository")
	}
}
