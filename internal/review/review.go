// Package review holds what people propose for review: problem reports on
// claims and findings with quotes, and the result each proposer sees.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/visibility"
)

// Service records problem reports and findings.
type Service struct {
	Pool    *pgxpool.Pool
	Resolve *resolve.Service
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ClaimValue is the object of a claim: a visible entity key or a text.
type ClaimValue struct {
	Key  string `json:"key,omitempty" maxLength:"500" doc:"The key of a visible entity."`
	Text string `json:"text,omitempty" maxLength:"2000"`
}

// ReportSource backs a report: a visible item, a note, or both.
type ReportSource struct {
	ItemID int64  `json:"itemId,omitempty" doc:"A visible item."`
	Note   string `json:"note,omitempty" maxLength:"2000"`
}

// ProblemReport is a problem report on one claim.
type ProblemReport struct {
	Problem string        `json:"problem" enum:"wrong_value,outdated,not_here"`
	Reason  string        `json:"reason" minLength:"1" maxLength:"2000"`
	Value   *ClaimValue   `json:"value,omitempty" doc:"The correct value. Required for wrong_value."`
	Source  *ReportSource `json:"source,omitempty"`
}

// FindingQuote is one evidence quote of a finding.
type FindingQuote struct {
	ItemID int64  `json:"itemId"`
	Quote  string `json:"quote" minLength:"1" maxLength:"2000" doc:"An exact part of a passage of the item."`
}

// ProposedFinding proposes a new claim with quotes from visible items
// (REQ-REV-005).
type ProposedFinding struct {
	Subject   string         `json:"subject" maxLength:"500" doc:"The key of a visible entity."`
	Predicate string         `json:"predicate"`
	Object    ClaimValue     `json:"object"`
	Evidence  []FindingQuote `json:"evidence" minItems:"1" maxItems:"20"`
}

// correction is the payload of a correction card.
type correction struct {
	Claim      int64         `json:"claim"`
	Problem    string        `json:"problem"`
	Reason     string        `json:"reason"`
	Value      *ClaimValue   `json:"value,omitempty"`
	ValueClaim int64         `json:"valueClaim,omitempty"`
	Source     *ReportSource `json:"source,omitempty"`
	Items      []int64       `json:"items"`
}

// finding is the payload of a finding card.
type finding struct {
	Claim     int64          `json:"claim"`
	Predicate string         `json:"predicate"`
	Evidence  []FindingQuote `json:"evidence"`
}

// ErrNotFound means that a claim, an entity or an item does not exist or
// is not visible.
var ErrNotFound = errors.New("review: not found")

// InvalidError is input that the claim rules or the quotes refuse.
type InvalidError struct{ Code, Message string }

func (e *InvalidError) Error() string { return e.Message }

func invalid(code, msg string) error { return &InvalidError{Code: code, Message: msg} }

// Report records a problem report as a correction card. A correct value
// becomes a Proposed human claim until a reviewer decides.
func (s *Service) Report(ctx context.Context, vis visibility.Set, claimID int64, r ProblemReport, proposer, via string) (int64, error) {
	r.Reason = strings.TrimSpace(r.Reason)
	if r.Reason == "" {
		return 0, invalid("reason", "Give a reason.")
	}
	if r.Problem == "wrong_value" && r.Value == nil {
		return 0, invalid("value", "Give the correct value.")
	}
	var id int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		c, err := q.VisibleClaim(ctx, VisibleClaimParams{ID: claimID, Visible: vis})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		p := correction{Claim: c.ID, Problem: r.Problem, Reason: r.Reason, Value: r.Value, Source: r.Source, Items: c.Items}
		conns := slices.Clone(c.Connections)
		quote := r.Reason
		if r.Source != nil {
			if n := strings.TrimSpace(r.Source.Note); n != "" {
				quote = n
			}
			if r.Source.ItemID != 0 {
				it, err := q.VisibleItem(ctx, VisibleItemParams{ID: r.Source.ItemID, Visible: vis})
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				if err != nil {
					return err
				}
				p.Items = []int64{it.ID}
				if !slices.Contains(conns, it.ConnectionID) {
					conns = append(conns, it.ConnectionID)
				}
			}
		}
		var object *objectRef
		if r.Value != nil {
			if object, err = s.object(ctx, q, vis, *r.Value, c.Predicate, c.SubjectType); err != nil {
				return err
			}
		}
		if object != nil {
			if p.ValueClaim, err = q.UpsertClaim(ctx, UpsertClaimParams{SubjectID: c.SubjectID, Predicate: c.Predicate,
				ObjectEntityID: object.id, ObjectText: object.text, ObjectNorm: object.norm, ScopeEnv: c.ScopeEnv}); err != nil {
				return err
			}
		}
		payload, _ := json.Marshal(p)
		if id, err = q.InsertProposal(ctx, InsertProposalParams{Kind: "correction", Target: "entity:" + c.SubjectKey, Payload: payload,
			ProposerID: &proposer, Via: via, Connections: conns}); err != nil {
			return err
		}
		if p.ValueClaim != 0 {
			if err := content.New(tx).AddNoteEvidence(ctx, content.AddNoteEvidenceParams{ClaimID: p.ValueClaim, Items: p.Items,
				PassageHash: noteHash(quote), Quote: quote, ProposalID: &id}); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "review.report", Target: fmt.Sprintf("claim:%d", c.ID),
			Details: map[string]any{"proposal": id, "problem": r.Problem}})
	})
	return id, err
}

type objectRef struct {
	id   *int64
	text *string
	norm string
}

