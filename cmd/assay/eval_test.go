package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

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
