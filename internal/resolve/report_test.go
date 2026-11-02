package resolve_test

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/resolve"
)

func TestSCN_EVL_004_ReportPerBandAndKind(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	var item int64
	if err := w.env.Server.Pool().QueryRow(ctx, "SELECT id FROM source_items WHERE external_id = 'PAY-1'").Scan(&item); err != nil {
		t.Fatal(err)
	}
	passage := fmt.Sprintf("passage:%d:ab", item)
	seed := func(kind, band, probs string, refs ...string) {
		w.exec(t, `INSERT INTO decisions (kind, subject_refs, input_hash, probs, raw_p, band, connections)
VALUES ($1, $2, sha256(random()::text::bytea), $3::jsonb, 0.5, $4, '{}')`, kind, refs, probs, band)
	}
	seed("same_entity", "high", "{}", "k:a", "k:b")
	seed("same_entity", "medium", "{}", "k:c", "k:d")
	seed("same_entity", "high", "{}", "k:e", "k:f")
	seed("same_entity", "high", "{}", "k:g", "k:h")
	seed("describes", "high", "{}", "k:one", passage, "mention:the card gateway")
	seed("describes", "low", "{}", "k:two", passage, "mention:the card gateway")
	seed("describes", "medium", "{}", "k:one", passage, "mention:the night job")
	seed("mention_link", "high", `{"c1":0.03,"c2":0.95,"none":0.01,"new":0.01}`, "k:four", "k:three", passage, "mention:sam acme")

	rs, err := w.env.Server.Resolve().Report(ctx, resolve.EvalLabels{
		Same:      []resolve.LabelPair{{A: "k:b", B: "k:a"}, {A: "k:c", B: "k:d"}},
		Different: []resolve.LabelPair{{A: "k:e", B: "k:f"}},
		Mentions: []resolve.LabelMention{{Item: "PAY-1", Mention: "The card gateway", Entity: "k:one"},
			{Item: "PAY-1", Mention: "the night job"}, {Item: "PAY-1", Mention: "Sam Acme", Entity: "k:three"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]resolve.BandResult{}
	for _, r := range rs {
		got[r.Kind+"/"+r.Band] = r
	}
	want := map[string][3]float64{ // decisions, precision, recall
		"same_entity/high":   {2, 0.5, 0.5},
		"same_entity/medium": {1, 1, 0.5},
		"describes/high":     {1, 1, 0.5},
		"describes/medium":   {1, 0, 0},
		"describes/low":      {1, 0, 0},
		"mention_link/high":  {1, 1, 0.5},
	}
	if len(got) != len(want) {
		t.Errorf("bands %v", rs)
	}
	for k, w := range want {
		r := got[k]
		if float64(r.Decisions) != w[0] || math.Abs(r.Precision-w[1]) > 1e-9 || math.Abs(r.Recall-w[2]) > 1e-9 {
			t.Errorf("%s: %d decisions, precision %.2f, recall %.2f; want %v", k, r.Decisions, r.Precision, r.Recall, w)
		}
	}
	if md := resolve.ReportMarkdown(rs); !strings.Contains(md, "| same_entity | high | 2 | 0.50 | 0.50 |") {
		t.Errorf("markdown:\n%s", md)
	}
}

// TestSCN_DSG_009_ReportScoresRepoSystem scores repo_system decisions
// against the labelled group of each repository.
func TestSCN_DSG_009_ReportScoresRepoSystem(t *testing.T) {
	w := start(t)
	seed := func(band, probs string, refs ...string) {
		w.exec(t, `INSERT INTO decisions (kind, subject_refs, input_hash, probs, raw_p, band, connections)
VALUES ('repo_system', $1, sha256(random()::text::bytea), $2::jsonb, 0.5, $3, '{}')`, refs, probs, band)
	}
	seed("high", `{"c1":0.9,"c2":0.05,"new":0.03,"none":0.02}`, "r:worker", "s:payments", "s:billing")
	seed("high", `{"c1":0.1,"c2":0.8,"new":0.05,"none":0.05}`, "r:web", "s:payments", "s:billing")
	seed("low", `{"c1":0.2,"new":0.1,"none":0.7}`, "r:fraud", "s:payments")
	seed("low", `{"c1":0.9,"new":0.05,"none":0.05}`, "r:unlabelled", "s:payments")
	rs, err := w.env.Server.Resolve().Report(context.Background(), resolve.EvalLabels{Repos: []resolve.LabelRepo{
		{Repository: "r:worker", Group: "s:payments"}, {Repository: "r:web", Group: "s:payments"}, {Repository: "r:fraud"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]resolve.BandResult{}
	for _, r := range rs {
		got[r.Kind+"/"+r.Band] = r
	}
	high, low := got["repo_system/high"], got["repo_system/low"]
	if high.Decisions != 2 || math.Abs(high.Precision-0.5) > 1e-9 || math.Abs(high.Recall-0.5) > 1e-9 {
		t.Errorf("high band %+v", high)
	}
	if low.Decisions != 1 || low.Precision != 1 || low.Recall != 0 {
		t.Errorf("low band %+v", low)
	}
}
