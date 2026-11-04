package content

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
)

// ChunkHashArgs is the job that fills the text hash of the chunks stored
// before chunks had one. A single update of millions of rows in the
// migration would block the start.
type ChunkHashArgs struct{}

// Kind is the River job kind.
func (ChunkHashArgs) Kind() string { return "chunk_hash" }

// ChunkHashWorker fills text hashes in batches of 10,000 chunks, one
// statement each, until none is left.
type ChunkHashWorker struct {
	river.WorkerDefaults[ChunkHashArgs]
	Pool *pgxpool.Pool
}

// Work fills the text hashes.
func (w *ChunkHashWorker) Work(ctx context.Context, _ *river.Job[ChunkHashArgs]) error {
	for {
		n, err := New(w.Pool).FillChunkHashes(ctx)
		if err != nil || n == 0 {
			return err
		}
	}
}

// ChunkHashJobs returns the fill job, at start and once a day. It ends at
// once when every chunk has a hash.
func ChunkHashJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour), func() (river.JobArgs, *river.InsertOpts) {
		return ChunkHashArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: 24 * time.Hour}}
	}, &river.PeriodicJobOpts{RunOnStart: true, ID: "chunk_hash"})}
}
