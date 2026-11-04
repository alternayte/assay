package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	gosync "sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/content"
)

// CursorOverlap is the overlap of an incremental sync with the previous
// cursor (REQ-SYN-001).
const CursorOverlap = 5 * time.Minute

// RunArgs is the job of one sync run.
type RunArgs struct {
	RunID int64 `json:"run_id" river:"unique"`
}

// Kind is the River job kind.
func (RunArgs) Kind() string { return "sync_run" }

// Queue is the River queue of sync jobs. Model jobs use the default queue.
const Queue = "sync"

// InsertOpts keeps one job per run.
func (RunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: Queue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// RunWorker works sync runs.
type RunWorker struct {
	river.WorkerDefaults[RunArgs]
	S *Service
}

// Timeout bounds one run.
func (w *RunWorker) Timeout(*river.Job[RunArgs]) time.Duration { return 6 * time.Hour }

// Work runs the run of the job.
func (w *RunWorker) Work(ctx context.Context, job *river.Job[RunArgs]) error {
	return w.S.Run(ctx, job.Args.RunID)
}

// ScheduleArgs is the periodic job that queues due runs.
type ScheduleArgs struct{}

// Kind is the River job kind.
func (ScheduleArgs) Kind() string { return "sync_schedule" }

// ScheduleWorker queues the runs that the schedules of the connections
// make due.
type ScheduleWorker struct {
	river.WorkerDefaults[ScheduleArgs]
	S *Service
}

// Work queues due syncs and reconciliations.
func (w *ScheduleWorker) Work(ctx context.Context, _ *river.Job[ScheduleArgs]) error {
	return w.S.Schedule(ctx)
}

// Workers adds the sync workers.
func (s *Service) Workers(ws *river.Workers) {
	river.AddWorker(ws, &RunWorker{S: s})
	river.AddWorker(ws, &ScheduleWorker{S: s})
}

// PeriodicJobs returns the periodic sync jobs.
func (s *Service) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		return ScheduleArgs{}, &river.InsertOpts{Queue: Queue, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
	}, &river.PeriodicJobOpts{RunOnStart: true, ID: "sync_schedule"})}
}

// Schedule queues the due runs and fails runs that stopped without a
// result.
func (s *Service) Schedule(ctx context.Context) error {
	now := s.now()
	q := New(s.Pool)
	if _, err := q.FailStaleRuns(ctx, FailStaleRunsParams{Now: &now, Before: ptr(now.Add(-7 * time.Hour))}); err != nil {
		return err
	}
	syncs, err := q.DueSyncs(ctx, &now)
	if err != nil {
		return err
	}
	for _, id := range syncs {
		if _, err := s.Queue(ctx, id, "sync", "schedule"); err != nil && !errors.Is(err, ErrRunActive) {
			return err
		}
	}
	recs, err := q.DueReconciles(ctx, &now)
	if err != nil {
		return err
	}
	for _, id := range recs {
		if _, err := s.Queue(ctx, id, "reconcile", "schedule"); err != nil && !errors.Is(err, ErrRunActive) {
			return err
		}
	}
	return nil
}

// errPaused fails a run of a paused connection.
var errPaused = errors.New("sync: the connection is paused")

// errPausedByAdmin ends a run that a pause stopped.
var errPausedByAdmin = errors.New("sync: an admin paused the connection")

// pausedReason is the status reason of a pause and the error of the runs
// that it stops.
const pausedReason = "An admin paused the connection."

// cancelRuns cancels the sync jobs of the unfinished runs of a connection.
// A running job then ends its run as failed with pausedReason.
func (s *Service) cancelRuns(ctx context.Context, connectionID int64) error {
	res, err := s.River.JobList(ctx, river.NewJobListParams().Kinds(RunArgs{}.Kind()).
		States(rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			rivertype.JobStateRunning).
		Where("(args->>'run_id')::bigint IN (SELECT id FROM sync_runs WHERE connection_id = @connection)",
			river.NamedArgs{"connection": connectionID}).First(100))
	if err != nil {
		return err
	}
	for _, j := range res.Jobs {
		if _, err := s.River.JobCancel(ctx, j.ID); err != nil {
			return err
		}
	}
	return nil
}

// ErrRunActive means that a run of the same kind is queued or running.
var ErrRunActive = errors.New("sync: a run of this kind is queued or running")

// Queue creates a run and its job in one transaction.
func (s *Service) Queue(ctx context.Context, connectionID int64, kind, trigger string) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		if _, err := q.ActiveRun(ctx, ActiveRunParams{ConnectionID: connectionID, Kind: kind}); err == nil {
			return ErrRunActive
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var err error
		id, err = q.InsertRun(ctx, InsertRunParams{ConnectionID: connectionID, Kind: kind, Trigger: trigger, Now: s.now()})
		if err != nil {
			return err
		}
		_, err = s.River.InsertTx(ctx, tx, RunArgs{RunID: id}, nil)
		return err
	})
	return id, err
}

