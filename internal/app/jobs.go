package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/settings"
)

// RetentionConfig is the retention setting, in days per record kind.
type RetentionConfig struct {
	QuestionsDays  int `json:"questionsDays" minimum:"1"`
	SyncRunsDays   int `json:"syncRunsDays" minimum:"1"`
	ModelUsageDays int `json:"modelUsageDays" minimum:"1"`
	AuditDays      int `json:"auditDays" minimum:"1"`
}

// Retention is the definition of the retention setting.
var Retention = settings.Define("retention", "Days to keep each kind of record. A daily job deletes older records.",
	RetentionConfig{QuestionsDays: 90, SyncRunsDays: 30, ModelUsageDays: 400, AuditDays: 365},
	func(c RetentionConfig) error {
		if c.QuestionsDays < 1 || c.SyncRunsDays < 1 || c.ModelUsageDays < 1 || c.AuditDays < 1 {
			return errors.New("each value must be 1 day or more")
		}
		return nil
	})

// RetentionArgs is the daily retention job.
type RetentionArgs struct{}

// Kind is the River job kind.
func (RetentionArgs) Kind() string { return "retention" }

// RetentionWorker deletes records older than the retention setting.
type RetentionWorker struct {
	river.WorkerDefaults[RetentionArgs]
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
}

// Work runs one retention pass.
func (w *RetentionWorker) Work(ctx context.Context, _ *river.Job[RetentionArgs]) error {
	cfg, err := settings.Read[RetentionConfig](ctx, w.pool, Retention)
	if err != nil {
		return err
	}
	now := w.now()
	return pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		n, err := audit.New(tx).DeleteBefore(ctx, now.AddDate(0, 0, -cfg.AuditDays))
		if err != nil {
			return err
		}
		w.log.Info("retention applied", "audit_deleted", n)
		return nil
	})
}

// newRiver builds the job client. The worker role works jobs and runs the
// periodic jobs. The web role only inserts jobs.
func (s *Server) newRiver() (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &RetentionWorker{pool: s.pool, log: s.log, now: s.now})
	cfg := &river.Config{
		Workers: workers,
		Logger:  s.log.With("component", "river"),
	}
	if s.cfg.Role != RoleWeb {
		cfg.Queues = map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 10}}
		cfg.PeriodicJobs = []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour), func() (river.JobArgs, *river.InsertOpts) {
				return RetentionArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: 24 * time.Hour}}
			}, &river.PeriodicJobOpts{RunOnStart: true, ID: "retention"}),
		}
	}
	return river.NewClient(riverpgxv5.New(s.pool), cfg)
}
