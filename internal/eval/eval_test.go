package eval_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/eval"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestSCN_EVL_002_QuestionSetAndScorer(t *testing.T) {
	qs, err := eval.Load("../../evals/questions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) < 40 {
		t.Fatalf("%d questions, want 40 or more", len(qs))
	}
	none := 0
	for _, q := range qs {
		if q.NoEvidence {
			none++
		}
	}
	if none == 0 || none == len(qs) {
		t.Fatalf("%d no-evidence questions of %d", none, len(qs))
	}
	for _, bad := range []string{
		"- id: a\n  question: x\n",                                    // no sources and facts
		"- id: a\n  question: x\n  no_evidence: true\n  facts: [y]\n", // facts on a no-evidence question
		"- id: a\n  question: x\n  sources: [s]\n  facts: [f]\n- id: a\n  question: y\n  sources: [s]\n  facts: [f]\n",
		"- id: a\n  question: x\n  sources: [s]\n  facts: [f]\n  extra: 1\n",
	} {
		if _, err := eval.Parse([]byte(bad)); err == nil {
			t.Errorf("no error for %q", bad)
		}
	}

	q1 := eval.Question{ID: "q1", Sources: []string{"Payments API overview", "catalog-info.yaml"}, Facts: []string{"Billing", "2 seconds"}}
	q2 := eval.Question{ID: "q2", Sources: []string{"PAY-2"}, Facts: []string{"one hour"}}
	q3 := eval.Question{ID: "q3", NoEvidence: true}
	q4 := eval.Question{ID: "q4", NoEvidence: true}
	rep := eval.Score([]eval.Result{
		{Question: q1, Markdown: "It is part of the  billing system [S1].", Latency: 100 * time.Millisecond, CostUSD: 0.01,
			Cited: []eval.Cited{{Title: "Payments API overview", URL: "https://wiki.acme.test/1001"}, {Title: "Team Hermes"}}},
		{Question: q2, Markdown: "The queue holds calls for one hour [S1].", Latency: 300 * time.Millisecond, CostUSD: 0.02,
			Cited: []eval.Cited{{Title: "PAY-2 Add a retry queue to the Payments API"}}},
		{Question: q3, NoEvidence: true, Latency: 200 * time.Millisecond},
		{Question: q4, NoEvidence: false, Markdown: "A guess.", Latency: 400 * time.Millisecond, CostUSD: 0.03},
	})
	// Cited: 3, correct 2. Expected sources: 3, found 2. Facts: 3, found 2.
	// No evidence right: q1, q2, q3. Latencies 100, 200, 300, 400 ms.
	if rep.Questions != 4 || !near(rep.CitationPrecision, 2.0/3) || !near(rep.CitationRecall, 2.0/3) ||
		!near(rep.FactRecall, 2.0/3) || !near(rep.NoEvidenceAccuracy, 0.75) || rep.LatencyP50 != 200*time.Millisecond ||
		rep.LatencyP95 != 400*time.Millisecond || !near(rep.CostUSD, 0.06) {
		t.Fatalf("report %+v", rep)
	}
	md := rep.Markdown("Acme")
	for _, want := range []string{"| Citation precision | 0.67 |", "| Citation recall | 0.67 |", "| Fact recall | 0.67 |",
		"| No-evidence accuracy | 0.75 |", "| Latency p50 | 200 ms |", "| Latency p95 | 400 ms |", "| Cost | 0.0600 USD |",
		"| q4 | 0 | 0 | 0/0 | 0/0 | no (wrong) |"} {
		if !strings.Contains(md, want) {
			t.Errorf("the report has no %q:\n%s", want, md)
		}
	}
}
