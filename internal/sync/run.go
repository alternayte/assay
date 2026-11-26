package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

// RunArgs is the job of one sync run. It lists the parts of the run and
// queues one part job per part.
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

// RunWorker splits sync runs into parts.
type RunWorker struct {
	river.WorkerDefaults[RunArgs]
	S *Service
}

// Timeout bounds the listing of the parts.
func (w *RunWorker) Timeout(*river.Job[RunArgs]) time.Duration { return 30 * time.Minute }

// Work splits the run of the job.
func (w *RunWorker) Work(ctx context.Context, job *river.Job[RunArgs]) error {
	return w.S.Split(ctx, job.Args.RunID)
}

// PartArgs is the job of one part of a sync run. Any worker process runs
// it.
type PartArgs struct {
	RunID int64  `json:"run_id" river:"unique"`
	Part  string `json:"part" river:"unique"`
}

// Kind is the River job kind.
func (PartArgs) Kind() string { return "sync_part" }

// InsertOpts keeps one job per part.
func (PartArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: Queue, MaxAttempts: 3, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// PartWorker works the parts of sync runs.
type PartWorker struct {
	river.WorkerDefaults[PartArgs]
	S *Service
}

// Timeout bounds one part.
func (w *PartWorker) Timeout(*river.Job[PartArgs]) time.Duration { return 6 * time.Hour }

// Work runs the part of the job.
func (w *PartWorker) Work(ctx context.Context, job *river.Job[PartArgs]) error {
	return w.S.RunPart(ctx, job.Args.RunID, job.Args.Part)
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
	river.AddWorker(ws, &PartWorker{S: s})
	river.AddWorker(ws, &ScheduleWorker{S: s})
}

// PeriodicJobs returns the periodic sync jobs.
func (s *Service) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(time.Minute), func() (river.JobArgs, *river.InsertOpts) {
		return ScheduleArgs{}, &river.InsertOpts{Queue: Queue, UniqueOpts: river.UniqueOpts{ByPeriod: time.Minute}}
	}, &river.PeriodicJobOpts{RunOnStart: true, ID: "sync_schedule"})}
}

// staleAfter is the age of a running run or part that stopped without a
// result. It is longer than the timeout of a part job.
const staleAfter = 7 * time.Hour

// Schedule queues the due runs and fails runs and parts that stopped
// without a result.
func (s *Service) Schedule(ctx context.Context) error {
	now := s.now()
	q := New(s.Pool)
	before := ptr(now.Add(-staleAfter))
	if _, err := q.FailStaleRuns(ctx, FailStaleRunsParams{Now: &now, Before: before}); err != nil {
		return err
	}
	runs, err := q.FailStaleParts(ctx, FailStalePartsParams{Now: &now, Before: before})
	if err != nil {
		return err
	}
	if err := s.settleAll(ctx, runs); err != nil {
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

// cancelRuns cancels the run and part jobs of the unfinished runs of a
// connection. A running part job then ends its part as failed with
// pausedReason.
func (s *Service) cancelRuns(ctx context.Context, connectionID int64) error {
	const page = 100
	params := river.NewJobListParams().Kinds(RunArgs{}.Kind(), PartArgs{}.Kind()).
		States(rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateScheduled,
			rivertype.JobStateRunning).
		Where("(args->>'run_id')::bigint IN (SELECT id FROM sync_runs WHERE connection_id = @connection)",
			river.NamedArgs{"connection": connectionID}).First(page)
	for {
		res, err := s.River.JobList(ctx, params)
		if err != nil {
			return err
		}
		for _, j := range res.Jobs {
			if _, err := s.River.JobCancel(ctx, j.ID); err != nil {
				return err
			}
		}
		if len(res.Jobs) < page {
			return nil
		}
		params = params.After(res.LastCursor)
	}
}

// ErrRunActive means that a run of the same kind is queued or running.
var ErrRunActive = errors.New("sync: a run of this kind is queued or running")

// Queue creates a run and its job in one transaction.
func (s *Service) Queue(ctx context.Context, connectionID int64, kind, trigger string) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = s.queueTx(ctx, tx, connectionID, kind, trigger)
		return err
	})
	return id, err
}

