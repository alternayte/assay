package content

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/entities"
)

// Extracted is one validated claim from a passage, with resolved entities.
type Extracted struct {
	Subject   int64
	Predicate string
	Object    *int64
	Text      string
	Scope     string
	Stance    string // supports or disputes
	Quote     string // normalized, a substring of the passage
}

// AddExtracted writes extracted claims of one passage in tx. The same fact
// from another passage or a field is the same claim with one more evidence
// row (REQ-CLM-002). New claims have the origin model.
func AddExtracted(ctx context.Context, tx pgx.Tx, connectionID, itemID int64, passageHash []byte, at time.Time, cs []Extracted,
	log *slog.Logger) error {
	in := ingest{q: New(tx), tx: tx, connectionID: connectionID, itemID: itemID, log: log, ids: map[string]int64{}}
	source, err := in.sourceType(ctx)
	if err != nil {
		return err
	}
	for _, c := range cs {
		var text *string
		norm := ""
		if c.Object == nil {
			t := strings.TrimSpace(c.Text)
			text, norm = &t, entities.NormalizeAlias(t)
		}
		id, err := in.claim(ctx, c.Subject, c.Predicate, c.Object, text, norm, c.Scope, "model", at)
		if err != nil {
			return err
		}
		if err := in.evidence(ctx, id, passageHash, "passage", c.Stance, c.Quote, source, at); err != nil {
			return err
		}
	}
	return nil
}