// Skip is one skipped item of a run.
type Skip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// stats counts the outcomes of a run. Workers add to it concurrently.
type stats struct {
	mu                                        gosync.Mutex
	seen, created, updated, unchanged, delete int32
	skipped                                   []Skip
	failed                                    error
}

func (st *stats) add(o content.Outcome) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.seen++
	switch o {
	case content.Created:
		st.created++
	case content.Updated:
		st.updated++
	default:
		st.unchanged++
	}
}

func (st *stats) skip(id, reason string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.seen++
	st.skipped = append(st.skipped, Skip{ID: id, Reason: reason})
}

func (st *stats) deleted() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.delete++
}

func (st *stats) fail(err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.failed == nil {
		st.failed = err
	}
}

// replace sets the error of the run, in place of the first error.
func (st *stats) replace(err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.failed = err
}

func (st *stats) err() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.failed
}

// Run works one sync run to its end. A 401 or 403 pauses the connection in
// the same transaction.
func (s *Service) Run(ctx context.Context, runID int64) error {
	q := New(s.Pool)
	run, err := q.GetRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.Status == "succeeded" || run.Status == "failed" {
		return nil
	}
	conn, err := q.GetConnection(ctx, run.ConnectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.now()
	if _, err := q.StartRun(ctx, StartRunParams{ID: runID, Now: &now, Cursor: conn.Cursor}); err != nil {
		return err
	}
	st := &stats{}
	cursor := conn.Cursor
	if conn.Status == "paused" {
		st.fail(errPaused)
	} else if src, err := s.sourceOf(ctx, conn); err != nil {
		// A shutdown here also requeues the run below.
		if !stopped(ctx) {
			return err
		}
	} else if k, err := s.open(ctx, src); err != nil {
		st.fail(err)
	} else if run.Kind == "reconcile" {
		s.reconcile(ctx, conn, k, st)
	} else {
		cursor = s.sync(ctx, conn, k, st)
	}
	if stopped(ctx) {
		// The process stops. The run goes back to the queue and starts
		// again after the restart. The items it stored stay, and the
		// next attempt skips them.
		if err := New(s.Pool).RequeueRun(context.WithoutCancel(ctx), runID); err != nil {
			return err
		}
		return river.JobSnooze(time.Second)
	}
	if errors.Is(context.Cause(ctx), river.ErrJobCancelledRemotely) {
		if c, err := New(s.Pool).GetConnection(context.WithoutCancel(ctx), conn.ID); err == nil && c.Status == "paused" {
			st.replace(errPausedByAdmin)
		}
	}
	return s.finish(ctx, conn, run, cursor, st)
}

// stopped reports a run that the shutdown of the process cancelled. A
// timeout and a cancel in the job queue end the run as failed.
func stopped(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled) && !errors.Is(context.Cause(ctx), river.ErrJobCancelledRemotely)
}