// queueTx creates a run and its job in tx.
func (s *Service) queueTx(ctx context.Context, tx pgx.Tx, connectionID int64, kind, trigger string) (int64, error) {
	q := New(tx)
	if _, err := q.ActiveRun(ctx, ActiveRunParams{ConnectionID: connectionID, Kind: kind}); err == nil {
		return 0, ErrRunActive
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	id, err := q.InsertRun(ctx, InsertRunParams{ConnectionID: connectionID, Kind: kind, Trigger: trigger, Now: s.now()})
	if err != nil {
		return 0, err
	}
	_, err = s.River.InsertTx(ctx, tx, RunArgs{RunID: id}, nil)
	return id, err
}

// Skip is one skipped item of a run.
type Skip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// stats counts the outcomes of a part. Workers add to it concurrently.
type stats struct {
	mu                                        gosync.Mutex
	seen, created, updated, unchanged, delete int32
	skipped                                   []Skip
	failed                                    error
	// rewrite counts the key rewrite of a run after a switch to Cloud.
	rewrite *rewriteCounts
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

// replace sets the error of the part, in place of the first error.
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

// counts are the stored stats of a part, and the sums of a run.
type counts struct {
	Seen      int32  `json:"seen"`
	Created   int32  `json:"created"`
	Updated   int32  `json:"updated"`
	Unchanged int32  `json:"unchanged"`
	Deleted   int32  `json:"deleted"`
	Skipped   []Skip `json:"skipped"`
	// Rewrite is set on the parts of a run after a switch to Cloud.
	Rewrite *rewriteCounts `json:"rewrite,omitempty"`
}

func (st *stats) counts() counts {
	st.mu.Lock()
	defer st.mu.Unlock()
	c := counts{Seen: st.seen, Created: st.created, Updated: st.updated, Unchanged: st.unchanged, Deleted: st.delete,
		Skipped: orEmpty(st.skipped)}
	if st.rewrite != nil {
		rw := *st.rewrite
		c.Rewrite = &rw
	}
	return c
}

func (c *counts) add(o counts) {
	c.Seen += o.Seen
	c.Created += o.Created
	c.Updated += o.Updated
	c.Unchanged += o.Unchanged
	c.Deleted += o.Deleted
	c.Skipped = append(c.Skipped, o.Skipped...)
	if o.Rewrite != nil {
		if c.Rewrite == nil {
			c.Rewrite = &rewriteCounts{}
		}
		c.Rewrite.add(o.Rewrite)
	}
}

// Run works a whole run in this process: it splits the run and works each
// part in turn. The commands and the tests use it. The job queue runs the
// parts on any worker process.
func (s *Service) Run(ctx context.Context, runID int64) error {
	if err := s.Split(ctx, runID); err != nil {
		return err
	}
	parts, err := New(s.Pool).RunParts(ctx, runID)
	if err != nil {
		return err
	}
	for _, p := range parts {
		if p.Status != "queued" {
			continue
		}
		if err := s.RunPart(ctx, runID, p.Part); err != nil {
			return err
		}
	}
	return nil
}

// Split lists the parts of a run, writes one row and one part job per part
// and ends. A run without parts ends at once.
func (s *Service) Split(ctx context.Context, runID int64) error {
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
	// A job that runs again after the parts were written has nothing to do.
	if has, err := q.RunHasParts(ctx, runID); err != nil || has {
		return err
	}
	conn, err := q.GetConnection(ctx, run.ConnectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := q.StartRun(ctx, StartRunParams{ID: runID, Now: ptr(s.now())}); err != nil {
		return err
	}
	if conn.Status == "paused" {
		return s.endRun(ctx, runID, errPaused)
	}
	if run.Kind == "reconcile" && conn.RewriteFrom != "" {
		return s.endRun(ctx, runID, errSwitching)
	}
	var parts []connectors.Part
	src, err := s.sourceOf(ctx, conn)
	if err != nil {
		// A shutdown here also requeues the run below.
		if !stopped(ctx) {
			return err
		}
	} else if k, oerr := s.open(ctx, src); oerr != nil {
		err = oerr
	} else {
		parts, err = k.Parts(ctx)
	}
	if stopped(ctx) {
		// The process stops. The run goes back to the queue and starts
		// again after the restart.
		if err := New(s.Pool).RequeueRun(context.WithoutCancel(ctx), runID); err != nil {
			return err
		}
		return river.JobSnooze(time.Second)
	}
	if errors.Is(context.Cause(ctx), river.ErrJobCancelledRemotely) {
		if c, cerr := New(s.Pool).GetConnection(context.WithoutCancel(ctx), conn.ID); cerr == nil && c.Status == "paused" {
			err = errPausedByAdmin
		}
	}
	if err != nil {
		return s.endRun(ctx, runID, err)
	}
	keys := make([]string, 0, len(parts))
	jobs := make([]river.InsertManyParams, 0, len(parts))
	for _, p := range parts {
		if slices.Contains(keys, p.Key) {
			continue
		}
		keys = append(keys, p.Key)
		jobs = append(jobs, river.InsertManyParams{Args: PartArgs{RunID: runID, Part: p.Key}})
	}
	var done *ended
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		done = nil
		// The first sync after a switch to Cloud gives the entity keys
		// the Cloud host before any part stores a Cloud item.
		if run.Kind == "sync" && conn.RewriteFrom != "" {
			rw := Rewrite{From: conn.RewriteFrom, To: hostOf(conn.BaseUrl)}
			n, err := s.rewriteHosts(ctx, tx, conn, rw.From, rw.To)
			if err != nil {
				return err
			}
			rw.Entities = n
			b, _ := json.Marshal(rw)
			if err := New(tx).SetRunRewrite(ctx, SetRunRewriteParams{ID: runID, Rewrite: b}); err != nil {
				return err
			}
		}
		if err := New(tx).InsertParts(ctx, InsertPartsParams{RunID: runID, Parts: keys}); err != nil {
			return err
		}
		if len(jobs) == 0 {
			var err error
			done, err = s.settle(ctx, tx, runID, nil)
			return err
		}
		_, err := s.River.InsertManyTx(ctx, tx, jobs)
		return err
	})
	if err != nil {
		return err
	}
	s.report(done)
	return nil
}

// endRun ends a run whose parts have all ended, or that failed before it
// had parts.
func (s *Service) endRun(ctx context.Context, runID int64, failed error) error {
	var done *ended
	err := s.inTx(context.WithoutCancel(ctx), func(tx pgx.Tx) error {
		var err error
		done, err = s.settle(context.WithoutCancel(ctx), tx, runID, failed)
		return err
	})
	if err != nil {
		return err
	}
	s.report(done)
	return nil
}

// partWait is the delay of a part job that the parallel parts of its
// connection hold back.
const partWait = 5 * time.Second

// errPartDone means that the part ended before its job started it, such as
// a part that a pause failed.
var errPartDone = errors.New("sync: the part has ended")

// RunPart works one part of a run to its end. The last part of the run
// ends the run. A 401 or 403 pauses the connection in the same
// transaction.
func (s *Service) RunPart(ctx context.Context, runID int64, key string) error {
	q := New(s.Pool)
	p, err := q.GetPart(ctx, GetPartParams{RunID: runID, Part: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.Status == "succeeded" || p.Status == "failed" {
		return nil
	}
	run, err := q.GetRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	conn, err := q.GetConnection(ctx, run.ConnectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	started, err := s.startPart(ctx, conn, runID, key)
	if errors.Is(err, errPartDone) {
		return nil
	}
	if err != nil {
		return err
	}
	if !started {
		return river.JobSnooze(partWait)
	}
	st := &stats{}
	part := connectors.Part{Key: key}
	cursor := ""
	var gone []int64
	if conn.Status == "paused" {
		st.fail(errPaused)
	} else if src, err := s.sourceOf(ctx, conn); err != nil {
		// A shutdown here also requeues the part below.
		if !stopped(ctx) {
			return err
		}
	} else if k, err := s.open(ctx, src); err != nil {
		st.fail(err)
	} else if run.Kind == "reconcile" {
		gone = s.reconcile(ctx, conn, k, part, st)
	} else {
		cursor = s.sync(ctx, conn, k, part, st, rewriteOf(run.Rewrite))
	}
	if stopped(ctx) {
		// The process stops. The part goes back to the queue and starts
		// again after the restart. The items it stored stay, and the
		// next attempt skips them.
		bg := context.WithoutCancel(ctx)
		err := s.inTx(bg, func(tx pgx.Tx) error {
			q := New(tx)
			if err := q.RequeuePart(bg, RequeuePartParams{RunID: runID, Part: key}); err != nil {
				return err
			}
			return q.RequeueIdleRun(bg, runID)
		})
		if err != nil {
			return err
		}
		return river.JobSnooze(time.Second)
	}
	if errors.Is(context.Cause(ctx), river.ErrJobCancelledRemotely) {
		if c, err := New(s.Pool).GetConnection(context.WithoutCancel(ctx), conn.ID); err == nil && c.Status == "paused" {
			st.replace(errPausedByAdmin)
		}
	}
	return s.finishPart(ctx, conn, run, key, cursor, gone, st)
}

// startPart marks a part as running when fewer than parallel_parts parts
// of the connection run, across all processes. It reports false when the
// cap holds the part back. River has no concurrency limit per key, so an
// advisory lock on the connection serializes the count and the start.
func (s *Service) startPart(ctx context.Context, conn Connection, runID int64, key string) (bool, error) {
	host, _ := os.Hostname()
	started := false
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		q := New(tx)
		if err := q.LockConnectionParts(ctx, conn.ID); err != nil {
			return err
		}
		n, err := q.RunningParts(ctx, RunningPartsParams{ConnectionID: conn.ID, RunID: runID, Part: key})
		if err != nil {
			return err
		}
		if n >= int64(max(1, conn.ParallelParts)) {
			started = false
			return nil
		}
		rows, err := q.StartPart(ctx, StartPartParams{Host: host, Now: ptr(s.now()), RunID: runID, Part: key})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errPartDone
		}
		// A run that a shutdown put back in the queue runs again.
		if _, err := q.StartRun(ctx, StartRunParams{ID: runID, Now: ptr(s.now())}); err != nil {
			return err
		}
		started = true
		return nil
	})
	return started, err
}

// finishPart writes the result of a part. The last part of the run ends
// the run in the same transaction.
func (s *Service) finishPart(ctx context.Context, conn Connection, run SyncRun, key, cursor string, gone []int64, st *stats) error {
	ctx = context.WithoutCancel(ctx)
	failed := st.err()
	status, msg := "succeeded", ""
	if failed != nil {
		status, msg = "failed", sourceError(failed)
	}
	gone = orEmpty(gone)
	c := st.counts()
	b, _ := json.Marshal(c)
	var done *ended
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		done = nil
		q := New(tx)
		// The lock on the run orders the ends of its parts: one of them
		// sees that it is the last.
		if _, err := q.LockRun(ctx, run.ID); err != nil {
			return err
		}
		now := s.now()
		if errors.Is(failed, connectors.ErrUnauthorized) {
			if err := pause(ctx, tx, conn.ID, msg); err != nil {
				return err
			}
		}
		if failed == nil && run.Kind == "sync" {
			if err := q.SetPartCursor(ctx, SetPartCursorParams{Now: &now, ConnectionID: conn.ID, Part: key, Cursor: cursor}); err != nil {
				return err
			}
		}
		if err := q.FinishPart(ctx, FinishPartParams{Status: status, Cursor: cursor, Stats: b, Error: msg, Gone: gone, Now: &now,
			RunID: run.ID, Part: key}); err != nil {
			return err
		}
		var err error
		done, err = s.settle(ctx, tx, run.ID, nil)
		return err
	})
	if err != nil {
		return err
	}
	s.Log.Info("sync part finished", "connection", conn.ID, "run", run.ID, "part", key, "status", status,
		"created", c.Created, "updated", c.Updated, "unchanged", c.Unchanged, "deleted", c.Deleted, "skipped", len(c.Skipped), "error", msg)
	s.report(done)
	return nil
}

