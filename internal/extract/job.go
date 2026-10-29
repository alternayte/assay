package extract

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/models"
)

// Triggers of extraction (SDD section 11).
const (
	TriggerAnswer = "answer"
	TriggerPage   = "page"
	TriggerAdmin  = "admin"
)

// Args is one extraction job: passages and the visible set of its trigger.
// The job reads only passages in that set.
type Args struct {
	Visible []int64  `json:"visible"`
	Items   []int64  `json:"items"`
	Hashes  []string `json:"hashes" doc:"Passage hashes in hex, one per item."`
	Trigger string   `json:"trigger"`
}

// Kind is the River job kind.
func (Args) Kind() string { return "extract" }

// InsertOpts retries a failed call a few times.
func (Args) InsertOpts() river.InsertOpts { return river.InsertOpts{MaxAttempts: 5} }

// Service extracts claims.
type Service struct {
	Pool    *pgxpool.Pool
	Models  *models.Gateway
	Log     *slog.Logger
	River   *river.Client[pgx.Tx]
	Metrics *Metrics
}

// Metrics count the rejected claims by reason.
type Metrics struct {
	Rejections *prometheus.CounterVec
	Accepted   prometheus.Counter
}

// NewMetrics returns the extraction metrics.
func NewMetrics() *Metrics {
	return &Metrics{
		Rejections: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "assay_extraction_rejections_total",
			Help: "Extracted claims that failed validation, by reason."}, []string{"reason"}),
		Accepted: prometheus.NewCounter(prometheus.CounterOpts{Name: "assay_extraction_accepted_total",
			Help: "Extracted claims that passed validation."}),
	}
}

// Collectors returns the metrics to register.
func (m *Metrics) Collectors() []prometheus.Collector {
	return []prometheus.Collector{m.Rejections, m.Accepted}
}

// Worker works extraction jobs.
type Worker struct {
	river.WorkerDefaults[Args]
	S *Service
}

// Work extracts the passages of the job. When the extract budget is used
// up, the job waits an hour and the models status shows the banner.
func (w *Worker) Work(ctx context.Context, job *river.Job[Args]) error {
	err := w.S.Run(ctx, job.Args)
	if errors.Is(err, models.ErrBudgetExceeded) {
		return river.JobSnooze(time.Hour)
	}
	return err
}

// Run extracts the passages of args that have no cached result. Without the
// extract function it does nothing.
func (s *Service) Run(ctx context.Context, a Args) error {
	if len(a.Items) != len(a.Hashes) || len(a.Visible) == 0 {
		return nil
	}
	hashes := make([][]byte, len(a.Hashes))
	for i, h := range a.Hashes {
		b, err := hex.DecodeString(h)
		if err != nil {
			return fmt.Errorf("extract: bad passage hash %q", h)
		}
		hashes[i] = b
	}
	q := New(s.Pool)
	version := Version()
	ps, err := q.Passages(ctx, PassagesParams{Items: a.Items, Hashes: hashes, Visible: a.Visible})
	if err != nil {
		return err
	}
	cached, err := q.Cached(ctx, CachedParams{Items: a.Items, Hashes: hashes, Version: version})
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, c := range cached {
		done[fmt.Sprintf("%d:%x", c.ItemID, c.PassageHash)] = true
	}
	for i := range a.Items {
		want := fmt.Sprintf("%d:%x", a.Items[i], hashes[i])
		if done[want] {
			continue
		}
		for _, p := range ps {
			if fmt.Sprintf("%d:%x", p.ItemID, p.Hash) != want {
				continue
			}
			done[want] = true
			if err := s.passage(ctx, a.Visible, version, p); err != nil {
				if errors.Is(err, models.ErrDisabled) {
					return nil
				}
				return err
			}
			break
		}
	}
	return nil
}

