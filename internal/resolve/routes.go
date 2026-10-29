package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/visibility"
)

// Subject is an entity that a card or a decision is about.
type Subject struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Card is one open review card.
type Card struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	Target     string     `json:"target"`
	Payload    any        `json:"payload"`
	DecisionID *int64     `json:"decisionId,omitempty"`
	P          *float64   `json:"p,omitempty"`
	Band       string     `json:"band,omitempty" doc:"Empty when the decide function was off."`
	Calibrated bool       `json:"calibrated"`
	Precision  *Precision `json:"precision,omitempty" doc:"The measured precision of the band."`
	Reasons    []string   `json:"reasons"`
	Subjects   []Subject  `json:"subjects"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// DecisionView is a stored decision as the API shows it.
type DecisionView struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	P          *float64   `json:"p,omitempty"`
	Band       string     `json:"band,omitempty"`
	Outcome    string     `json:"outcome"`
	Reasons    []string   `json:"reasons"`
	Subjects   []Subject  `json:"subjects"`
	At         time.Time  `json:"at"`
	ReviewedAt *time.Time `json:"reviewedAt,omitempty"`
}

// KindCalibration is the calibration of one decision kind.
type KindCalibration struct {
	Kind         string               `json:"kind"`
	Reviewed     int                  `json:"reviewed"`
	Calibrated   bool                 `json:"calibrated"`
	AutoApply    bool                 `json:"autoApply"`
	CanAutoApply bool                 `json:"canAutoApply"`
	Bins         []Bin                `json:"bins"`
	Precision    map[string]Precision `json:"precision"`
}

func (s *Service) subjects(ctx context.Context, vis visibility.Set, refs []string) ([]Subject, error) {
	rows, err := New(s.Pool).EntityRefs(ctx, EntityRefsParams{Keys: refs, Visible: vis})
	if err != nil {
		return nil, err
	}
	out := []Subject{}
	for _, k := range refs {
		for _, r := range rows {
			if r.Key == k {
				out = append(out, Subject{Key: r.Key, Name: r.Name, Type: r.Type})
			}
		}
	}
	return out, nil
}

func reasonsOf(raw []byte) []string {
	var rs []string
	_ = json.Unmarshal(raw, &rs)
	return orEmpty(rs)
}

// Cards lists the open cards whose inputs are all visible.
func (s *Service) Cards(ctx context.Context, vis visibility.Set, kind string, limit int) ([]Card, error) {
	var k *string
	if kind != "" {
		k = &kind
	}
	rows, err := New(s.Pool).VisibleCards(ctx, VisibleCardsParams{Visible: vis, Kind: k, Lim: int32(limit)}) //nolint:gosec // The limit is at most 500.
	if err != nil {
		return nil, err
	}
	cals := map[string]Calibration{}
	out := []Card{}
	for _, r := range rows {
		c := Card{ID: r.ID, Kind: r.Kind, Target: r.Target, DecisionID: r.DecisionID, P: r.RawP, Reasons: reasonsOf(r.Reasons), CreatedAt: r.CreatedAt}
		_ = json.Unmarshal(r.Payload, &c.Payload)
		if r.Band != nil {
			c.Band = *r.Band
		}
		refs := r.SubjectRefs
		if refs == nil && len(r.Target) > len("entity:") && r.Target[:len("entity:")] == "entity:" {
			refs = []string{r.Target[len("entity:"):]}
		}
		if c.Subjects, err = s.subjects(ctx, vis, refs); err != nil {
			return nil, err
		}
		if r.DecisionID != nil {
			kind := r.Kind
			if kind == "merge" {
				kind = KindSameEntity
			}
			cal, ok := cals[kind]
			if !ok {
				if cal, err = s.kindCalibration(ctx, kind); err != nil {
					return nil, err
				}
				cals[kind] = cal
			}
			c.Calibrated = cal.Calibrated
			if p, ok := cal.Precision[c.Band]; ok && c.Band != "" {
				c.Precision = &p
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// kindCalibration computes the calibration of a kind with its precision.
func (s *Service) kindCalibration(ctx context.Context, kind string) (Calibration, error) {
	rows, err := New(s.Pool).ReviewedOutcomes(ctx, kind)
	if err != nil {
		return Calibration{}, err
	}
	os := make([]Outcome, len(rows))
	for i, r := range rows {
		os[i] = Outcome{RawP: r.RawP, Band: r.Band, Accepted: r.Accepted}
	}
	return Calibrate(os), nil
}

// ErrCardDone means that the card has a decision already.
var ErrCardDone = errors.New("resolve: the card is decided")

// ErrNotFound means that the card or decision does not exist or is not
// visible.
var ErrNotFound = errors.New("resolve: not found")

// errCardKind means that the card kind has no action here.
var errCardKind = errors.New("resolve: this card kind is decided on its own page")

// Accept accepts a card as a reviewer. A merge card merges its entities
// with an optional canonical name and stores the verdict same.
func (s *Service) Accept(ctx context.Context, vis visibility.Set, id int64, canonical, by string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.openCard(ctx, tx, vis, id)
		if err != nil {
			return err
		}
		switch c.Kind {
		case "merge":
			if _, _, err := s.Entities.MergeTx(ctx, tx, vis, entities.MergeRequest{Keys: c.SubjectRefs, Canonical: canonical, By: by}); err != nil {
				return err
			}
			if err := s.verdict(ctx, tx, c.SubjectRefs, "same", by); err != nil {
				return err
			}
		default:
			return errCardKind
		}
		return s.closeCard(ctx, tx, c, "accepted", "", by)
	})
}

// Reject rejects a card with a reason. A merge card stores the verdict
// different, so the pair is not suggested again (REQ-RES-006).
func (s *Service) Reject(ctx context.Context, vis visibility.Set, id int64, reason, by string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.openCard(ctx, tx, vis, id)
		if err != nil {
			return err
		}
		if c.Kind == "merge" {
			if err := s.verdict(ctx, tx, c.SubjectRefs, "different", by); err != nil {
				return err
			}
		}
		return s.closeCard(ctx, tx, c, "rejected", reason, by)
	})
}

func (s *Service) openCard(ctx context.Context, tx pgx.Tx, vis visibility.Set, id int64) (CardForUpdateRow, error) {
	c, err := New(tx).CardForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	for _, conn := range c.Connections {
		if !slices.Contains(vis, conn) {
			return c, ErrNotFound
		}
	}
	if c.Status != "open" {
		return c, ErrCardDone
	}
	return c, nil
}

func (s *Service) closeCard(ctx context.Context, tx pgx.Tx, c CardForUpdateRow, status, reason, by string) error {
	now := s.now()
	if err := New(tx).DecideCard(ctx, DecideCardParams{ID: c.ID, Status: status, ReviewerID: &by, Reason: reason, DecidedAt: &now}); err != nil {
		return err
	}
	if c.DecisionID != nil && c.DecisionKind != nil {
		if err := s.review(ctx, tx, *c.DecisionID, *c.DecisionKind, status); err != nil {
			return err
		}
	}
	return audit.Write(ctx, tx, audit.Entry{Action: "cards." + status, Target: fmt.Sprintf("card:%d", c.ID),
		Details: map[string]any{"kind": c.Kind, "reason": reason, "decision": c.DecisionID}})
}

func (s *Service) verdict(ctx context.Context, tx pgx.Tx, refs []string, verdict, by string) error {
	if len(refs) != 2 {
		return nil
	}
	rows, err := New(tx).SourcesOfKeys(ctx, refs)
	if err != nil || len(rows) != 2 {
		return err
	}
	return New(tx).AddVerdict(ctx, AddVerdictParams{X: rows[0], Y: rows[1], Kind: KindSameEntity, Verdict: verdict, By: &by})
}

// Undo splits the merge of an auto-applied decision and counts the
// decision as rejected for calibration.
func (s *Service) Undo(ctx context.Context, vis visibility.Set, id int64) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		d, err := New(tx).DecisionForUpdate(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		for _, conn := range d.Connections {
			if !slices.Contains(vis, conn) {
				return ErrNotFound
			}
		}
		if d.Outcome != "auto" {
			return ErrCardDone
		}
		if d.MergeID != nil {
			if err := s.Entities.SplitTx(ctx, tx, *d.MergeID); err != nil && !errors.Is(err, entities.ErrMergeUndone) {
				return err
			}
		}
		if err := s.review(ctx, tx, d.ID, d.Kind, "undone"); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "resolve.undo", Target: fmt.Sprintf("decision:%d", d.ID),
			Details: map[string]any{"kind": d.Kind, "merge": d.MergeID}})
	})
}

// Register adds the card, decision and calibration routes.
func (s *Service) Register(a huma.API) {
	reviewers := huma.Middlewares{api.Require(auth.RoleReviewer)}
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}
	tags := []string{"review"}

	type cardsIn struct {
		Kind  string `query:"kind" enum:"correction,finding,merge,mention_link,unnamed,new_term,term_edit,conflict,needs_edit,"`
		Limit int    `query:"limit" minimum:"1" maximum:"500" default:"200"`
	}
	type cardsOut struct {
		Body struct {
			Cards []Card `json:"cards"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listCards", Method: http.MethodGet, Path: "/api/v1/cards", Tags: tags,
		Summary: "List open review cards", Description: "A card shows only when all its inputs are visible to you.",
		Middlewares: reviewers,
	}, func(ctx context.Context, in *cardsIn) (*cardsOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		cs, err := s.Cards(ctx, vis, in.Kind, in.Limit)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &cardsOut{}
		out.Body.Cards = cs
		return out, nil
	})

	cardErr := func(err error) error {
		switch {
		case err == nil:
			return nil
		case errors.Is(err, ErrNotFound), errors.Is(err, entities.ErrNotFound):
			return api.ErrNotFound
		case errors.Is(err, ErrCardDone):
			return api.Problem(http.StatusConflict, "decided", "The card already has a decision.")
		case errors.Is(err, errCardKind):
			return api.Problem(http.StatusUnprocessableEntity, "card_kind", "Decide this card on its own page.")
		}
		return api.Internal(err)
	}
	type acceptIn struct {
		ID   int64 `path:"id"`
		Body struct {
			CanonicalName string `json:"canonicalName,omitempty" maxLength:"200"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "acceptCard", Method: http.MethodPost, Path: "/api/v1/cards/{id}/accept", Tags: tags,
		Summary: "Accept a card", Description: "A merge card merges its entities. The canonical name is optional.",
		Middlewares: reviewers, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *acceptIn) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		return nil, cardErr(s.Accept(ctx, vis, in.ID, in.Body.CanonicalName, api.Caller(ctx).User.ID))
	})
	type rejectIn struct {
		ID   int64 `path:"id"`
		Body struct {
			Reason string `json:"reason" minLength:"1" maxLength:"1000"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "rejectCard", Method: http.MethodPost, Path: "/api/v1/cards/{id}/reject", Tags: tags,
		Summary: "Reject a card", Description: "A reason is required. A rejected merge card stores the verdict that the pair is not the same.",
		Middlewares: reviewers, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *rejectIn) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		return nil, cardErr(s.Reject(ctx, vis, in.ID, in.Body.Reason, api.Caller(ctx).User.ID))
	})

	type decisionsIn struct {
		Outcome string `query:"outcome" enum:"open,accepted,rejected,auto,undone" default:"auto"`
		Limit   int    `query:"limit" minimum:"1" maximum:"500" default:"200"`
	}
	type decisionsOut struct {
		Body struct {
			Decisions []DecisionView `json:"decisions"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listDecisions", Method: http.MethodGet, Path: "/api/v1/decisions", Tags: tags,
		Summary: "List decisions by outcome", Description: "outcome=auto lists the auto-applied decisions. A decision shows only when all its inputs are visible to you.",
		Middlewares: reviewers,
	}, func(ctx context.Context, in *decisionsIn) (*decisionsOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		rows, err := New(s.Pool).VisibleDecisions(ctx, VisibleDecisionsParams{Outcome: in.Outcome, Visible: vis, Lim: int32(in.Limit)}) //nolint:gosec // At most 500.
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &decisionsOut{}
		out.Body.Decisions = []DecisionView{}
		for _, r := range rows {
			v := DecisionView{ID: r.ID, Kind: r.Kind, P: r.RawP, Outcome: r.Outcome, Reasons: reasonsOf(r.Reasons), At: r.At, ReviewedAt: r.ReviewedAt}
			if r.Band != nil {
				v.Band = *r.Band
			}
			if v.Subjects, err = s.subjects(ctx, vis, r.SubjectRefs); err != nil {
				return nil, api.Internal(err)
			}
			out.Body.Decisions = append(out.Body.Decisions, v)
		}
		return out, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "undoDecision", Method: http.MethodPost, Path: "/api/v1/decisions/{id}/undo", Tags: tags,
		Summary: "Undo an auto-applied decision", Description: "An auto merge is split. Calibration counts the decision as rejected.",
		Middlewares: reviewers, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		err = s.Undo(ctx, vis, in.ID)
		if errors.Is(err, ErrCardDone) {
			return nil, api.Problem(http.StatusConflict, "not_auto", "The decision is not auto-applied.")
		}
		return nil, cardErr(err)
	})

	type calOut struct {
		Body struct {
			Kinds []KindCalibration `json:"kinds"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listCalibration", Method: http.MethodGet, Path: "/api/v1/calibration", Tags: tags,
		Summary: "List the calibration of each decision kind", Description: "Before 50 reviewed outcomes the default thresholds apply and calibrated is false.",
		Middlewares: reviewers,
	}, func(ctx context.Context, _ *struct{}) (*calOut, error) {
		out := &calOut{}
		for _, k := range Kinds {
			kc, err := s.KindCalibration(ctx, k)
			if err != nil {
				return nil, api.Internal(err)
			}
			out.Body.Kinds = append(out.Body.Kinds, kc)
		}
		return out, nil
	})
	type autoIn struct {
		Kind string `path:"kind" enum:"same_entity,mention_link,describes,equivalent,still_supports,term_worthy"`
		Body struct {
			AutoApply bool `json:"autoApply"`
		}
	}
	type autoOut struct{ Body KindCalibration }
	huma.Register(a, huma.Operation{
		OperationID: "setAutoApply", Method: http.MethodPut, Path: "/api/v1/calibration/{kind}", Tags: tags,
		Summary:     "Turn auto-apply of a decision kind on or off",
		Description: "Auto-apply needs 50 reviewed outcomes and High precision of 0.95 or more over the last 200. Assay turns it off when precision drops.",
		Middlewares: admins,
	}, func(ctx context.Context, in *autoIn) (*autoOut, error) {
		kc, err := s.KindCalibration(ctx, in.Kind)
		if err != nil {
			return nil, api.Internal(err)
		}
		if in.Body.AutoApply && !kc.CanAutoApply {
			return nil, api.Problem(http.StatusConflict, "not_earned", "This kind needs 50 reviewed outcomes and High precision of 0.95 or more.")
		}
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if err := New(tx).SetAutoApply(ctx, SetAutoApplyParams{Kind: in.Kind, AutoApply: in.Body.AutoApply}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "resolve.auto_apply", Target: "decision_kind:" + in.Kind,
				Details: map[string]any{"enabled": in.Body.AutoApply, "reviewed": kc.Reviewed}})
		})
		if err != nil {
			return nil, api.Internal(err)
		}
		kc.AutoApply = in.Body.AutoApply
		return &autoOut{Body: kc}, nil
	})
}

// KindCalibration returns the calibration of a kind as the API shows it.
func (s *Service) KindCalibration(ctx context.Context, kind string) (KindCalibration, error) {
	c, err := s.kindCalibration(ctx, kind)
	if err != nil {
		return KindCalibration{}, err
	}
	_, auto, err := s.calibration(ctx, kind)
	if err != nil {
		return KindCalibration{}, err
	}
	return KindCalibration{Kind: kind, Reviewed: c.Reviewed, Calibrated: c.Calibrated, AutoApply: auto, CanAutoApply: c.CanAutoApply(),
		Bins: c.Bins, Precision: c.Precision}, nil
}
