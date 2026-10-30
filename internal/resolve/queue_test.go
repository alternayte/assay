package resolve_test

import (
	"math"
	"testing"

	"github.com/alternayte/assay/internal/resolve"
)

func TestSCN_REV_009_QueueOrder(t *testing.T) {
	p := func(v float64) *float64 { return &v }
	cs := []resolve.Card{
		{ID: 1, Kind: "merge", P: p(0.95), Impact: 10},
		{ID: 2, Kind: "merge", P: p(0.9), Impact: 0},
		{ID: 3, Kind: "merge", P: p(0.5), Impact: 2},
		{ID: 4, Kind: "correction", Impact: 4},
		{ID: 5, Kind: "merge", P: p(0.5), Impact: 10},
		{ID: 6, Kind: "merge", P: p(0.5), Impact: 0},
	}
	resolve.SortQueue(cs)
	want := []int64{5, 4, 3, 1, 6, 2}
	scores := []float64{10, 4, 2, 1, 0, 0}
	for i, c := range cs {
		if c.ID != want[i] || math.Abs(c.Score-scores[i]) > 1e-9 {
			t.Fatalf("position %d: card %d with score %.3f, want card %d with %.3f", i, c.ID, c.Score, want[i], scores[i])
		}
	}
	for _, x := range []struct{ p, u float64 }{{0, 0}, {1, 0}, {0.5, 1}, {0.75, 0.5}, {0.2, 0.4}} {
		if u := resolve.Uncertainty(&x.p); math.Abs(u-x.u) > 1e-9 {
			t.Errorf("uncertainty of %.2f is %.3f, want %.3f", x.p, u, x.u)
		}
	}
	if u := resolve.Uncertainty(nil); u != 1 {
		t.Errorf("uncertainty without a probability is %.3f", u)
	}
}