func (s *Service) passage(ctx context.Context, vis []int64, version string, p PassagesRow) error {
	q := New(s.Pool)
	known, err := q.KnownEntities(ctx, KnownEntitiesParams{Visible: vis, Norm: entities.NormalizeAlias(p.Norm)})
	if err != nil {
		return err
	}
	in := Passage{Title: p.Title, HeadingPath: p.HeadingPath, Text: p.Text, Norm: p.Norm}
	for _, k := range known {
		in.Known = append(in.Known, Known{Key: k.Key, Type: k.Type, Name: k.Name})
	}
	res, err := s.Models.Chat(ctx, models.Extract, Request(in))
	if err != nil {
		return err
	}
	valid, rejected := Parse(res.Content, p.Norm)
	var out []content.Extracted
	for _, v := range valid {
		c, reason, detail := s.resolve(ctx, vis, v)
		if reason != "" {
			rejected = append(rejected, Rejection{Index: v.Index, Reason: reason, Detail: detail})
			continue
		}
		out = append(out, c)
	}
	if s.Metrics != nil {
		s.Metrics.Accepted.Add(float64(len(out)))
		for _, r := range rejected {
			s.Metrics.Rejections.WithLabelValues(r.Reason).Inc()
		}
	}
	reasons, _ := json.Marshal(orEmpty(rejected))
	raw := json.RawMessage(res.Content)
	if !json.Valid(raw) {
		raw, _ = json.Marshal(map[string]string{"text": res.Content})
	}
	at := time.Now()
	if p.SourceUpdatedAt != nil {
		at = *p.SourceUpdatedAt
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := content.AddExtracted(ctx, tx, p.ConnectionID, p.ItemID, p.Hash, at, out, s.Log); err != nil {
			return err
		}
		return New(tx).InsertExtraction(ctx, InsertExtractionParams{ItemID: p.ItemID, PassageHash: p.Hash, ExtractorVersion: version,
			Accepted: int32(len(out)), Rejected: int32(len(rejected)), Reasons: reasons, Output: raw}) //nolint:gosec // At most 10 claims.
	})
}

// resolve turns the mentions of a claim into entities. A key resolves
// directly. A name resolves when exactly one visible entity of an allowed
// type has it as an alias. Description mentions and other names go to
// resolution (REQ-EXT-005).
func (s *Service) resolve(ctx context.Context, vis []int64, v Valid) (content.Extracted, string, string) {
	p := claims.Registry[v.Predicate]
	c := content.Extracted{Predicate: v.Predicate, Stance: v.Stance, Quote: v.Quote}
	if v.Scope != nil {
		c.Scope = entities.NormalizeAlias(v.Scope.Env)
	}
	subj, subjType, reason, detail := s.mention(ctx, vis, v.Subject, p.Subjects)
	if reason != "" {
		return c, reason, detail
	}
	c.Subject = subj
	objType := claims.ObjText
	if v.Object.Kind == "text" {
		c.Text = v.Object.Value
		if slices.Contains(p.Objects, claims.ObjDate) {
			objType = claims.ObjDate
			if _, err := time.Parse("2006-01-02", v.Object.Value); err != nil {
				return c, ReasonBadPredicate, v.Predicate + " needs a date as YYYY-MM-DD"
			}
		}
	} else {
		obj, t, reason, detail := s.mention(ctx, vis, v.Object, p.Objects)
		if reason != "" {
			return c, reason, detail
		}
		c.Object, objType = &obj, t
	}
	if err := claims.CheckExtracted(v.Predicate, subjType, objType); err != nil {
		return c, ReasonBadPredicate, err.Error()
	}
	return c, "", ""
}

func (s *Service) mention(ctx context.Context, vis []int64, m Mention, allowed []string) (int64, string, string, string) {
	q := New(s.Pool)
	switch m.Kind {
	case "key":
		e, err := q.EntityByKey(ctx, EntityByKeyParams{Key: m.Value, Visible: vis})
		if err != nil {
			return 0, "", ReasonUnresolved, "unknown key " + m.Value
		}
		return e.ID, e.Type, "", ""
	case "name":
		rows, err := q.EntitiesByAlias(ctx, EntitiesByAliasParams{Norm: entities.NormalizeAlias(m.Value), Visible: vis})
		if err != nil {
			return 0, "", ReasonUnresolved, err.Error()
		}
		var match []EntitiesByAliasRow
		for _, r := range rows {
			if slices.Contains(allowed, r.Type) || slices.Contains(allowed, claims.ObjAny) {
				match = append(match, r)
			}
		}
		if len(match) != 1 {
			return 0, "", ReasonUnresolved, fmt.Sprintf("%d entities match the name %q", len(match), m.Value)
		}
		return match[0].ID, match[0].Type, "", ""
	case "description":
		return 0, "", ReasonDescription, m.Value
	}
	return 0, "", ReasonBadPredicate, "a text where an entity is needed"
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
