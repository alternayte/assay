package content

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SplitSource splits the Markdown body of a source code item into its
// passages. The code package sets it, so that reindex splits code as sync
// does.
var SplitSource func(file, body string) []Passage

// Reindexed counts the result of a reindex.
type Reindexed struct {
	Items, Passages, Chunks int
}

// Reindex splits the current version of every item again and replaces its
// passages, chunks and full-text vectors, one transaction per item. The
// embedding vectors of the old chunks go with them; the embed job builds
// them again. Evidence whose passage is gone becomes stale. It is the
// operator command assay reindex, for a change of the splitter or the
// chunker, or for a damaged index.
func Reindex(ctx context.Context, pool *pgxpool.Pool) (Reindexed, error) {
	var out Reindexed
	var after int64
	for {
		items, err := New(pool).ReindexItems(ctx, ReindexItemsParams{After: after, Lim: 100})
		if err != nil {
			return out, err
		}
		for _, it := range items {
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				var code []Passage
				if it.Kind == "code" && SplitSource != nil {
					_, file, _ := strings.Cut(it.ExternalID, ":")
					code = SplitSource(file, it.Body)
				}
				ps, chunks, err := writeIndex(ctx, New(tx), it.ID, it.VersionID, it.ConnectionID, it.Title, it.Body, code)
				out.Passages += len(ps)
				out.Chunks += chunks
				return err
			})
			if err != nil {
				return out, err
			}
			out.Items++
			after = it.ID
		}
		if len(items) < 100 {
			return out, nil
		}
	}
}
