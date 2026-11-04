package content

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/alternayte/assay/internal/models"
)

// EmbedBatch is the number of chunks in one embed call.
const EmbedBatch = 64

// maxEmbedRunes bounds one embed input. A chunk of MaxChunkTokens has fewer
// runes. A chunk stored before Chunks cut long words can have many more,
// and the embed model refuses an input past its own limit; its vector then
// covers the start of the chunk.
const maxEmbedRunes = 4 * MaxChunkTokens

func embedText(s string) string {
	if utf8.RuneCountInString(s) <= maxEmbedRunes {
		return s
	}
	return string([]rune(s)[:maxEmbedRunes])
}

// Embedder fills the target embedding space with chunk and profile vectors
// and activates it when it is complete.
type Embedder struct {
	Pool   *pgxpool.Pool
	Models *models.Gateway
	Log    *slog.Logger
	// Shards is the number of embed shards. Shard i embeds the chunks with
	// id % Shards = i.
	Shards int
}

// recountEvery is the time between two exact counts of done and total.
const recountEvery = 5 * time.Minute

func (e *Embedder) shards() int { return max(1, e.Shards) }

// Run works all shards in turn. The last one activates a complete building
// space. The command assay reindex uses it.
func (e *Embedder) Run(ctx context.Context) error {
	n := e.shards()
	for i := range n {
		if err := e.Shard(ctx, i, n); err != nil {
			return err
		}
	}
	return nil
}

type pendingChunk struct {
	id, conn int64
	text     string
}

// Shard embeds the chunks of shard i of n that have no vector in the
// target space, until none is left or a call fails. Shard 0 also embeds
// the profiles and counts done and total exactly. A shard that finds no
// pending chunk activates a building space when no shard has pending
// work. Without the embed function it does nothing.
func (e *Embedder) Shard(ctx context.Context, i, n int) error {
	space, err := e.Models.TargetSpace(ctx)
	if err != nil {
		return e.stop(err)
	}
	if space == nil {
		return nil
	}
	tbl := space.Table()
	var counted time.Time
	if i == 0 {
		if err := e.recount(ctx, space); err != nil {
			return err
		}
		counted = time.Now()
	}
	// The job reads all chunks, as sync does: no visible set applies.
	pending := fmt.Sprintf(`-- vis:system
SELECT c.id, c.connection_id, c.text FROM chunks c
WHERE c.id > $1 AND c.id %% $2 = $3 AND NOT EXISTS (SELECT 1 FROM %s v WHERE v.chunk_id = c.id)
ORDER BY c.id LIMIT %d`, tbl, EmbedBatch)
	var last int64
	for {
		rows, err := e.Pool.Query(ctx, pending, last, n, i)
		if err != nil {
			return err
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pendingChunk, error) {
			var c pendingChunk
			return c, r.Scan(&c.id, &c.conn, &c.text)
		})
		if err != nil {
			return err
		}
		if len(batch) > 0 {
			last = batch[len(batch)-1].id
			added, err := e.embedChunks(ctx, space, batch)
			if err != nil {
				if serr := e.Models.SetSpaceError(ctx, space.ID, err.Error()); serr != nil {
					return serr
				}
				return e.stop(err)
			}
			if err := e.Models.AddSpaceDone(ctx, space.ID, added); err != nil {
				return err
			}
		}
		if i == 0 && time.Since(counted) >= recountEvery {
			if err := e.recount(ctx, space); err != nil {
				return err
			}
			counted = time.Now()
		}
		if len(batch) < EmbedBatch {
			break
		}
	}
	if i == 0 {
		if err := e.profiles(ctx, space); err != nil {
			return e.stop(err)
		}
	}
	if space.Status != "building" {
		return nil
	}
	var busy bool
	if err := e.Pool.QueryRow(ctx, fmt.Sprintf(`-- vis:system
SELECT EXISTS (SELECT 1 FROM chunks c WHERE NOT EXISTS (SELECT 1 FROM %[1]s v WHERE v.chunk_id = c.id))
    OR EXISTS (SELECT 1 FROM entity_profiles p WHERE NOT EXISTS (SELECT 1 FROM %[1]s v WHERE v.ref_kind = 'profile' AND v.ref_id = p.entity_id))`,
		tbl)).Scan(&busy); err != nil || busy {
		return err
	}
	if err := e.Models.ActivateSpace(ctx, space.ID); err != nil {
		return err
	}
	if err := e.recount(ctx, space); err != nil {
		return err
	}
	e.Log.Info("embedding space active", "space", space.ID, "model", space.Model)
	return nil
}

