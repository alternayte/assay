package claims

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/visibility"
)

// ErrNotVisible is a claim that is missing or has no visible support.
var ErrNotVisible = errors.New("claims: the claim does not exist")

// Resolve records a reviewer choice: the claim wins its subject, predicate
// and scope, and the other claims become Superseded (REQ-CLM-006).
func Resolve(ctx context.Context, tx pgx.Tx, vis visibility.Set, claimID int64, by string, now time.Time) (int64, error) {
	q := New(tx)
	c, err := q.ClaimScope(ctx, ClaimScopeParams{ID: claimID, Visible: vis})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotVisible
	}
	if err != nil {
		return 0, err
	}
	if err := q.CloseResolutions(ctx, CloseResolutionsParams{Now: &now, SubjectID: c.SubjectID, Predicate: c.Predicate, ScopeKey: c.ScopeEnv}); err != nil {
		return 0, err
	}
	var decider *string
	if by != "" {
		decider = &by
	}
	id, err := q.InsertResolution(ctx, InsertResolutionParams{SubjectID: c.SubjectID, Predicate: c.Predicate, ScopeKey: c.ScopeEnv,
		ChosenClaimID: claimID, DecidedBy: decider, DecidedAt: now})
	if err != nil {
		return 0, err
	}
	return id, audit.Write(ctx, tx, audit.Entry{Action: "claims.resolve", Target: fmt.Sprintf("claim:%d", claimID),
		Details: map[string]any{"resolution": id, "predicate": c.Predicate}})
}

// Reopen opens a conflict card for each open resolution that chose another
// claim, after new support for this claim. The resolution stays: the status
// rules show the claim again only to a reader who sees support newer than
// the resolution (REQ-CLM-006, SI-02). connections are the connections of
// the card inputs: the evidence of the claims and the sources of their
// entities.
func Reopen(ctx context.Context, tx pgx.Tx, claimID int64, connections []int64, now time.Time) error {
	q := New(tx)
	rs, err := q.ResolutionsAgainst(ctx, claimID)
	if err != nil {
		return err
	}
	for _, r := range rs {
		target := fmt.Sprintf("resolution:%d", r.ID)
		open, err := q.OpenCardExists(ctx, OpenCardExistsParams{Kind: "conflict", Target: target})
		if err != nil {
			return err
		}
		if open {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"resolution": r.ID, "chosen": r.ChosenClaimID, "claim": claimID,
			"claims": []int64{r.ChosenClaimID, claimID}, "at": now})
		if _, err := q.InsertCard(ctx, InsertCardParams{Kind: "conflict", Target: target, Payload: payload, Connections: connections}); err != nil {
			return err
		}
	}
	return nil
}