// object checks a value against the predicate registry.
func (s *Service) object(ctx context.Context, q *Queries, vis visibility.Set, v ClaimValue, predicate, subjectType string) (*objectRef, error) {
	key, text := strings.TrimSpace(v.Key), strings.TrimSpace(v.Text)
	switch {
	case key != "" && text != "", key == "" && text == "":
		return nil, invalid("value", "Give an entity or a text, not both.")
	case key != "":
		e, err := q.VisibleEntity(ctx, VisibleEntityParams{Key: key, Visible: vis})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if err := claims.Check(predicate, subjectType, e.Type); err != nil {
			return nil, invalid("value", err.Error())
		}
		return &objectRef{id: &e.ID}, nil
	}
	if err := claims.Check(predicate, subjectType, claims.ObjText); err != nil {
		return nil, invalid("value", err.Error())
	}
	return &objectRef{text: &text, norm: entities.NormalizeAlias(text)}, nil
}

// noteHash identifies a note or a reason as the passage of human evidence.
func noteHash(s string) []byte {
	h := sha256.Sum256([]byte("note:" + s))
	return h[:]
}

// Propose records a finding as a finding card with a Proposed claim. Each
// quote must be an exact part of a passage of a visible item.
func (s *Service) Propose(ctx context.Context, vis visibility.Set, f ProposedFinding, proposer, via string) (proposal, claim int64, err error) {
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		subject, err := q.VisibleEntity(ctx, VisibleEntityParams{Key: strings.TrimSpace(f.Subject), Visible: vis})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		object, err := s.object(ctx, q, vis, f.Object, f.Predicate, subject.Type)
		if err != nil {
			return err
		}
		type quoted struct {
			FindingQuote
			hash []byte
		}
		var qs []quoted
		var conns []int64
		for _, e := range f.Evidence {
			it, err := q.VisibleItem(ctx, VisibleItemParams{ID: e.ItemID, Visible: vis})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			hash, err := q.QuotedPassage(ctx, QuotedPassageParams{ItemID: it.ID, Visible: vis, Quote: e.Quote})
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("quote", fmt.Sprintf("The quote is not in item %d.", it.ID))
			}
			if err != nil {
				return err
			}
			qs = append(qs, quoted{FindingQuote: e, hash: hash})
			if !slices.Contains(conns, it.ConnectionID) {
				conns = append(conns, it.ConnectionID)
			}
		}
		if claim, err = q.UpsertClaim(ctx, UpsertClaimParams{SubjectID: subject.ID, Predicate: f.Predicate, ObjectEntityID: object.id,
			ObjectText: object.text, ObjectNorm: object.norm}); err != nil {
			return err
		}
		payload, _ := json.Marshal(finding{Claim: claim, Predicate: f.Predicate, Evidence: f.Evidence})
		if proposal, err = q.InsertProposal(ctx, InsertProposalParams{Kind: "finding", Target: "entity:" + subject.Key, Payload: payload,
			ProposerID: &proposer, Via: via, Connections: conns}); err != nil {
			return err
		}
		for _, e := range qs {
			if err := content.New(tx).AddProposalEvidence(ctx, content.AddProposalEvidenceParams{ClaimID: claim, ItemID: e.ItemID,
				PassageHash: e.hash, Quote: e.Quote, ProposalID: &proposal}); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "review.finding", Target: "entity:" + subject.Key,
			Details: map[string]any{"proposal": proposal, "claim": claim, "predicate": f.Predicate}})
	})
	return proposal, claim, err
}

// ApplyCorrection accepts a correction card (REQ-REV-003). A correct value
// wins its subject, predicate and scope by a resolution. Without a value,
// a human row disputes the claim. The source system does not change.
func (s *Service) ApplyCorrection(ctx context.Context, tx pgx.Tx, vis visibility.Set, payload []byte, _, reviewer string, _ resolve.Edits) error {
	var p correction
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	if p.ValueClaim != 0 {
		_, err := claims.Resolve(ctx, tx, vis, p.ValueClaim, reviewer, s.now())
		return err
	}
	quote := p.Reason
	if p.Source != nil && strings.TrimSpace(p.Source.Note) != "" {
		quote = strings.TrimSpace(p.Source.Note)
	}
	return content.New(tx).AddDispute(ctx, content.AddDisputeParams{ClaimID: p.Claim, PassageHash: noteHash(quote), Quote: quote,
		Items: p.Items})
}

// Proposal is a proposal as its proposer sees it, with the result.
type Proposal struct {
	ID        int64      `json:"id"`
	Kind      string     `json:"kind"`
	Target    string     `json:"target"`
	Status    string     `json:"status" enum:"open,accepted,rejected,needs_evidence"`
	Reason    string     `json:"reason" doc:"The reason of a reviewer who rejected the proposal or asked for evidence."`
	Via       string     `json:"via" enum:"ui,mcp,system"`
	CreatedAt time.Time  `json:"createdAt"`
	DecidedAt *time.Time `json:"decidedAt"`
}

// Mine lists the newest proposals of a person.
func (s *Service) Mine(ctx context.Context, proposer string, limit int) ([]Proposal, error) {
	rows, err := New(s.Pool).Proposals(ctx, ProposalsParams{ProposerID: &proposer, Lim: int32(limit)}) //nolint:gosec // At most 200.
	if err != nil {
		return nil, err
	}
	out := make([]Proposal, len(rows))
	for i, r := range rows {
		out[i] = Proposal{ID: r.ID, Kind: r.Kind, Target: r.Target, Status: r.Status, Reason: r.Reason, Via: r.Via, CreatedAt: r.CreatedAt,
			DecidedAt: r.DecidedAt}
	}
	return out, nil
}
