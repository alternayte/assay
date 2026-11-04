package content

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/entities"
)

// InferredSource is the evidence source type of facts that a model inferred
// from code. Facts from spec files outrank it.
const InferredSource = "decision"

// Inferred is a fact that a model inferred from one passage of an item. The
// caller checked that the quote is in the passage.
type Inferred struct {
	Fact
	ItemID      int64
	PassageHash []byte
}

// WriteInferred replaces the inferred evidence of items with the evidence of
// facts, in tx. A claim that loses its last evidence is retracted.
func WriteInferred(ctx context.Context, tx pgx.Tx, connectionID int64, items []int64, facts []Inferred, now time.Time, log *slog.Logger) error {
	q := New(tx)
	items = orEmptyIDs(items)
	before, err := q.InferredClaims(ctx, items)
	if err != nil {
		return err
	}
	if err := q.DeleteInferredEvidence(ctx, items); err != nil {
		return err
	}
	ids := map[string]int64{}
	for _, f := range facts {
		objType := claims.ObjText
		if f.Object != nil {
			objType = f.Object.Type
		}
		if err := claims.Check(f.Predicate, f.Subject.Type, objType); err != nil {
			log.Warn("inferred fact dropped", "predicate", f.Predicate, "reason", err.Error())
			continue
		}
		in := ingest{q: q, tx: tx, connectionID: connectionID, itemID: f.ItemID, log: log, ids: ids, origin: "model"}
		subject, err := in.entity(ctx, f.Subject)
		if errors.Is(err, errBadEntity) {
			log.Warn("inferred fact dropped", "predicate", f.Predicate, "reason", err.Error())
			continue
		}
		if err != nil {
			return err
		}
		var objectID *int64
		var objectText *string
		objectNorm := ""
		if f.Object != nil {
			id, err := in.entity(ctx, *f.Object)
			if errors.Is(err, errBadEntity) {
				log.Warn("inferred fact dropped", "predicate", f.Predicate, "reason", err.Error())
				continue
			}
			if err != nil {
				return err
			}
			objectID = &id
		} else {
			text := strings.TrimSpace(f.Text)
			objectText = &text
			objectNorm = entities.NormalizeAlias(text)
		}
		claimID, err := in.claim(ctx, subject, f.Predicate, objectID, objectText, objectNorm, "", in.origin, now)
		if err != nil {
			return err
		}
		if err := in.evidence(ctx, claimID, f.PassageHash, "passage", "supports", Normalize(f.Quote), InferredSource, now); err != nil {
			return err
		}
	}
	if len(before) == 0 {
		return nil
	}
	_, err = q.RetractUnsupported(ctx, RetractUnsupportedParams{Now: &now, Ids: before})
	return err
}
