package extract

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/alternayte/assay/internal/models"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// Queue queues one job for the passages that have no cached result. It
// returns false when all are cached.
func (s *Service) Queue(ctx context.Context, vis []int64, trigger string, items []int64, hashes []string) (bool, error) {
	raw := make([][]byte, len(hashes))
	for i, h := range hashes {
		b, err := hex.DecodeString(h)
		if err != nil {
			return false, fmt.Errorf("extract: bad passage hash %q", h)
		}
		raw[i] = b
	}
	cached, err := New(s.Pool).Cached(ctx, CachedParams{Items: items, Hashes: raw, Version: Version()})
	if err != nil {
		return false, err
	}
	done := map[string]bool{}
	for _, c := range cached {
		done[fmt.Sprintf("%d:%x", c.ItemID, c.PassageHash)] = true
	}
	a := Args{Visible: vis, Trigger: trigger}
	for i := range items {
		k := fmt.Sprintf("%d:%x", items[i], raw[i])
		if done[k] {
			continue
		}
		done[k] = true
		a.Items = append(a.Items, items[i])
		a.Hashes = append(a.Hashes, hashes[i])
	}
	if len(a.Items) == 0 {
		return false, nil
	}
	// The same passages queued twice, such as by two opens of a page, run once.
	_, err = s.River.Insert(ctx, a, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByArgs: true}})
	return err == nil, err
}

// Estimate is the work of an admin extraction of a connection.
type Estimate struct {
	Passages  int64   `json:"passages" doc:"Passages with no cached result."`
	TokensIn  int64   `json:"tokensIn"`
	TokensOut int64   `json:"tokensOut"`
	CostUSD   float64 `json:"costUsd" doc:"The estimate with the prices of the extract function."`
	On        bool    `json:"on" doc:"The extract function is on."`
}

// outPerPassage is the estimate of output tokens of one passage.
const outPerPassage = 300

// EstimateConnection counts the passages of a connection that have no
// cached result and estimates the cost. It sends no model request.
func (s *Service) EstimateConnection(ctx context.Context, connectionID int64) (Estimate, error) {
	p, err := New(s.Pool).Pending(ctx, PendingParams{Visible: []int64{connectionID}, Version: Version()})
	if err != nil {
		return Estimate{}, err
	}
	in, out, on, err := s.Models.Prices(ctx, models.Extract)
	if err != nil {
		return Estimate{}, err
	}
	e := Estimate{Passages: p.Passages, On: on}
	e.TokensIn = p.Chars/4 + p.Passages*int64(models.EstimateTokens(Prompt)+200)
	e.TokensOut = p.Passages * outPerPassage
	e.CostUSD = models.Cost(int(e.TokensIn), int(e.TokensOut), in, out)
	return e, nil
}

// batch is the number of passages of one admin job.
const batch = 50

// page is the number of passages that one planning step reads.
const page = 5000

// ConnectionArgs plans the admin extraction of a connection. The plan runs
// as a job, so the request that starts it returns at once.
type ConnectionArgs struct {
	Connection int64   `json:"connection" river:"unique"`
	Visible    []int64 `json:"visible"`
}

// Kind is the River job kind.
func (ConnectionArgs) Kind() string { return "extract_connection" }

// InsertOpts allows one unfinished plan per connection.
func (ConnectionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateRunning,
		rivertype.JobStateScheduled}}}
}

// ConnectionWorker works plan jobs.
type ConnectionWorker struct {
	river.WorkerDefaults[ConnectionArgs]
	S *Service
}

// Work queues the extraction jobs of the connection.
func (w *ConnectionWorker) Work(ctx context.Context, job *river.Job[ConnectionArgs]) error {
	_, err := w.S.QueueConnection(ctx, job.Args.Connection, job.Args.Visible)
	return err
}

// StartConnection queues the plan of an admin extraction of a connection.
// vis is the visible set of the admin.
func (s *Service) StartConnection(ctx context.Context, connectionID int64, vis []int64) error {
	_, err := s.River.Insert(ctx, ConnectionArgs{Connection: connectionID, Visible: vis}, nil)
	return err
}

// QueueConnection queues jobs for all passages of a connection with no
// cached result, and returns the number of passages. vis is the visible set
// of the admin; it and the connection resolve the mentions.
func (s *Service) QueueConnection(ctx context.Context, connectionID int64, vis []int64) (int, error) {
	if !slices.Contains(vis, connectionID) {
		vis = append(slices.Clone(vis), connectionID)
	}
	q := New(s.Pool)
	n := 0
	after := int64(0)
	for {
		rows, err := q.PendingBatch(ctx, PendingBatchParams{Visible: []int64{connectionID}, After: after, Version: Version(), Lim: page})
		if err != nil {
			return n, err
		}
		if len(rows) == 0 {
			return n, nil
		}
		var jobs []river.InsertManyParams
		for len(rows) > 0 {
			part := rows[:min(batch, len(rows))]
			rows = rows[len(part):]
			a := Args{Visible: vis, Trigger: TriggerAdmin, Connection: connectionID}
			for _, r := range part {
				a.Items = append(a.Items, r.ItemID)
				a.Hashes = append(a.Hashes, hex.EncodeToString(r.Hash))
				after = r.ID
			}
			jobs = append(jobs, river.InsertManyParams{Args: a})
			n += len(part)
		}
		if _, err := s.River.InsertMany(ctx, jobs); err != nil {
			return n, err
		}
	}
}

// adminJobs lists the unfinished admin extraction jobs of a connection.
func adminJobs(connectionID int64, states ...rivertype.JobState) *river.JobListParams {
	return river.NewJobListParams().Kinds(Args{}.Kind(), ConnectionArgs{}.Kind()).States(states...).
		Where("(args->>'connection')::bigint = @connection", river.NamedArgs{"connection": connectionID})
}

var waiting = []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable,
	rivertype.JobStateScheduled}

// Running reports whether an admin extraction of the connection has
// unfinished jobs.
func (s *Service) Running(ctx context.Context, connectionID int64) (bool, error) {
	res, err := s.River.JobList(ctx, adminJobs(connectionID, append(waiting, rivertype.JobStateRunning)...).First(1))
	if err != nil {
		return false, err
	}
	return len(res.Jobs) > 0, nil
}

// StopConnection stops the admin extraction of a connection: it cancels the
// running jobs and deletes the waiting ones. The results so far stay in the
// cache, so a new start continues with the rest. It returns the number of
// jobs it stopped.
func (s *Service) StopConnection(ctx context.Context, connectionID int64) (int, error) {
	n := 0
	running, err := s.River.JobList(ctx, adminJobs(connectionID, rivertype.JobStateRunning).First(10000))
	if err != nil {
		return n, err
	}
	for _, j := range running.Jobs {
		if _, err := s.River.JobCancel(ctx, j.ID); err != nil {
			return n, err
		}
		n++
	}
	for {
		res, err := s.River.JobList(ctx, adminJobs(connectionID, waiting...).First(1000))
		if err != nil {
			return n, err
		}
		if len(res.Jobs) == 0 {
			return n, nil
		}
		ids := make([]int64, len(res.Jobs))
		for i, j := range res.Jobs {
			ids[i] = j.ID
		}
		del, err := s.River.JobDeleteMany(ctx, river.NewJobDeleteManyParams().IDs(ids...).States(waiting...))
		if err != nil {
			return n, err
		}
		n += len(del.Jobs)
	}
}
