package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	gosync "sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

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

// InsertOpts keeps one job per run.
func (RunArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
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
		return ScheduleArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
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

func (st *stats) err() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.failed
}

// Run works one sync run to its end. A 401 or 403 pauses the connection,
// which hides its content in the same transaction.
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
	} else {
		src, err := s.sourceOf(ctx, conn)
		if err != nil {
			return err
		}
		k, err := s.open(ctx, src)
		if err != nil {
			st.fail(err)
		} else if run.Kind == "reconcile" {
			s.reconcile(ctx, conn, k, st)
		} else {
			cursor = s.sync(ctx, conn, k, st)
		}
	}
	return s.finish(ctx, conn, run, cursor, st)
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
		return q.FinishRun(ctx, FinishRunParams{ID: run.ID, Status: status, Now: &now, CursorTo: cursor, Seen: st.seen,
			Created: st.created, Updated: st.updated, Unchanged: st.unchanged, Deleted: st.delete, Skipped: skipped, Error: msg})
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

// sync fetches the items changed since the cursor minus the overlap. It
// returns the new cursor: the newest update time it handled.
func (s *Service) sync(ctx context.Context, conn Connection, k connectors.Connector, st *stats) string {
	var since time.Time
	if conn.Cursor != "" {
		if t, err := time.Parse(time.RFC3339Nano, conn.Cursor); err == nil {
			since = t.Add(-CursorOverlap)
		}
	}
	var refs []connectors.Ref
	if err := k.Changed(ctx, since, func(r connectors.Ref) error { refs = append(refs, r); return nil }); err != nil {
		st.fail(err)
		return conn.Cursor
	}
	ids := make([]string, len(refs))
	newest := time.Time{}
	if conn.Cursor != "" {
		newest, _ = time.Parse(time.RFC3339Nano, conn.Cursor)
	}
	for i, r := range refs {
		ids[i] = r.ID
		if r.Updated.After(newest) {
			newest = r.Updated
		}
	}
	s.fetchAll(ctx, conn, k, ids, st)
	if newest.IsZero() {
		return conn.Cursor
	}
	return newest.UTC().Format(time.RFC3339Nano)
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
	var missing []string
	for id := range inScope {
		if !known[id] {
			missing = append(missing, id)
		}
	}
	s.fetchAll(ctx, conn, k, missing, st)
}

func (s *Service) tombstone(ctx context.Context, itemID int64) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return content.Tombstone(ctx, tx, itemID, s.now())
	})
}

// fetchAll fetches and stores items with the concurrency of the
// connection. A 401 or 403 stops the run.
func (s *Service) fetchAll(ctx context.Context, conn Connection, k connectors.Connector, ids []string, st *stats) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan string)
	var wg gosync.WaitGroup
	for range max(1, int(conn.RateConcurrency)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range work {
				if err := s.fetchOne(ctx, conn, k, id, st); err != nil {
					st.fail(err)
					cancel()
				}
			}
		}()
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

func (s *Service) fetchOne(ctx context.Context, conn Connection, k connectors.Connector, id string, st *stats) error {
	it, err := k.Fetch(ctx, id)
	var skip *connectors.SkipError
	switch {
	case errors.As(err, &skip):
		st.skip(id, skip.Reason)
		return nil
	case errors.Is(err, connectors.ErrNotFound):
		existing, err := New(s.Pool).ItemByExternalID(ctx, ItemByExternalIDParams{ConnectionID: conn.ID, ExternalID: id})
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
	case err != nil:
		return err
	}
	var outcome content.Outcome
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		outcome, err = content.Store(ctx, tx, conn.ID, conn.DefaultDomainID, it, s.Log)
		return err
	})
	if err != nil {
		return err
	}
	st.add(outcome)
	return nil
}

func ptr[T any](v T) *T { return &v }

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
