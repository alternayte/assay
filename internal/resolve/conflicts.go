package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

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
	conns, err := q.AllConnections(ctx)
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
		c, err := q.ClaimIdentity(ctx, set.Claims[0])
		if err != nil {
			return err
		}
		key := conflictKey(c.SubjectID, c.Predicate, c.ScopeEnv)
		if done, err := s.knownConflict(ctx, key, set.Claims); err != nil || done {
			return err
		}
		inputs, err := q.ClaimsConnections(ctx, set.Claims)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"claims": set.Claims, "predicate": set.Predicate})
		if _, err := q.InsertDecisionCard(ctx, InsertDecisionCardParams{Kind: "conflict", Target: target, Payload: payload,
			Connections: inputs, DedupeKey: &key}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
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

// knownConflict handles a conflict set whose question has a card. An open
// card gets the new claims of the set. A decided card answers the set when
// the set holds no claim that the card did not have. It reports whether a
// card answered the set.
func (s *Service) knownConflict(ctx context.Context, key string, ids []int64) (bool, error) {
	q := New(s.Pool)
	cs, err := q.CardsByKey(ctx, CardsByKeyParams{Kind: "conflict", Key: &key})
	if err != nil || len(cs) == 0 {
		return false, err
	}
	type conflictPayload struct {
		Claims    []int64 `json:"claims"`
		Predicate string  `json:"predicate"`
	}
	for _, c := range cs {
		var pl conflictPayload
		if err := json.Unmarshal(c.Payload, &pl); err != nil {
			return false, err
		}
		news := slices.DeleteFunc(slices.Clone(ids), func(id int64) bool { return slices.Contains(pl.Claims, id) })
		switch {
		case c.Status == "open" && len(news) > 0:
			pl.Claims = append(pl.Claims, news...)
			payload, err := json.Marshal(pl)
			if err != nil {
				return false, err
			}
			return true, q.SetCardPayload(ctx, SetCardPayloadParams{ID: c.ID, Payload: payload})
		case c.Status == "open":
			return true, nil
		case c.Status != "closed" && len(news) == 0:
			return true, nil
		}
	}
	return false, nil
}
