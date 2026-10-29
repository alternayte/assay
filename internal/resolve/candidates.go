package resolve

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Candidate limits (REQ-RES-001).
const (
	MaxCandidates = 5
	MinTrigram    = 0.4
	ProfileTopK   = 10
)

// Scores of the deterministic signals. A pair takes its best signal, and
// each shared neighbour adds NeighbourScore.
const (
	AcronymScore   = 0.8
	NeighbourScore = 0.2
)

// Candidate is a possible duplicate of a subject with the signals that
// found it.
type Candidate struct {
	ID      int64
	Score   float64
	Reasons []string
}

type pair struct{ a, b int64 }

func key(a, b int64) pair {
	if a > b {
		a, b = b, a
	}
	return pair{a, b}
}

// Candidates finds possible duplicates without a model: alias trigram,
// profile cosine, shared neighbours and acronym initials, within one entity
// type. Each subject gets its best MaxCandidates. Pairs with a verdict are
// left out.
func (s *Service) Candidates(ctx context.Context) (map[int64][]Candidate, error) {
	q := New(s.Pool)
	found := map[pair]*Candidate{}
	add := func(a, b int64, score float64, reason string) {
		k := key(a, b)
		c := found[k]
		if c == nil {
			c = &Candidate{}
			found[k] = c
		}
		c.Score = max(c.Score, score)
		c.Reasons = append(c.Reasons, reason)
	}
	tri, err := q.TrigramPairs(ctx, MinTrigram)
	if err != nil {
		return nil, err
	}
	for _, r := range tri {
		add(r.AID, r.BID, r.Score, fmt.Sprintf("alias similarity %.2f", r.Score))
	}
	acr, err := q.AcronymPairs(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range acr {
		add(r.AID, r.BID, AcronymScore, "acronym of the name")
	}
	cos, err := s.cosinePairs(ctx)
	if err != nil {
		return nil, err
	}
	for k, score := range cos {
		add(k.a, k.b, score, fmt.Sprintf("profile similarity %.2f", score))
	}
	nb, err := q.NeighbourPairs(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range nb {
		reason := fmt.Sprintf("%d shared owners or parents", r.Shared)
		if c := found[key(r.AID, r.BID)]; c != nil {
			c.Score += NeighbourScore * float64(r.Shared)
			c.Reasons = append(c.Reasons, reason)
			continue
		}
		add(r.AID, r.BID, NeighbourScore*float64(r.Shared), reason)
	}
	vs, err := q.Verdicts(ctx, KindSameEntity)
	if err != nil {
		return nil, err
	}
	for _, v := range vs {
		delete(found, key(v.AID, v.BID))
	}
	out := map[int64][]Candidate{}
	for k, c := range found {
		out[k.a] = append(out[k.a], Candidate{ID: k.b, Score: c.Score, Reasons: c.Reasons})
		out[k.b] = append(out[k.b], Candidate{ID: k.a, Score: c.Score, Reasons: c.Reasons})
	}
	for id, cs := range out {
		slices.SortFunc(cs, func(x, y Candidate) int {
			if c := cmp.Compare(y.Score, x.Score); c != 0 {
				return c
			}
			return cmp.Compare(x.ID, y.ID)
		})
		if len(cs) > MaxCandidates {
			cs = cs[:MaxCandidates]
		}
		out[id] = cs
	}
	return out, nil
}

// cosinePairs gives for each profile its ProfileTopK nearest profiles of
// the same type in the active space. Without a space it gives none.
func (s *Service) cosinePairs(ctx context.Context) (map[pair]float64, error) {
	sp, err := s.Models.ActiveSpace(ctx)
	if err != nil || sp == nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, fmt.Sprintf(`-- vis:system
SELECT a.ref_id, n.ref_id, n.score
FROM %[1]s a
JOIN entities ea ON ea.id = a.ref_id AND ea.status <> 'merged'
CROSS JOIN LATERAL (SELECT b.ref_id, (1 - (b.embedding <=> a.embedding))::float8 AS score
                    FROM %[1]s b
                    JOIN entities eb ON eb.id = b.ref_id AND eb.type = ea.type AND eb.status <> 'merged'
                    WHERE b.ref_kind = 'profile' AND b.ref_id <> a.ref_id
                    ORDER BY b.embedding <=> a.embedding LIMIT %[2]d) n
WHERE a.ref_kind = 'profile'`, sp.Table(), ProfileTopK))
	if err != nil {
		return nil, err
	}
	out := map[pair]float64{}
	var a, b int64
	var score float64
	_, err = pgx.ForEachRow(rows, []any{&a, &b, &score}, func() error {
		k := key(a, b)
		out[k] = max(out[k], score)
		return nil
	})
	return out, err
}
