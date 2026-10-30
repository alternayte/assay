package resolve

import (
	"math"
	"sort"
)

// Uncertainty is 1 − |2p − 1| (REQ-REV-010). A card without a probability,
// such as a report by a person, is fully uncertain.
func Uncertainty(p *float64) float64 {
	if p == nil {
		return 1
	}
	return 1 - math.Abs(2*(*p)-1)
}

// SortQueue sets the score of each card to its impact multiplied by its
// uncertainty and sorts the highest score first. Equal scores put the more
// uncertain card first, then the older card.
func SortQueue(cs []Card) {
	for i := range cs {
		cs[i].Score = float64(cs[i].Impact) * Uncertainty(cs[i].P)
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if ua, ub := Uncertainty(a.P), Uncertainty(b.P); ua != ub {
			return ua > ub
		}
		return a.ID < b.ID
	})
}