// embedChunks writes the vectors of a batch and returns the number of new
// vector rows. A chunk whose text hash matches a chunk with a vector in the
// space gets a copy of that vector. The rest go to the model once per
// distinct text. A vector holds no text, and both chunks hold the same
// text, so a copy shows no one anything new.
func (e *Embedder) embedChunks(ctx context.Context, space *models.Space, batch []pendingChunk) (int, error) {
	tbl := space.Table()
	ids := make([]int64, len(batch))
	for i, c := range batch {
		ids[i] = c.id
	}
	rows, err := e.Pool.Query(ctx, fmt.Sprintf(`-- vis:system
INSERT INTO %[1]s (ref_kind, ref_id, connection_id, chunk_id, embedding)
SELECT DISTINCT ON (c.id) 'chunk', c.id, c.connection_id, c.id, v.embedding
FROM chunks c
JOIN chunks o ON o.text_hash = c.text_hash AND o.id <> c.id
JOIN %[1]s v ON v.ref_kind = 'chunk' AND v.ref_id = o.id
WHERE c.id = ANY($1) AND c.text_hash IS NOT NULL
ORDER BY c.id, o.id
ON CONFLICT (ref_kind, ref_id) DO NOTHING
RETURNING ref_id`, tbl), ids)
	if err != nil {
		return 0, err
	}
	copied, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	done := map[int64]bool{}
	for _, id := range copied {
		done[id] = true
	}
	var texts []string
	at := map[string]int{}
	var refs []int64
	var slots []int
	for _, c := range batch {
		if done[c.id] {
			continue
		}
		t := embedText(c.text)
		k, ok := at[t]
		if !ok {
			k = len(texts)
			at[t] = k
			texts = append(texts, t)
		}
		refs = append(refs, c.id)
		slots = append(slots, k)
	}
	if len(texts) == 0 {
		return len(copied), nil
	}
	res, err := e.Models.Embed(ctx, texts, "chunks")
	if err != nil {
		return 0, err
	}
	for _, v := range res.Vectors {
		if len(v) != int(space.Dim) {
			return 0, fmt.Errorf("content: the embed model returned %d dimensions; the space has %d", len(v), space.Dim)
		}
	}
	vecs := make([]string, len(refs))
	for i, k := range slots {
		vecs[i] = models.Vector(res.Vectors[k])
	}
	// A chunk can be gone after the read; the insert then adds nothing.
	tag, err := e.Pool.Exec(ctx, fmt.Sprintf(`-- vis:system
INSERT INTO %s (ref_kind, ref_id, connection_id, chunk_id, embedding)
SELECT 'chunk', c.id, c.connection_id, c.id, r.v::vector
FROM unnest($1::bigint[], $2::text[]) AS r (id, v) JOIN chunks c ON c.id = r.id
ON CONFLICT (ref_kind, ref_id) DO NOTHING`, tbl), refs, vecs)
	if err != nil {
		return 0, err
	}
	return len(copied) + int(tag.RowsAffected()), nil
}