func (s *Service) finish(ctx context.Context, conn Connection, run SyncRun, cursor string, st *stats) error {
	now := s.now()
	failed := st.err()
	status, msg := "succeeded", ""
	if failed != nil {
		status, msg = "failed", sourceError(failed)
	}
	skipped, _ := json.Marshal(orEmpty(st.skipped))
	err := pgx.BeginFunc(context.WithoutCancel(ctx), s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		if errors.Is(failed, connectors.ErrUnauthorized) {
			if _, err := q.SetStatus(ctx, SetStatusParams{ID: conn.ID, Status: "paused", Reason: msg}); err != nil {
				return err
			}
			if err := audit.Write(ctx, tx, audit.Entry{Action: "connections.pause", Target: fmt.Sprintf("connection:%d", conn.ID),
				Details: map[string]any{"reason": msg, "automatic": true}}); err != nil {
				return err
			}
		}
		if failed == nil {
			switch run.Kind {
			case "sync":
				if err := q.SetCursor(ctx, SetCursorParams{ID: conn.ID, Cursor: cursor, Now: &now}); err != nil {
					return err
				}
			case "reconcile":
				if err := q.FinishReconcile(ctx, FinishReconcileParams{ID: conn.ID, Now: &now}); err != nil {
					return err
				}
			}
		}
		if err := q.FinishRun(ctx, FinishRunParams{ID: run.ID, Status: status, Now: &now, CursorTo: cursor, Seen: st.seen,
			Created: st.created, Updated: st.updated, Unchanged: st.unchanged, Deleted: st.delete, Skipped: skipped, Error: msg}); err != nil {
			return err
		}
		if failed == nil && s.AfterRun != nil && len(conn.CodeGlobs) > 0 {
			return s.AfterRun(ctx, tx, conn.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if s.Metrics != nil {
		s.Metrics.Runs.WithLabelValues(run.Kind, status).Inc()
		s.Metrics.Items.WithLabelValues("created").Add(float64(st.created))
		s.Metrics.Items.WithLabelValues("updated").Add(float64(st.updated))
		s.Metrics.Items.WithLabelValues("unchanged").Add(float64(st.unchanged))
		s.Metrics.Items.WithLabelValues("deleted").Add(float64(st.delete))
		s.Metrics.Items.WithLabelValues("skipped").Add(float64(len(st.skipped)))
	}
	s.Log.Info("sync run finished", "connection", conn.ID, "run", run.ID, "kind", run.Kind, "status", status,
		"created", st.created, "updated", st.updated, "unchanged", st.unchanged, "deleted", st.delete, "skipped", len(st.skipped), "error", msg)
	return nil
}

// listBatch is the number of listed refs that sync stores before it saves
// the cursor and lets the connector read on. It is the page size of a
// Confluence listing with bodies.
const listBatch = 50

// sync stores the items changed since the cursor minus the overlap. It
// streams the listing: each batch of refs is stored before the connector
// reads on, and the cursor then moves to the newest update time of the
// batch, so that a run that stops continues from there. It returns the new
// cursor: the newest update time it handled.
func (s *Service) sync(ctx context.Context, conn Connection, k connectors.Connector, st *stats) string {
	var since time.Time
	cursor := conn.Cursor
	if cursor != "" {
		if t, err := time.Parse(time.RFC3339Nano, cursor); err == nil {
			since = t.Add(-CursorOverlap)
		}
	}
	newest := time.Time{}
	if cursor != "" {
		newest, _ = time.Parse(time.RFC3339Nano, cursor)
	}
	stored := &storedItems{}
	var batch []connectors.Ref
	flush := func() error {
		defer func() { batch = batch[:0] }()
		if err := s.stored(ctx, conn.ID, batch, stored); err != nil {
			return err
		}
		var work []connectors.Ref
		for _, r := range batch {
			if stored.has(r) {
				st.add(content.Unchanged)
				continue
			}
			work = append(work, r)
		}
		s.fetchAll(ctx, conn, k, work, st)
		if err := st.err(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			// A stop can end the batch before each ref is stored.
			return err
		}
		at := newest
		for _, r := range batch {
			if r.Updated.After(at) {
				at = r.Updated
			}
		}
		if !at.After(newest) {
			return nil
		}
		newest = at
		cursor = newest.UTC().Format(time.RFC3339Nano)
		now := s.now()
		return New(s.Pool).SetCursor(ctx, SetCursorParams{ID: conn.ID, Cursor: cursor, Now: &now})
	}
	err := k.Changed(ctx, since, func(r connectors.Ref) error {
		batch = append(batch, r)
		if len(batch) < listBatch {
			return nil
		}
		return flush()
	})
	if err == nil && len(batch) > 0 {
		err = flush()
	}
	if err != nil {
		st.fail(err)
		return cursor
	}
	if sl, ok := k.(connectors.SkipLister); ok {
		for _, sk := range sl.Skipped() {
			st.skip(sk.ID, sk.Reason)
		}
	}
	return cursor
}

// storedItems are the revisions and source update times of the stored
// items of a connection. Each map is read once, when a ref first needs it.
type storedItems struct {
	revisions map[string]string
	updated   map[string]time.Time
}

// has reports whether the stored item is the listed one: the same revision,
// or, when the item has no stored revision, the same or a later source
// update time. Items that an older Assay stored have no revision. Sync
// skips the fetch, so that a run after an interrupted one fetches only the
// items it did not store.
func (st *storedItems) has(r connectors.Ref) bool {
	if rev, ok := st.revisions[r.ID]; ok && r.Version != "" {
		return rev == r.Version
	}
	at, ok := st.updated[r.ID]
	return ok && !r.Updated.IsZero() && !r.Updated.After(at)
}

// stored reads the stored revisions and source update times of a
// connection into out, each the first time a ref has one to compare. Items
// that this run stores later are not in the maps; the listing names each
// item once.
func (s *Service) stored(ctx context.Context, connectionID int64, refs []connectors.Ref, out *storedItems) error {
	q := New(s.Pool)
	if out.revisions == nil && slices.ContainsFunc(refs, func(r connectors.Ref) bool { return r.Version != "" }) {
		rows, err := q.ItemRevisions(ctx, connectionID)
		if err != nil {
			return err
		}
		out.revisions = make(map[string]string, len(rows))
		for _, r := range rows {
			out.revisions[r.ExternalID] = r.Revision
		}
	}
	if out.updated == nil && slices.ContainsFunc(refs, func(r connectors.Ref) bool { return !r.Updated.IsZero() }) {
		rows, err := q.ItemUpdates(ctx, connectionID)
		if err != nil {
			return err
		}
		out.updated = make(map[string]time.Time, len(rows))
		for _, r := range rows {
			if r.SourceUpdatedAt != nil {
				out.updated[r.ExternalID] = *r.SourceUpdatedAt
			}
		}
	}
	return nil
}

// reconcile lists all IDs in scope, tombstones the items that are gone and
// fetches the items that sync missed.
func (s *Service) reconcile(ctx context.Context, conn Connection, k connectors.Connector, st *stats) {
	inScope := map[string]bool{}
	if err := k.ListIDs(ctx, func(id string) error { inScope[id] = true; return nil }); err != nil {
		st.fail(err)
		return
	}
	stored, err := New(s.Pool).ItemIDs(ctx, conn.ID)
	if err != nil {
		st.fail(err)
		return
	}
	known := map[string]bool{}
	for _, it := range stored {
		known[it.ExternalID] = true
		if !inScope[it.ExternalID] {
			if err := s.tombstone(ctx, it.ID); err != nil {
				st.fail(err)
				return
			}
			st.deleted()
		}
	}
	var missing []connectors.Ref
	for id := range inScope {
		if !known[id] {
			missing = append(missing, connectors.Ref{ID: id})
		}
	}
	s.fetchAll(ctx, conn, k, missing, st)
}

func (s *Service) tombstone(ctx context.Context, itemID int64) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return content.Tombstone(ctx, tx, itemID, s.now())
	})
}