// pause pauses an active connection after the source refused its token.
// The parts that also get the refusal find it paused.
func pause(ctx context.Context, tx pgx.Tx, connectionID int64, reason string) error {
	n, err := New(tx).PauseActive(ctx, PauseActiveParams{ID: connectionID, Reason: reason})
	if err != nil || n == 0 {
		return err
	}
	return audit.Write(ctx, tx, audit.Entry{Action: "connections.pause", Target: fmt.Sprintf("connection:%d", connectionID),
		Details: map[string]any{"reason": reason, "automatic": true}})
}

// ended is a run that settle ended, for the metrics and the log.
type ended struct {
	run    SyncRun
	status string
	error  string
	counts counts
}

// settle ends a run whose parts have all finished, in tx: it locks the run,
// sums the stats of the parts and sets the status. failed is the error of a
// run that failed before it had parts. A run that succeeded saves the time
// of the sync, and a reconciliation tombstones the items that its parts
// found gone and the items of parts that left the scope. It returns nil
// when the run goes on or already ended.
func (s *Service) settle(ctx context.Context, tx pgx.Tx, runID int64, failed error) (*ended, error) {
	q := New(tx)
	run, err := q.LockRun(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if run.Status != "queued" && run.Status != "running" {
		return nil, nil
	}
	if n, err := q.UnfinishedParts(ctx, runID); err != nil || n > 0 {
		return nil, err
	}
	parts, err := q.RunParts(ctx, runID)
	if err != nil {
		return nil, err
	}
	conn, err := q.GetConnection(ctx, run.ConnectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	sum := counts{Skipped: []Skip{}}
	msg := ""
	if failed != nil {
		msg = sourceError(failed)
		if errors.Is(failed, connectors.ErrUnauthorized) {
			if err := pause(ctx, tx, conn.ID, msg); err != nil {
				return nil, err
			}
		}
	}
	keys := make([]string, 0, len(parts))
	for _, p := range parts {
		keys = append(keys, p.Part)
		var c counts
		_ = json.Unmarshal(p.Stats, &c)
		sum.add(c)
		if p.Status == "failed" && msg == "" {
			msg = p.Error
		}
	}
	status := "succeeded"
	if failed != nil || msg != "" {
		status = "failed"
	}
	if rw := rewriteOf(run.Rewrite); rw != nil && run.Kind == "sync" {
		if sum.Rewrite != nil {
			rw.Items, rw.People, rw.Unmatched = sum.Rewrite.Items, sum.Rewrite.People, sum.Rewrite.Unmatched
		}
		b, _ := json.Marshal(rw)
		if err := q.SetRunRewrite(ctx, SetRunRewriteParams{ID: run.ID, Rewrite: b}); err != nil {
			return nil, err
		}
		if status == "succeeded" {
			if err := s.finishSwitch(ctx, tx, conn, rw); err != nil {
				return nil, err
			}
		}
	}
	if status == "succeeded" {
		switch run.Kind {
		case "sync":
			if err := q.FinishSync(ctx, FinishSyncParams{ID: conn.ID, Now: &now}); err != nil {
				return nil, err
			}
		case "reconcile":
			n, err := s.tombstoneGone(ctx, tx, conn.ID, parts, keys)
			if err != nil {
				return nil, err
			}
			sum.Deleted += n
			if err := q.FinishReconcile(ctx, FinishReconcileParams{ID: conn.ID, Now: &now}); err != nil {
				return nil, err
			}
		}
	}
	skipped, _ := json.Marshal(sum.Skipped)
	if err := q.FinishRun(ctx, FinishRunParams{ID: run.ID, Status: status, Now: &now, Seen: sum.Seen, Created: sum.Created,
		Updated: sum.Updated, Unchanged: sum.Unchanged, Deleted: sum.Deleted, Skipped: skipped, Error: msg}); err != nil {
		return nil, err
	}
	if status == "succeeded" && s.AfterRun != nil && len(conn.CodeGlobs) > 0 {
		if err := s.AfterRun(ctx, tx, conn.ID); err != nil {
			return nil, err
		}
	}
	return &ended{run: run, status: status, error: msg, counts: sum}, nil
}

// finishSwitch ends the switch of a connection to Cloud after its first
// sync, in tx: the connection needs no more rewrite, one audit entry holds
// the hosts and counts, and a reconciliation tombstones the items that
// matched nothing.
func (s *Service) finishSwitch(ctx context.Context, tx pgx.Tx, conn Connection, rw *Rewrite) error {
	if err := New(tx).ClearRewrite(ctx, ClearRewriteParams{ID: conn.ID, RewriteFrom: rw.From}); err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, audit.Entry{Action: "connections.switch", Target: fmt.Sprintf("connection:%d", conn.ID),
		Details: map[string]any{"from": rw.From, "to": rw.To, "items": rw.Items, "entities": rw.Entities, "people": rw.People,
			"unmatched": rw.Unmatched}}); err != nil {
		return err
	}
	if _, err := s.queueTx(ctx, tx, conn.ID, "reconcile", "manual"); err != nil && !errors.Is(err, ErrRunActive) {
		return err
	}
	return nil
}

// tombstoneGone tombstones, after all parts of a reconciliation listed
// their items, the items that a part found gone and that no other part
// stored since, and the items of parts that are no longer in the scope. An
// item that moved to another part, such as a page that moved to another
// space, was stored by that part and stays.
func (s *Service) tombstoneGone(ctx context.Context, tx pgx.Tx, connectionID int64, parts []SyncPart, keys []string) (int32, error) {
	q := New(tx)
	var ids []int64
	for _, p := range parts {
		if len(p.Gone) == 0 {
			continue
		}
		still, err := q.StillInPart(ctx, StillInPartParams{Ids: p.Gone, Part: p.Part})
		if err != nil {
			return 0, err
		}
		ids = append(ids, still...)
	}
	outside, err := q.ItemsOutsideParts(ctx, ItemsOutsidePartsParams{ConnectionID: connectionID, Parts: keys})
	if err != nil {
		return 0, err
	}
	ids = append(ids, outside...)
	now := s.now()
	n := 0
	done := map[int64]bool{}
	for _, id := range ids {
		d, err := tombstoneTx(ctx, tx, id, now, done)
		if err != nil {
			return 0, err
		}
		n += d
	}
	return int32(n), nil //nolint:gosec // A connection holds far fewer items than the int32 range.
}

// tombstoneTx tombstones an item and the attachment items of a page,
// except the items in done, and adds them to done. It returns the number of
// items it deleted.
func tombstoneTx(ctx context.Context, tx pgx.Tx, itemID int64, now time.Time, done map[int64]bool) (int, error) {
	atts, err := New(tx).AttachmentItems(ctx, itemID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range append(atts, itemID) {
		if done[id] {
			continue
		}
		done[id] = true
		if err := content.Tombstone(ctx, tx, id, now); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// settleAll ends the runs whose last parts a sweep or a pause failed.
func (s *Service) settleAll(ctx context.Context, runIDs []int64) error {
	slices.Sort(runIDs)
	for _, id := range slices.Compact(runIDs) {
		if err := s.endRun(ctx, id, nil); err != nil {
			return err
		}
	}
	return nil
}

// report counts an ended run in the metrics and the log.
func (s *Service) report(e *ended) {
	if e == nil {
		return
	}
	c := e.counts
	if s.Metrics != nil {
		s.Metrics.Runs.WithLabelValues(e.run.Kind, e.status).Inc()
		s.Metrics.Items.WithLabelValues("created").Add(float64(c.Created))
		s.Metrics.Items.WithLabelValues("updated").Add(float64(c.Updated))
		s.Metrics.Items.WithLabelValues("unchanged").Add(float64(c.Unchanged))
		s.Metrics.Items.WithLabelValues("deleted").Add(float64(c.Deleted))
		s.Metrics.Items.WithLabelValues("skipped").Add(float64(len(c.Skipped)))
	}
	s.Log.Info("sync run finished", "connection", e.run.ConnectionID, "run", e.run.ID, "kind", e.run.Kind, "status", e.status,
		"created", c.Created, "updated", c.Updated, "unchanged", c.Unchanged, "deleted", c.Deleted, "skipped", len(c.Skipped), "error", e.error)
}

// inTx runs fn in a transaction. A deadlock or a serialization failure
// rolls the transaction back, and fn runs again.
func (s *Service) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	var err error
	for attempt := 1; ; attempt++ {
		err = pgx.BeginFunc(ctx, s.Pool, fn)
		if !retryable(err) || attempt == storeAttempts {
			return err
		}
	}
}

// stopped reports a run that the shutdown of the process cancelled. A
// timeout and a cancel in the job queue end the run as failed.
func stopped(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.Canceled) && !errors.Is(context.Cause(ctx), river.ErrJobCancelledRemotely)
}

// listBatch is the number of listed refs that sync stores before it saves
// the cursor and lets the connector read on. It is the page size of a
// Confluence listing with bodies.
const listBatch = 50

// sync stores the items of a part changed since the cursor of the part
// minus the overlap. It streams the listing: each batch of refs is stored
// before the connector reads on, and the cursor then moves to the newest
// update time of the batch, so that a part that stops continues from there.
// It returns the new cursor: the newest update time it handled.
//
// The first sync after a switch to Cloud, with rw set, lists the whole
// part and stores every item again: before each batch it fetches the
// items, matches them with the stored items and people in one transaction,
// and then stores them with their Cloud links and keys.
func (s *Service) sync(ctx context.Context, conn Connection, k connectors.Connector, part connectors.Part, st *stats, rw *Rewrite) string {
	cursor, err := New(s.Pool).PartCursor(ctx, PartCursorParams{ConnectionID: conn.ID, Part: part.Key})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		st.fail(err)
		return ""
	}
	var since time.Time
	newest := time.Time{}
	if cursor != "" && rw == nil {
		if t, err := time.Parse(time.RFC3339Nano, cursor); err == nil {
			since = t.Add(-CursorOverlap)
			newest = t
		}
	}
	var cloudIDs []string
	if rw != nil {
		st.rewrite = &rewriteCounts{}
		cursor = ""
		if conn.Kind == "confluence" {
			// A stored page matches a Cloud page only when its ID is not
			// in Cloud.
			cloudIDs = []string{}
			if err := k.ListIDs(ctx, part, func(id string) error { cloudIDs = append(cloudIDs, id); return nil }); err != nil {
				st.fail(err)
				return ""
			}
		}
	}
	stored := &storedItems{}
	var batch []connectors.Ref
	flush := func() error {
		defer func() { batch = batch[:0] }()
		if err := s.stored(ctx, conn.ID, part.Key, batch, stored); err != nil {
			return err
		}
		var work []connectors.Ref
		for _, r := range batch {
			if rw == nil && stored.has(r) {
				st.add(content.Unchanged)
				continue
			}
			work = append(work, r)
		}
		if rw != nil {
			if err := s.prefetch(ctx, conn, k, work); err != nil {
				return err
			}
			counts := &rewriteCounts{}
			if err := s.rewriteBatch(ctx, conn, rw, part.Key, work, cloudIDs, counts); err != nil {
				return err
			}
			st.mu.Lock()
			st.rewrite.add(counts)
			st.mu.Unlock()
		}
		s.fetchAll(ctx, conn, k, part, work, st)
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
		return New(s.Pool).SetPartCursor(ctx, SetPartCursorParams{Now: ptr(s.now()), ConnectionID: conn.ID, Part: part.Key, Cursor: cursor})
	}
	if rc, ok := k.(connectors.Recaller); ok {
		known, err := s.known(ctx, conn.ID, part.Key)
		if err != nil {
			st.fail(err)
			return ""
		}
		rc.Recall(part, known)
	}
	err = k.Changed(ctx, part, since, func(r connectors.Ref) error {
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

// prefetch fetches the items of refs that do not carry them, with the
// concurrency of the connection, so that the key rewrite sees their people.
// A skipped item keeps its reason; an item that is gone stays without an
// item, and its store tombstones it.
func (s *Service) prefetch(ctx context.Context, conn Connection, k connectors.Connector, refs []connectors.Ref) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, max(1, int(conn.RateConcurrency)))
	var wg gosync.WaitGroup
	var mu gosync.Mutex
	var first error
	for i := range refs {
		if refs[i].Item != nil || refs[i].Skip != "" {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(r *connectors.Ref) {
			defer wg.Done()
			defer func() { <-sem }()
			it, err := k.Fetch(ctx, r.ID)
			var skip *connectors.SkipError
			switch {
			case errors.As(err, &skip):
				r.Skip = skip.Reason
			case errors.Is(err, connectors.ErrNotFound):
			case err != nil:
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
				cancel()
			default:
				r.Item = it
			}
		}(&refs[i])
	}
	wg.Wait()
	if first != nil {
		return first
	}
	return ctx.Err()
}

// known returns the stored items of a part with their revisions.
func (s *Service) known(ctx context.Context, connectionID int64, part string) ([]connectors.Known, error) {
	q := New(s.Pool)
	ids, err := q.PartItemIDs(ctx, PartItemIDsParams{ConnectionID: connectionID, Part: part})
	if err != nil {
		return nil, err
	}
	revs, err := q.ItemRevisions(ctx, ItemRevisionsParams{ConnectionID: connectionID, Part: part})
	if err != nil {
		return nil, err
	}
	rev := make(map[string]string, len(revs))
	for _, r := range revs {
		rev[r.ExternalID] = r.Revision
	}
	out := make([]connectors.Known, 0, len(ids))
	for _, it := range ids {
		out = append(out, connectors.Known{ID: it.ExternalID, Revision: rev[it.ExternalID]})
	}
	return out, nil
}

// storedItems are the revisions and source update times of the stored
// items of a part. Each map is read once, when a ref first needs it.
type storedItems struct {
	revisions map[string]string
	updated   map[string]time.Time
}

// has reports whether the stored item is the listed one: the same revision,
// or, when the item has no stored revision, the same or a later source
// update time. Items that an older Assay stored have no revision. Sync
// skips the fetch, so that a run after an interrupted one fetches only the
// items it did not store. An item stored in another part is not in the
// maps: sync stores it again, and it moves to this part.
func (st *storedItems) has(r connectors.Ref) bool {
	if rev, ok := st.revisions[r.ID]; ok && r.Version != "" {
		return rev == r.Version
	}
	at, ok := st.updated[r.ID]
	return ok && !r.Updated.IsZero() && !r.Updated.After(at)
}

// stored reads the stored revisions and source update times of a part
// into out, each the first time a ref has one to compare. Items that this
// run stores later are not in the maps; the listing names each item once.
func (s *Service) stored(ctx context.Context, connectionID int64, part string, refs []connectors.Ref, out *storedItems) error {
	q := New(s.Pool)
	if out.revisions == nil && slices.ContainsFunc(refs, func(r connectors.Ref) bool { return r.Version != "" }) {
		rows, err := q.ItemRevisions(ctx, ItemRevisionsParams{ConnectionID: connectionID, Part: part})
		if err != nil {
			return err
		}
		out.revisions = make(map[string]string, len(rows))
		for _, r := range rows {
			out.revisions[r.ExternalID] = r.Revision
		}
	}
	if out.updated == nil && slices.ContainsFunc(refs, func(r connectors.Ref) bool { return !r.Updated.IsZero() }) {
		rows, err := q.ItemUpdates(ctx, ItemUpdatesParams{ConnectionID: connectionID, Part: part})
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

// reconcile lists all IDs of a part and fetches the items that sync
// missed, also the items that another part stored. It returns the stored
// items of the part that the listing no longer has. The last part of the
// run tombstones them, so that an item that moved to another part stays.
func (s *Service) reconcile(ctx context.Context, conn Connection, k connectors.Connector, part connectors.Part, st *stats) []int64 {
	inScope := map[string]bool{}
	if err := k.ListIDs(ctx, part, func(id string) error { inScope[id] = true; return nil }); err != nil {
		st.fail(err)
		return nil
	}
	stored, err := New(s.Pool).PartItemIDs(ctx, PartItemIDsParams{ConnectionID: conn.ID, Part: part.Key})
	if err != nil {
		st.fail(err)
		return nil
	}
	known := map[string]bool{}
	var gone []int64
	for _, it := range stored {
		known[it.ExternalID] = true
		if !inScope[it.ExternalID] {
			gone = append(gone, it.ID)
		}
	}
	var missing []connectors.Ref
	for id := range inScope {
		if !known[id] {
			missing = append(missing, connectors.Ref{ID: id})
		}
	}
	s.fetchAll(ctx, conn, k, part, missing, st)
	return gone
}

// tombstone tombstones an item and the attachment items of a page. It
// returns the number of items it deleted.
func (s *Service) tombstone(ctx context.Context, itemID int64) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		n, err = tombstoneTx(ctx, tx, itemID, s.now(), map[int64]bool{})
		return err
	})
	return n, err
}

// fetchAll stores items with the concurrency of the connection. It fetches
// each ref that does not carry its item. A 401 or 403 stops the part.
func (s *Service) fetchAll(ctx context.Context, conn Connection, k connectors.Connector, part connectors.Part, refs []connectors.Ref, st *stats) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan connectors.Ref)
	var wg gosync.WaitGroup
	for range max(1, int(conn.RateConcurrency)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range work {
				if err := s.fetchOne(ctx, conn, k, part, r, st); err != nil {
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

func (s *Service) fetchOne(ctx context.Context, conn Connection, k connectors.Connector, part connectors.Part, r connectors.Ref, st *stats) error {
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
	it.Part = part.Key
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

// storeAttempts bounds the attempts of one transaction.
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
	n, err := s.tombstone(ctx, existing)
	if err != nil {
		return err
	}
	for range n {
		st.deleted()
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
