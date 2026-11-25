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

func TestSCN_EVL_005_RetrievalMetrics(t *testing.T) {
	for _, bad := range []string{
		"- id: a\n  question: x\n  no_evidence: true\n  expected: [{source: s, grade: 3}]\n",
		"- id: a\n  question: x\n  sources: [s]\n  facts: [f]\n  expected: [{source: s, grade: 4}]\n",
		"- id: a\n  question: x\n  sources: [s]\n  facts: [f]\n  expected: [{source: '', grade: 3}]\n",
		"- id: a\n  question: x\n  sources: [s]\n  facts: [f]\n  expected: [{source: s, grade: 1}]\n",
	} {
		if _, err := eval.Parse([]byte(bad)); err == nil {
			t.Errorf("no error for %q", bad)
		}
	}
	qs, err := eval.Parse([]byte("- id: a\n  question: x\n  sources: [s]\n  facts: [f]\n  expected: [{source: s, grade: 3}, {source: t, grade: 1}]\n"))
	if err != nil || len(qs[0].Expected) != 2 || qs[0].Expected[1] != (eval.Expected{Source: "t", Grade: 1}) {
		t.Fatalf("parse expected: %+v %v", qs, err)
	}

	item := func(id int64, title string) eval.Ranked { return eval.Ranked{ItemID: id, Title: title} }
	// q1: A at item rank 2 (its second chunk does not count), the related C
	// at 3 and B at 4.
	q1 := eval.Question{ID: "q1", Expected: []eval.Expected{{Source: "A", Grade: 3}, {Source: "B", Grade: 2}, {Source: "C", Grade: 1}}}
	q2 := eval.Question{ID: "q2", Expected: []eval.Expected{{Source: "D", Grade: 3}}}
	// q3: F at rank 6, E not in the ranking.
	q3 := eval.Question{ID: "q3", Expected: []eval.Expected{{Source: "E", Grade: 3}, {Source: "F", Grade: 2}}}
	q4 := eval.Question{ID: "q4", Expected: []eval.Expected{{Source: "G", Grade: 3}}}
	rep := eval.ScoreRetrieval(eval.Pipeline{EmbedModel: "fake-embed-64", Vector: "on", Rerank: "off"}, []eval.Ranking{
		{Question: q1, Results: []eval.Ranked{item(1, "X"), item(2, "A"), item(2, "A"), item(3, "C"), item(4, "B")}},
		{Question: q2, Results: []eval.Ranked{{ItemID: 5, Title: "Other", URL: "https://wiki.acme.test/D"}}},
		{Question: q3, Results: []eval.Ranked{item(1, "X1"), item(2, "X2"), item(3, "X3"), item(4, "X4"), item(5, "X5"), item(6, "F")}},
		{Question: q4, Err: "status 500"},
	})
	l := math.Log2
	ndcg1 := (3/l(3) + 1/l(4) + 2/l(5)) / (3 + 2/l(3) + 1/l(4))
	ndcg3 := (2 / l(7)) / (3 + 2/l(3))
	want := []eval.RetrievalRow{
		{ID: "q1", Rank: 2, RR: 0.5, Hit3: true, Recall5: 1, Recall20: 1, NDCG10: ndcg1},
		{ID: "q2", Rank: 1, RR: 1, Hit1: true, Hit3: true, Recall5: 1, Recall20: 1, NDCG10: 1},
		{ID: "q3", Rank: 6, RR: 1.0 / 6, Recall20: 0.5, NDCG10: ndcg3},
		{ID: "q4", Err: "status 500"},
	}
	for i, w := range want {
		g := rep.Rows[i]
		if g.ID != w.ID || g.Rank != w.Rank || !near(g.RR, w.RR) || g.Hit1 != w.Hit1 || g.Hit3 != w.Hit3 || !near(g.Recall5, w.Recall5) ||
			!near(g.Recall20, w.Recall20) || !near(g.NDCG10, w.NDCG10) || g.Err != w.Err {
			t.Errorf("row %d: %+v, want %+v", i, g, w)
		}
	}
	if rep.Queries != 4 || !near(rep.MRR, (0.5+1+1.0/6)/4) || !near(rep.Hit1, 0.25) || !near(rep.Hit3, 0.5) ||
		!near(rep.Recall5, 0.5) || !near(rep.Recall20, 0.625) || !near(rep.NDCG10, (ndcg1+1+ndcg3)/4) {
		t.Fatalf("report %+v", rep)
	}
	md := rep.Markdown("Acme")
	for _, w := range []string{"Embedding model: fake-embed-64. Vector search: on. Rerank: off.", "| MRR | 0.417 |", "| Hit@1 | 0.250 |",
		"| Hit@3 | 0.500 |", "| Recall@5 | 0.500 |", "| Recall@20 | 0.625 |", "| q1 | 2 | no | yes |",
		"| q4 | none | no | no | 0.00 | 0.00 | 0.000 | status 500 |"} {
		if !strings.Contains(md, w) {
			t.Errorf("the report has no %q:\n%s", w, md)
		}
	}
}
