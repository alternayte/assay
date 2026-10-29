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
