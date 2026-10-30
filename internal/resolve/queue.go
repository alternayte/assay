package resolve

import (
	"math"
	"slices"
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

var bandRank = map[string]int{"": 0, BandLow: 1, BandMedium: 2, BandHigh: 3}

// Cluster joins merge cards that share an entity into one cluster card
// (REQ-REV-009). A Low pair says that its entities are likely different,
// so it stays a card of its own and links nothing. The cluster card takes the lowest ID, the union of the
// subjects and the lowest band of its pairs, with the probability and
// reasons of that pair. Pairs lists the merge cards of the cluster.
func Cluster(cs []Card) []Card {
	parent := map[string]string{}
	var root func(string) string
	root = func(k string) string {
		if p, ok := parent[k]; ok && p != k {
			parent[k] = root(p)
			return parent[k]
		}
		parent[k] = k
		return k
	}
	links := func(c Card) bool { return c.Kind == "merge" && len(c.refs) > 0 && c.Band != BandLow }
	for _, c := range cs {
		if !links(c) {
			continue
		}
		for _, r := range c.refs[1:] {
			parent[root(r)] = root(c.refs[0])
		}
	}
	out := make([]Card, 0, len(cs))
	at := map[string]int{}
	for _, c := range cs {
		if !links(c) {
			if c.Kind == "merge" {
				c.Pairs = []int64{c.ID}
			}
			out = append(out, c)
			continue
		}
		r := root(c.refs[0])
		i, ok := at[r]
		if !ok {
			c.Pairs = []int64{c.ID}
			at[r] = len(out)
			out = append(out, c)
			continue
		}
		k := &out[i]
		k.Pairs = append(k.Pairs, c.ID)
		for _, ref := range c.refs {
			if !slices.Contains(k.refs, ref) {
				k.refs = append(k.refs, ref)
			}
		}
		if c.CreatedAt.Before(k.CreatedAt) {
			k.CreatedAt = c.CreatedAt
		}
		if bandRank[c.Band] < bandRank[k.Band] || (c.Band == k.Band && Uncertainty(c.P) > Uncertainty(k.P)) {
			k.Band, k.P, k.DecisionID, k.Reasons = c.Band, c.P, c.DecisionID, c.Reasons
		}
		k.ID = min(k.ID, c.ID)
	}
	return out
}