// fetchAll stores items with the concurrency of the connection. It fetches
// each ref that does not carry its item. A 401 or 403 stops the run.
func (s *Service) fetchAll(ctx context.Context, conn Connection, k connectors.Connector, refs []connectors.Ref, st *stats) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan connectors.Ref)
	var wg gosync.WaitGroup
	for range max(1, int(conn.RateConcurrency)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range work {
				if err := s.fetchOne(ctx, conn, k, r, st); err != nil {
					st.fail(err)
					cancel()
				}
			}
		}()
	}
	for _, r := range refs {
		if ctx.Err() != nil {
			break
		}
		select {
		case work <- r:
		case <-ctx.Done():
		}
	}
	close(work)
	wg.Wait()
}

func (s *Service) fetchOne(ctx context.Context, conn Connection, k connectors.Connector, r connectors.Ref, st *stats) error {
	id, it := r.ID, r.Item
	var err error
	switch {
	case r.Skip != "":
		err = &connectors.SkipError{Reason: r.Skip}
	case it == nil:
		it, err = k.Fetch(ctx, id)
	}
	var skip *connectors.SkipError
	switch {
	case errors.As(err, &skip):
		// A stored version of a skipped item would stay stale, so it goes.
		st.skip(id, skip.Reason)
		return s.tombstoneStored(ctx, conn.ID, id, st)
	case errors.Is(err, connectors.ErrNotFound):
		return s.tombstoneStored(ctx, conn.ID, id, st)
	case err != nil:
		return err
	}
	var outcome content.Outcome
	// Items that name the same entities and claims, such as the entities of
	// one catalog, lock rows in different orders. A deadlock rolls back one
	// transaction, and the item is stored again.
	for attempt := 1; ; attempt++ {
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			outcome, err = content.Store(ctx, tx, conn.ID, conn.DefaultDomainID, it, s.Log)
			return err
		})
		if !retryable(err) || attempt == storeAttempts {
			break
		}
	}
	if err != nil {
		return err
	}
	st.add(outcome)
	return nil
}

// storeAttempts bounds the attempts to store one item.
const storeAttempts = 5

// retryable reports a deadlock or a serialization failure: the
// transaction can run again.
func retryable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40P01" || pe.Code == "40001")
}

// tombstoneStored tombstones the stored item with an external ID, if any.
func (s *Service) tombstoneStored(ctx context.Context, connectionID int64, externalID string, st *stats) error {
	existing, err := New(s.Pool).ItemByExternalID(ctx, ItemByExternalIDParams{ConnectionID: connectionID, ExternalID: externalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.tombstone(ctx, existing); err != nil {
		return err
	}
	st.deleted()
	return nil
}

func ptr[T any](v T) *T { return &v }

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
