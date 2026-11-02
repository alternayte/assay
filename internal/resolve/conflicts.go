package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/visibility"
)

// Conflicts opens a conflict card for each conflict set that the claims of
// all active connections give: two or more Conflicting values of one
// subject, single-valued predicate and overlapping scope. A set with a card,
// open or decided, gets no second card. The card inputs are the evidence of
// the claims and the sources of their entities, so a reviewer sees the card
// only when the reviewer sees all of them (REQ-REV-002, REQ-CLM-006).
func (s *Service) Conflicts(ctx context.Context) error {
	var preds []string
	for name, p := range claims.Registry {
		if !p.Multi && !p.NeverConflicts {
			preds = append(preds, name)
		}
	}
	slices.Sort(preds)
	q := New(s.Pool)
	subjects, err := q.ConflictSubjects(ctx, preds)
	if err != nil || len(subjects) == 0 {
		return err
	}
	conns, err := q.ActiveConnections(ctx)
	if err != nil {
		return err
	}
	cs, err := claims.Load(ctx, s.Pool, visibility.Set(conns), claims.Query{Subjects: subjects})
	if err != nil {
		return err
	}
	for _, set := range claims.Conflicts(cs) {
		if set.Disputed || len(set.Claims) < 2 || !allConflicting(cs, set.Claims) {
			continue
		}
		parts := make([]string, len(set.Claims))
		for i, id := range set.Claims {
			parts[i] = fmt.Sprint(id)
		}
		target := "conflict:" + strings.Join(parts, ",")
		known, err := q.ConflictCardKnown(ctx, ConflictCardKnownParams{Target: target, Ids: set.Claims})
		if err != nil {
			return err
		}
		if known {
			continue
		}
		inputs, err := q.ClaimsConnections(ctx, set.Claims)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"claims": set.Claims, "predicate": set.Predicate})
		if _, err := q.InsertDecisionCard(ctx, InsertDecisionCardParams{Kind: "conflict", Target: target, Payload: payload,
			Connections: inputs}); err != nil {
			return err
		}
	}
	return nil
}

// allConflicting reports whether each claim of a set is Conflicting. A set
// whose best source decides it has one Supported claim and needs no card.
func allConflicting(cs []claims.Claim, ids []int64) bool {
	for _, c := range cs {
		if slices.Contains(ids, c.ID) && c.Status != claims.Conflicting {
			return false
		}
	}
	return true
}
