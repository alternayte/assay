package content

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/models"
)

// EmbedBatch is the number of chunks in one embed call.
const EmbedBatch = 64

// Embedder fills the target embedding space with chunk vectors and
// activates it when it is complete.
type Embedder struct {
	Pool   *pgxpool.Pool
	Models *models.Gateway
	Log    *slog.Logger
}

// Run embeds the chunks that have no vector in the target space, until
// none is left or a call fails. A complete building space becomes active.
// Without the embed function it does nothing.
func (e *Embedder) Run(ctx context.Context) error {
	space, err := e.Models.TargetSpace(ctx)
	if err != nil {
		return e.stop(err)
	}
	if space == nil {
		return nil
	}
	tbl := space.Table()
	// The job reads all chunks, as sync does: no visible set applies.
	pending := fmt.Sprintf(`-- vis:system
SELECT c.id, c.connection_id, c.text FROM chunks c
WHERE NOT EXISTS (SELECT 1 FROM %s v WHERE v.chunk_id = c.id)
ORDER BY c.id LIMIT %d`, tbl, EmbedBatch)
	for {
		rows, err := e.Pool.Query(ctx, pending)
		if err != nil {
			return err
		}
		type chunk struct {
			id, conn int64
			text     string
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (chunk, error) {
			var c chunk
			return c, r.Scan(&c.id, &c.conn, &c.text)
		})
		if err != nil {
			return err
		}
		if len(batch) > 0 {
			texts := make([]string, len(batch))
			for i, c := range batch {
				texts[i] = c.text
			}
			res, err := e.Models.Embed(ctx, texts, "chunks")
			if err != nil {
				_ = e.progress(ctx, space, err.Error())
				return e.stop(err)
			}
			err = pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
				for i, c := range batch {
					if len(res.Vectors[i]) != int(space.Dim) {
						return fmt.Errorf("content: the embed model returned %d dimensions; the space has %d", len(res.Vectors[i]), space.Dim)
					}
					// The chunk can be gone after the read; the insert then
					// adds nothing.
					if _, err := tx.Exec(ctx, fmt.Sprintf(`-- vis:system
INSERT INTO %s (ref_kind, ref_id, connection_id, chunk_id, embedding)
SELECT 'chunk', c.id, c.connection_id, c.id, $2::vector FROM chunks c WHERE c.id = $1
ON CONFLICT (ref_kind, ref_id) DO UPDATE SET embedding = EXCLUDED.embedding`, tbl), c.id, models.Vector(res.Vectors[i])); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
		}
		if err := e.progress(ctx, space, ""); err != nil {
			return err
		}
		if len(batch) < EmbedBatch {
			break
		}
	}
	if space.Status == "building" {
		if err := e.Models.ActivateSpace(ctx, space.ID); err != nil {
			return err
		}
		e.Log.Info("embedding space active", "space", space.ID, "model", space.Model)
	}
	return nil
}

// stop ends a run without an error when the function is off or its budget
// is used up. Search then uses full-text and trigram only.
func (e *Embedder) stop(err error) error {
	if errors.Is(err, models.ErrDisabled) || errors.Is(err, models.ErrBudgetExceeded) {
		e.Log.Warn("embedding paused", "reason", err.Error())
		return nil
	}
	return err
}

func (e *Embedder) progress(ctx context.Context, s *models.Space, msg string) error {
	var done, total int
	err := e.Pool.QueryRow(ctx, fmt.Sprintf(`-- vis:system
SELECT (SELECT count(*) FROM %s WHERE ref_kind = 'chunk'), (SELECT count(*) FROM chunks)`, s.Table())).Scan(&done, &total)
	if err != nil {
		return err
	}
	return e.Models.SetSpaceProgress(ctx, s.ID, done, total, msg)
}

// EmbedArgs is the periodic embed job.
type EmbedArgs struct{}

// Kind is the River job kind.
func (EmbedArgs) Kind() string { return "embed" }

// EmbedWorker works the embed job.
type EmbedWorker struct {
	river.WorkerDefaults[EmbedArgs]
	E *Embedder
}

// Timeout bounds one run.
func (w *EmbedWorker) Timeout(*river.Job[EmbedArgs]) time.Duration { return time.Hour }

// Work embeds the pending chunks.
func (w *EmbedWorker) Work(ctx context.Context, _ *river.Job[EmbedArgs]) error { return w.E.Run(ctx) }

// PeriodicJobs returns the embed job, once a minute.
func (e *Embedder) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		return EmbedArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
	}, &river.PeriodicJobOpts{RunOnStart: true, ID: "embed"})}
}