// profiles embeds the entity profiles that have no vector in the space. A
// profile that changes loses its vector, so it is embedded again.
func (e *Embedder) profiles(ctx context.Context, space *models.Space) error {
	tbl := space.Table()
	pending := fmt.Sprintf(`-- vis:system
SELECT p.entity_id, p.connections[1], p.text FROM entity_profiles p
WHERE NOT EXISTS (SELECT 1 FROM %s v WHERE v.ref_kind = 'profile' AND v.ref_id = p.entity_id)
ORDER BY p.entity_id LIMIT %d`, tbl, EmbedBatch)
	for {
		rows, err := e.Pool.Query(ctx, pending)
		if err != nil {
			return err
		}
		type profile struct {
			id, conn int64
			text     string
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (profile, error) {
			var p profile
			return p, r.Scan(&p.id, &p.conn, &p.text)
		})
		if err != nil || len(batch) == 0 {
			return err
		}
		texts := make([]string, len(batch))
		for i, p := range batch {
			texts[i] = p.text
		}
		res, err := e.Models.Embed(ctx, texts, "profiles")
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
			for i, p := range batch {
				if len(res.Vectors[i]) != int(space.Dim) {
					return fmt.Errorf("content: the embed model returned %d dimensions; the space has %d", len(res.Vectors[i]), space.Dim)
				}
				if _, err := tx.Exec(ctx, fmt.Sprintf(`-- vis:system
INSERT INTO %s (ref_kind, ref_id, connection_id, embedding)
SELECT 'profile', p.entity_id, $2, $3::vector FROM entity_profiles p WHERE p.entity_id = $1
ON CONFLICT (ref_kind, ref_id) DO UPDATE SET embedding = EXCLUDED.embedding`, tbl), p.id, p.conn, models.Vector(res.Vectors[i])); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil || len(batch) < EmbedBatch {
			return err
		}
	}
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

// recount sets done and total of a space to exact counts.
func (e *Embedder) recount(ctx context.Context, s *models.Space) error {
	var done, total int
	err := e.Pool.QueryRow(ctx, fmt.Sprintf(`-- vis:system
SELECT (SELECT count(*) FROM %s WHERE ref_kind = 'chunk'), (SELECT count(*) FROM chunks)`, s.Table())).Scan(&done, &total)
	if err != nil {
		return err
	}
	return e.Models.SetSpaceCount(ctx, s.ID, done, total)
}

// EmbedArgs is the periodic job of one embed shard.
type EmbedArgs struct {
	Shard  int `json:"shard"`
	Shards int `json:"shards"`
}

// Kind is the River job kind.
func (EmbedArgs) Kind() string { return "embed" }

// EmbedQueue is the River queue of the embed jobs. It has one worker per
// shard. The shards split the chunks, so two never embed the same chunk.
const EmbedQueue = "embed"

// InsertOpts keeps one unfinished job per shard, so that jobs do not pile
// up while a long run works.
func (EmbedArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EmbedQueue, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable,
		rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateRunning, rivertype.JobStateScheduled}}}
}

// EmbedWorker works the embed jobs.
type EmbedWorker struct {
	river.WorkerDefaults[EmbedArgs]
	E *Embedder
}

// Timeout bounds one run.
func (w *EmbedWorker) Timeout(*river.Job[EmbedArgs]) time.Duration { return time.Hour }

// Work embeds the pending chunks of the shard. A job without a shard count
// is from before the shards: it is shard 0.
func (w *EmbedWorker) Work(ctx context.Context, job *river.Job[EmbedArgs]) error {
	if job.Args.Shards < 1 {
		return w.E.Shard(ctx, 0, w.E.shards())
	}
	return w.E.Shard(ctx, job.Args.Shard, job.Args.Shards)
}

// PeriodicJobs returns one embed job per shard, once a minute.
func (e *Embedder) PeriodicJobs() []*river.PeriodicJob {
	n := e.shards()
	out := make([]*river.PeriodicJob, n)
	for i := range n {
		out[i] = river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
			return EmbedArgs{Shard: i, Shards: n}, nil
		}, &river.PeriodicJobOpts{RunOnStart: true, ID: fmt.Sprintf("embed_%d", i)})
	}
	return out
}
