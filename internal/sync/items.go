package sync

import (
	"context"
	"errors"
	"slices"
	gosync "sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/connectors"
)

// ItemsArgs is the job of a webhook: it reads the named items of a
// connection again from the source. Repos are the repositories of a push
// that names no paths, such as an Azure DevOps push: the job compares their
// files with the stored revisions.
type ItemsArgs struct {
	ConnectionID int64    `json:"connection_id" river:"unique"`
	IDs          []string `json:"ids" river:"unique"`
	Repos        []string `json:"repos,omitempty" river:"unique"`
}

// Kind is the River job kind.
func (ItemsArgs) Kind() string { return "sync_items" }

// itemsUnique is the period in which the same items cause one job. A source
// that sends one event per field change then causes one read.
const itemsUnique = time.Minute

// InsertOpts keeps one job per set of items and minute.
func (ItemsArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: Queue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: itemsUnique}}
}

// ItemsWorker reads the items that webhooks name.
type ItemsWorker struct {
	river.WorkerDefaults[ItemsArgs]
	S *Service
}

// Timeout bounds one job.
func (w *ItemsWorker) Timeout(*river.Job[ItemsArgs]) time.Duration { return 30 * time.Minute }

// Work reads the items of the job.
func (w *ItemsWorker) Work(ctx context.Context, job *river.Job[ItemsArgs]) error {
	return w.S.SyncItems(ctx, job.Args.ConnectionID, job.Args.IDs, job.Args.Repos)
}

// itemsWait is the delay of a job while a reconciliation of its connection
// runs.
const itemsWait = 30 * time.Second

// SyncItems reads the named items of a connection with Fetch and stores
// them, with the rate and the concurrency of the connection. A paused
// connection drops the job: pause stops every read of the source. While a
// reconciliation runs the job waits, because the reconciliation tombstones
// the stored items that its listing missed. An item that Fetch does not
// find becomes a tombstone. A 401 or 403 pauses the connection.
func (s *Service) SyncItems(ctx context.Context, connectionID int64, ids, repos []string) error {
	q := New(s.Pool)
	conn, err := q.GetConnection(ctx, connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if conn.Status == "paused" || !webhookKind(conn.Kind, conn.BaseUrl, conn.Options) || conn.RewriteFrom != "" {
		return nil
	}
	active, err := q.ReconcileActive(ctx, conn.ID)
	if err != nil {
		return err
	}
	if active {
		return river.JobSnooze(itemsWait)
	}
	src, err := s.sourceOf(ctx, conn)
	if err != nil {
		return err
	}
	k, err := s.open(ctx, src)
	if err != nil {
		return err
	}
	tg, ok := k.(connectors.Targeter)
	if !ok {
		return nil
	}
	st := &stats{}
	if fl, ok := k.(connectors.FileLister); ok {
		for _, repo := range repos {
			changed, err := s.changedFiles(ctx, conn.ID, fl, repo)
			if err != nil {
				st.fail(err)
				break
			}
			ids = append(ids, changed...)
		}
	}
	if st.err() == nil {
		slices.Sort(ids)
		s.fetchTargets(ctx, conn, k, tg, slices.Compact(ids), st)
	}
	c := st.counts()
	if s.Metrics != nil {
		s.Metrics.Items.WithLabelValues("created").Add(float64(c.Created))
		s.Metrics.Items.WithLabelValues("updated").Add(float64(c.Updated))
		s.Metrics.Items.WithLabelValues("unchanged").Add(float64(c.Unchanged))
		s.Metrics.Items.WithLabelValues("deleted").Add(float64(c.Deleted))
		s.Metrics.Items.WithLabelValues("skipped").Add(float64(len(c.Skipped)))
	}
	failed := st.err()
	msg := ""
	if failed != nil {
		msg = sourceError(failed)
	}
	s.Log.Info("webhook items synced", "connection", conn.ID, "items", len(ids), "created", c.Created, "updated", c.Updated,
		"unchanged", c.Unchanged, "deleted", c.Deleted, "skipped", len(c.Skipped), "error", msg)
	if errors.Is(failed, connectors.ErrUnauthorized) {
		bg := context.WithoutCancel(ctx)
		return s.inTx(bg, func(tx pgx.Tx) error { return pause(bg, tx, conn.ID, msg) })
	}
	return failed
}

// changedFiles returns the file IDs of a repository whose blob differs from
// the stored revision, and the stored files that the tree no longer has:
// their Fetch returns 404, and they become tombstones.
func (s *Service) changedFiles(ctx context.Context, connectionID int64, fl connectors.FileLister, repo string) ([]string, error) {
	var refs []connectors.Ref
	if err := fl.Files(ctx, repo, func(r connectors.Ref) error { refs = append(refs, r); return nil }); err != nil {
		return nil, err
	}
	stored := &storedItems{}
	if err := s.stored(ctx, connectionID, repo, refs, stored); err != nil {
		return nil, err
	}
	listed := make(map[string]bool, len(refs))
	var out []string
	for _, r := range refs {
		listed[r.ID] = true
		if !stored.has(r) {
			out = append(out, r.ID)
		}
	}
	rows, err := New(s.Pool).PartItemIDs(ctx, PartItemIDsParams{ConnectionID: connectionID, Part: repo})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, _, _, history := connectors.ParseHistoryID(r.ExternalID); !history && !listed[r.ExternalID] {
			out = append(out, r.ExternalID)
		}
	}
	return out, nil
}

// fetchTargets reads and stores the items of ids with the concurrency of
// the connection. The first error stops the reads.
func (s *Service) fetchTargets(ctx context.Context, conn Connection, k connectors.Connector, tg connectors.Targeter, ids []string, st *stats) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan string)
	var wg gosync.WaitGroup
	for range max(1, int(conn.RateConcurrency)) {
		wg.Go(func() {
			for id := range work {
				if err := s.fetchTarget(ctx, conn, k, tg, id, st); err != nil {
					st.fail(err)
					cancel()
				}
			}
		})
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		select {
		case work <- id:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
}

// fetchTarget reads one named item. An item outside the scope of the
// connection is not stored.
func (s *Service) fetchTarget(ctx context.Context, conn Connection, k connectors.Connector, tg connectors.Targeter, id string, st *stats) error {
	it, err := k.Fetch(ctx, id)
	var skip *connectors.SkipError
	switch {
	case errors.As(err, &skip):
		return s.fetchOne(ctx, conn, k, connectors.Part{}, connectors.Ref{ID: id, Skip: skip.Reason}, st)
	case errors.Is(err, connectors.ErrNotFound):
		return s.tombstoneStored(ctx, conn.ID, id, st)
	case err != nil:
		return err
	}
	part, ok, err := tg.PartOf(ctx, it)
	if err != nil || !ok {
		return err
	}
	return s.fetchOne(ctx, conn, k, connectors.Part{Key: part}, connectors.Ref{ID: id, Item: it}, st)
}
