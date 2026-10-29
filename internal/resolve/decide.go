package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/models"
)

// Decision kinds (SDD section 12).
const (
	KindSameEntity    = "same_entity"
	KindMentionLink   = "mention_link"
	KindDescribes     = "describes"
	KindEquivalent    = "equivalent"
	KindStillSupports = "still_supports"
	KindTermWorthy    = "term_worthy"
)

// Kinds are the decision kinds in display order.
var Kinds = []string{KindSameEntity, KindMentionLink, KindDescribes, KindEquivalent, KindStillSupports, KindTermWorthy}

// Question is one typed decision to make and store.
type Question struct {
	Kind        string
	Refs        []string
	Connections []int64
	Reasons     []string
	Request     models.DecisionRequest
}

// Decided is a stored decision. Band is empty when the decide function is
// off: the card then has no band (REQ-MOD-009).
type Decided struct {
	ID    int64
	P     float64
	Probs map[string]float64
	Band  string
	Auto  bool
}

func (q Question) hash() []byte {
	b, _ := json.Marshal([]any{q.Kind, q.Refs, q.Request.Instructions, q.Request.Yes, q.Request.No, q.Request.Options, q.Request.State})
	h := sha256.Sum256(b)
	return h[:]
}

// decided reports whether the same question was asked before.
func (s *Service) decided(ctx context.Context, q Question) (bool, error) {
	return New(s.Pool).DecidedInput(ctx, DecidedInputParams{Kind: q.Kind, InputHash: q.hash()})
}

// ask runs a question through decide and stores it with its probabilities,
// band, reasons and input connections (REQ-RES-003, SI-18). A High answer
// of a kind with auto-apply on is marked auto, and the caller applies it.
func (s *Service) ask(ctx context.Context, q Question) (*Decided, error) {
	q.Request.Ref = q.Kind
	d, err := s.Models.Decide(ctx, q.Request)
	off := errors.Is(err, models.ErrDisabled)
	if err != nil && !off {
		return nil, err
	}
	out := &Decided{Probs: map[string]float64{}}
	model := ""
	var rawP *float64
	var band *string
	outcome := "open"
	if !off {
		out.Probs, model = d.Probs, d.Model
		out.P = d.P()
		if q.Request.Type == models.Choice {
			for _, v := range d.Probs {
				out.P = max(out.P, v)
			}
		}
		cal, auto, err := s.calibration(ctx, q.Kind)
		if err != nil {
			return nil, err
		}
		out.Band = cal.Band(out.P)
		out.Auto = auto && out.Band == BandHigh
		if out.Auto {
			outcome = "auto"
		}
		rawP, band = &out.P, &out.Band
	}
	probs, _ := json.Marshal(out.Probs)
	reasons, _ := json.Marshal(orEmpty(q.Reasons))
	conns := slices.Clone(q.Connections)
	slices.Sort(conns)
	conns = slices.Compact(conns)
	out.ID, err = New(s.Pool).InsertDecision(ctx, InsertDecisionParams{Kind: q.Kind, SubjectRefs: q.Refs, InputHash: q.hash(), Model: model,
		Probs: probs, RawP: rawP, Band: band, Connections: orEmpty(conns), Outcome: outcome, Reasons: reasons, At: s.now()})
	return out, err
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// calibration returns the calibration of a kind and whether auto-apply is
// on.
func (s *Service) calibration(ctx context.Context, kind string) (Calibration, bool, error) {
	row, err := New(s.Pool).GetCalibration(ctx, kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Calibration{}, false, nil
	}
	if err != nil {
		return Calibration{}, false, err
	}
	var c Calibration
	if err := json.Unmarshal(row.Bins, &c.Bins); err != nil {
		return Calibration{}, false, fmt.Errorf("resolve: calibration bins of %s: %w", kind, err)
	}
	c.Reviewed = int(row.Reviewed)
	c.Calibrated = c.Reviewed >= MinReviewed
	return c, row.AutoApply, nil
}

// Recalibrate computes the calibration of a kind from its reviewed
// outcomes. When auto-apply is on and the kind no longer earns it, it turns
// off with an audit entry (REQ-RES-005).
func (s *Service) Recalibrate(ctx context.Context, kind string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := s.recalibrate(ctx, tx, kind)
		return err
	})
}

func (s *Service) recalibrate(ctx context.Context, tx pgx.Tx, kind string) (Calibration, error) {
	q := New(tx)
	rows, err := q.ReviewedOutcomes(ctx, kind)
	if err != nil {
		return Calibration{}, err
	}
	os := make([]Outcome, len(rows))
	for i, r := range rows {
		os[i] = Outcome{RawP: r.RawP, Band: r.Band, Accepted: r.Accepted}
	}
	c := Calibrate(os)
	bins, _ := json.Marshal(c.Bins)
	var high *float64
	if h, ok := c.Precision[BandHigh]; ok {
		high = &h.Value
	}
	auto, err := q.UpsertCalibration(ctx, UpsertCalibrationParams{Kind: kind, Bins: bins, Reviewed: int32(c.Reviewed), HighPrecision: high}) //nolint:gosec // Counts are small.
	if err != nil {
		return c, err
	}
	if auto && !c.CanAutoApply() {
		if err := q.SetAutoApply(ctx, SetAutoApplyParams{Kind: kind, AutoApply: false}); err != nil {
			return c, err
		}
		return c, audit.Write(ctx, tx, audit.Entry{Action: "resolve.auto_apply", Target: "decision_kind:" + kind,
			Details: map[string]any{"enabled": false, "reason": "precision", "reviewed": c.Reviewed, "highPrecision": high}})
	}
	return c, nil
}

// review stores the outcome of a decision and recalibrates its kind.
func (s *Service) review(ctx context.Context, tx pgx.Tx, id int64, kind, outcome string) error {
	now := s.now()
	if err := New(tx).SetOutcome(ctx, SetOutcomeParams{ID: id, Outcome: outcome, ReviewedAt: &now}); err != nil {
		return err
	}
	_, err := s.recalibrate(ctx, tx, kind)
	return err
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
