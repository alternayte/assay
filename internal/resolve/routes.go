package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/visibility"
)

// Subject is an entity that a card or a decision is about.
type Subject struct {
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Aliases []string `json:"aliases" doc:"The visible aliases."`
	Sources []string `json:"sources" doc:"The names of the visible connections with a source of the entity."`

	domain *int64
}

// Card is one open review card.
type Card struct {
	ID         int64          `json:"id"`
	Kind       string         `json:"kind"`
	Target     string         `json:"target"`
	Payload    any            `json:"payload"`
	DecisionID *int64         `json:"decisionId,omitempty"`
	P          *float64       `json:"p,omitempty"`
	Band       string         `json:"band,omitempty" doc:"Empty when the decide function was off."`
	Calibrated bool           `json:"calibrated"`
	Precision  *Precision     `json:"precision,omitempty" doc:"The measured precision of the band."`
	Reasons    []string       `json:"reasons"`
	Subjects   []Subject      `json:"subjects"`
	Members    []Subject      `json:"members,omitempty" doc:"The entities of a merge cluster. Accept merges the members that you pick."`
	Pairs      []int64        `json:"pairs,omitempty" doc:"The merge cards of a cluster."`
	Claims     []claims.Claim `json:"claims,omitempty" doc:"The claims of a correction, finding or conflict card, with their visible evidence."`
	Status     string         `json:"status" enum:"open,accepted,rejected,needs_evidence"`
	Reason     string         `json:"reason,omitempty"`
	Reviewer   string         `json:"reviewer,omitempty" doc:"The email of the reviewer of a decided card."`
	DecidedAt  *time.Time     `json:"decidedAt,omitempty"`
	Impact     int            `json:"impact" doc:"Visible claims, pages and questions of the last 30 days that touch the subjects."`
	Score      float64        `json:"score" doc:"Impact multiplied by uncertainty. The queue shows the highest score first."`
	CreatedAt  time.Time      `json:"createdAt"`

	refs        []string
	connections []int64
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
	m, err := s.subjectMap(ctx, vis, refs)
	if err != nil {
		return nil, err
	}
	return pick(m, refs), nil
}

// subjectMap loads the visible subjects of refs by key.
func (s *Service) subjectMap(ctx context.Context, vis visibility.Set, refs []string) (map[string]Subject, error) {
	rows, err := New(s.Pool).EntityRefs(ctx, EntityRefsParams{Keys: refs, Visible: vis})
	if err != nil {
		return nil, err
	}
	m := make(map[string]Subject, len(rows))
	for _, r := range rows {
		m[r.Key] = Subject{Key: r.Key, Name: r.Name, Type: r.Type, Aliases: orEmpty(r.Aliases), Sources: orEmpty(r.Sources), domain: r.DomainID}
	}
	return m, nil
}

func pick(m map[string]Subject, refs []string) []Subject {
	out := []Subject{}
	for _, k := range refs {
		if sub, ok := m[k]; ok && !slices.ContainsFunc(out, func(x Subject) bool { return x.Key == k }) {
			out = append(out, sub)
		}
	}
	return out
}

func reasonsOf(raw []byte) []string {
	var rs []string
	_ = json.Unmarshal(raw, &rs)
	return orEmpty(rs)
}

// CardFilter selects the cards of the queue (REQ-REV-011). Zero values
// select all.
type CardFilter struct {
	Decided    bool
	Kind, Band string
	Connection int64
	Type       string
	Domain     int64
	Query      string
}

func (f CardFilter) match(c Card) bool {
	if f.Band != "" && c.Band != f.Band {
		return false
	}
	if f.Connection != 0 && !slices.Contains(c.connections, f.Connection) {
		return false
	}
	if f.Type != "" && !slices.ContainsFunc(c.Subjects, func(x Subject) bool { return x.Type == f.Type }) {
		return false
	}
	if f.Domain != 0 && !slices.ContainsFunc(c.Subjects, func(x Subject) bool { return x.domain != nil && *x.domain == f.Domain }) {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(f.Query)); q != "" {
		text, _ := json.Marshal(c.Payload)
		hay := strings.ToLower(c.Kind + " " + string(text))
		for _, x := range c.Subjects {
			hay += " " + strings.ToLower(x.Name+" "+strings.Join(x.Aliases, " "))
		}
		return strings.Contains(hay, q)
	}
	return true
}

// maxQueue is the number of cards that the queue order ranks.
const maxQueue = 5000

// Cards lists the cards whose inputs are all visible: open cards in queue
// order (REQ-REV-010), or decided cards with the newest decision first.
func (s *Service) Cards(ctx context.Context, vis visibility.Set, f CardFilter, limit int) ([]Card, error) {
	var k *string
	if f.Kind != "" {
		k = &f.Kind
	}
	q := New(s.Pool)
	var out []Card
	if f.Decided {
		rows, err := q.DecidedCards(ctx, DecidedCardsParams{Visible: vis, Kind: k, Lim: maxQueue})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, newCard(r.ID, r.Kind, r.Target, r.Payload, r.CreatedAt, r.DecisionID, r.RawP, r.Band, r.Reasons, r.SubjectRefs,
				r.Connections))
			c := &out[len(out)-1]
			c.Status, c.Reason, c.Reviewer, c.DecidedAt = r.Status, r.Reason, r.Reviewer, r.DecidedAt
		}
	} else {
		rows, err := q.VisibleCards(ctx, VisibleCardsParams{Visible: vis, Kind: k, Lim: maxQueue})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, newCard(r.ID, r.Kind, r.Target, r.Payload, r.CreatedAt, r.DecisionID, r.RawP, r.Band, r.Reasons, r.SubjectRefs,
				r.Connections))
		}
		out = Cluster(out)
	}
	var keys []string
	for _, c := range out {
		keys = append(keys, c.refs...)
	}
	subs, err := s.subjectMap(ctx, vis, keys)
	if err != nil {
		return nil, err
	}
	if !f.Decided {
		impact := map[string]int{}
		if len(keys) > 0 {
			is, err := q.Impact(ctx, ImpactParams{Keys: keys, Visible: vis, Since: s.now().AddDate(0, 0, -30)})
			if err != nil {
				return nil, err
			}
			for _, i := range is {
				impact[i.Key] = int(i.Impact)
			}
		}
		for i := range out {
			for _, ref := range out[i].refs {
				out[i].Impact += impact[ref]
			}
		}
		SortQueue(out)
	}
	kept := []Card{}
	for _, c := range out {
		c.Subjects = pick(subs, c.refs)
		if c.Kind == "merge" {
			c.Members = c.Subjects
		}
		if f.match(c) {
			kept = append(kept, c)
		}
		if len(kept) == limit {
			break
		}
	}
	if err := s.enrich(ctx, vis, kept); err != nil {
		return nil, err
	}
	return kept, nil
}

func newCard(id int64, kind, target string, payload []byte, at time.Time, decision *int64, p *float64, band *string, reasons []byte,
	refs []string, conns []int64) Card {
	c := Card{ID: id, Kind: kind, Target: target, DecisionID: decision, P: p, Reasons: reasonsOf(reasons), CreatedAt: at, Status: "open",
		refs: refs, connections: conns}
	_ = json.Unmarshal(payload, &c.Payload)
	if band != nil {
		c.Band = *band
	}
	if c.refs == nil && strings.HasPrefix(target, "entity:") {
		c.refs = []string{strings.TrimPrefix(target, "entity:")}
	}
	if kind == "merge" {
		c.Pairs = []int64{id}
	}
	return c
}

// enrich adds the calibration and the claims of the cards.
func (s *Service) enrich(ctx context.Context, vis visibility.Set, cs []Card) error {
	cals := map[string]Calibration{}
	var ids []int64
	want := make([][]int64, len(cs))
	for i := range cs {
		c := &cs[i]
		var ref struct{ Claim, ValueClaim, Chosen int64 }
		if b, err := json.Marshal(c.Payload); err == nil {
			_ = json.Unmarshal(b, &ref)
		}
		for _, id := range []int64{ref.Claim, ref.ValueClaim, ref.Chosen} {
			if id != 0 {
				want[i] = append(want[i], id)
				ids = append(ids, id)
			}
		}
		if c.DecisionID == nil {
			continue
		}
		kind := c.Kind
		if kind == "merge" {
			kind = KindSameEntity
		}
		cal, ok := cals[kind]
		if !ok {
			var err error
			if cal, err = s.kindCalibration(ctx, kind); err != nil {
				return err
			}
			cals[kind] = cal
		}
		c.Calibrated = cal.Calibrated
		if p, ok := cal.Precision[c.Band]; ok && c.Band != "" {
			c.Precision = &p
		}
	}
	if len(ids) == 0 {
		return nil
	}
	cl, err := claims.Load(ctx, s.Pool, vis, claims.Query{IDs: ids})
	if err != nil {
		return err
	}
	for i := range cs {
		for _, id := range want[i] {
			for _, x := range cl {
				if x.ID == id {
					cs[i].Claims = append(cs[i].Claims, x)
				}
			}
		}
	}
	return nil
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
// with an optional canonical name and stores the verdict same. A link card
// adds the claim of its mention. An unnamed card makes its entity active,
// with the canonical name as its name.
func (s *Service) Accept(ctx context.Context, vis visibility.Set, id int64, edits Edits, by string) error {
	canonical := strings.TrimSpace(edits.Name)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.openCard(ctx, tx, vis, id)
		if err != nil {
			return err
		}
		switch c.Kind {
		case "merge":
			return s.acceptCluster(ctx, tx, vis, c.ID, edits.Members, canonical, by)
		case "mention_link":
			if err := s.acceptMention(ctx, tx, c.Payload); err != nil {
				return err
			}
		case "unnamed":
			if err := s.acceptUnnamed(ctx, tx, c.Payload, canonical); err != nil {
				return err
			}
		case "finding":
			// The evidence of an accepted proposal counts as human
			// evidence, so the claim becomes Supported.
		default:
			apply, ok := s.Apply[c.Kind]
			if !ok {
				return errCardKind
			}
			proposer := ""
			if c.ProposerID != nil {
				proposer = *c.ProposerID
			}
			if err := apply(ctx, tx, vis, c.Payload, proposer, by, edits); err != nil {
				return err
			}
		}
		return s.closeCard(ctx, tx, c, "accepted", "", by)
	})
}

// Reject rejects a card with a reason. A merge card stores the verdict
// different, so the pair is not suggested again (REQ-RES-006). An unnamed
// card deletes its proposed entity.
func (s *Service) Reject(ctx context.Context, vis visibility.Set, id int64, reason, by string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.openCard(ctx, tx, vis, id)
		if err != nil {
			return err
		}
		switch c.Kind {
		case "merge":
			return s.rejectCluster(ctx, tx, vis, c.ID, reason, by)
		case "unnamed":
			if err := s.rejectUnnamed(ctx, tx, c.Payload); err != nil {
				return err
			}
		}
		return s.closeCard(ctx, tx, c, "rejected", reason, by)
	})
}

// NeedsEvidence closes a card with a request for evidence and a reason.
// The proposer sees the reason and can report again with a source.
func (s *Service) NeedsEvidence(ctx context.Context, vis visibility.Set, id int64, reason, by string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		c, err := s.openCard(ctx, tx, vis, id)
		if err != nil {
			return err
		}
		return s.closeCard(ctx, tx, c, "needs_evidence", reason, by)
	})
}

// errMembers means that the picked members are not two or more entities of
// the cluster.
var errMembers = errors.New("resolve: pick two or more members of the cluster")

// clusterOf returns the open merge cards of the cluster of a card, with the
// subject keys of each.
func (s *Service) clusterOf(ctx context.Context, tx pgx.Tx, vis visibility.Set, id int64) (Card, map[int64][]string, error) {
	rows, err := New(tx).VisibleCards(ctx, VisibleCardsParams{Visible: vis, Kind: ptr("merge"), Lim: maxQueue})
	if err != nil {
		return Card{}, nil, err
	}
	pairs := map[int64][]string{}
	cs := make([]Card, 0, len(rows))
	for _, r := range rows {
		pairs[r.ID] = r.SubjectRefs
		c := Card{ID: r.ID, Kind: r.Kind, refs: r.SubjectRefs}
		if r.Band != nil {
			c.Band = *r.Band
		}
		cs = append(cs, c)
	}
	for _, c := range Cluster(cs) {
		if slices.Contains(c.Pairs, id) {
			return c, pairs, nil
		}
	}
	return Card{}, nil, ErrNotFound
}

// acceptCluster merges the picked members of the cluster of a card, by
// default all, and accepts each pair card with both entities among them.
func (s *Service) acceptCluster(ctx context.Context, tx pgx.Tx, vis visibility.Set, id int64, members []string, canonical, by string) error {
	cl, pairs, err := s.clusterOf(ctx, tx, vis, id)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		members = cl.refs
	}
	if len(members) < 2 {
		return errMembers
	}
	for _, m := range members {
		if !slices.Contains(cl.refs, m) {
			return errMembers
		}
	}
	if _, _, err := s.Entities.MergeTx(ctx, tx, vis, entities.MergeRequest{Keys: members, Canonical: canonical, By: by}); err != nil {
		return err
	}
	for _, pid := range cl.Pairs {
		refs := pairs[pid]
		if len(refs) == 0 || slices.ContainsFunc(refs, func(r string) bool { return !slices.Contains(members, r) }) {
			continue
		}
		pc, err := s.openCard(ctx, tx, vis, pid)
		if err != nil {
			return err
		}
		if err := s.verdict(ctx, tx, refs, "same", by); err != nil {
			return err
		}
		if err := s.closeCard(ctx, tx, pc, "accepted", "", by); err != nil {
			return err
		}
	}
	return nil
}

// rejectCluster rejects every pair card of the cluster of a card and
// stores the verdict different for each pair (REQ-RES-006).
func (s *Service) rejectCluster(ctx context.Context, tx pgx.Tx, vis visibility.Set, id int64, reason, by string) error {
	cl, pairs, err := s.clusterOf(ctx, tx, vis, id)
	if err != nil {
		return err
	}
	for _, pid := range cl.Pairs {
		pc, err := s.openCard(ctx, tx, vis, pid)
		if err != nil {
			return err
		}
		if err := s.verdict(ctx, tx, pairs[pid], "different", by); err != nil {
			return err
		}
		if err := s.closeCard(ctx, tx, pc, "rejected", reason, by); err != nil {
			return err
		}
	}
	return nil
}

// NotSame stores the verdict that two entities are different and rejects
// the open merge cards of exactly these two (REQ-RES-006, REQ-REV-012).
func (s *Service) NotSame(ctx context.Context, vis visibility.Set, keys []string, reason, by string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		subs, err := s.subjectMap(ctx, vis, keys)
		if err != nil {
			return err
		}
		if len(keys) != 2 || keys[0] == keys[1] || len(subs) != 2 {
			return ErrNotFound
		}
		if err := s.verdict(ctx, tx, keys, "different", by); err != nil {
			return err
		}
		ids, err := New(tx).OpenMergeCards(ctx, keys)
		if err != nil {
			return err
		}
		for _, id := range ids {
			c, err := s.openCard(ctx, tx, vis, id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if len(c.SubjectRefs) != 2 {
				continue
			}
			if err := s.closeCard(ctx, tx, c, "rejected", reason, by); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "resolve.not_same", Target: "entity:" + keys[0],
			Details: map[string]any{"keys": keys}})
	})
}

func ptr[T any](v T) *T { return &v }

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

// filterBody is a card filter in a request body.
type filterBody struct {
	Kind       string `json:"kind,omitempty" enum:"correction,finding,merge,mention_link,unnamed,new_term,term_edit,conflict,needs_edit,"`
	Connection int64  `json:"connection,omitempty"`
	Type       string `json:"type,omitempty"`
	Domain     int64  `json:"domain,omitempty"`
	Q          string `json:"q,omitempty" maxLength:"200"`
}

// AcceptBand accepts each open card of the band of f that f selects, and
// returns the accepted card IDs (REQ-REV-011).
func (s *Service) AcceptBand(ctx context.Context, vis visibility.Set, f CardFilter, by string) ([]int64, error) {
	if f.Band == "" {
		return nil, errCardKind
	}
	cs, err := s.Cards(ctx, vis, f, maxQueue)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for _, c := range cs {
		err := s.Accept(ctx, vis, c.ID, Edits{}, by)
		switch {
		case err == nil:
			ids = append(ids, c.ID)
		case errors.Is(err, ErrCardDone), errors.Is(err, ErrNotFound):
		default:
			return ids, err
		}
	}
	return ids, nil
}

// Register adds the card, decision and calibration routes.
func (s *Service) Register(a huma.API) {
	reviewers := huma.Middlewares{api.Require(auth.RoleReviewer)}
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}
	tags := []string{"review"}

	type filterIn struct {
		Kind       string `query:"kind" enum:"correction,finding,merge,mention_link,unnamed,new_term,term_edit,conflict,needs_edit,"`
		Band       string `query:"band" enum:"high,medium,low,"`
		Connection int64  `query:"connection" doc:"A connection of the card inputs."`
		Type       string `query:"type" doc:"An entity type of a subject."`
		Domain     int64  `query:"domain" doc:"A domain of a subject."`
		Q          string `query:"q" maxLength:"200" doc:"Text in the subject names, aliases or the card."`
	}
	filter := func(f filterIn, decided bool) CardFilter {
		return CardFilter{Decided: decided, Kind: f.Kind, Band: f.Band, Connection: f.Connection, Type: f.Type, Domain: f.Domain, Query: f.Q}
	}
	type cardsIn struct {
		filterIn
		Status string `query:"status" enum:"open,decided" default:"open" doc:"open lists the queue in order; decided lists the newest decisions first."`
		Limit  int    `query:"limit" minimum:"1" maximum:"500" default:"200"`
	}
	type cardsOut struct {
		Body struct {
			Cards []Card `json:"cards"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listCards", Method: http.MethodGet, Path: "/api/v1/cards", Tags: tags,
		Summary: "List review cards", Description: "A card shows only when all its inputs are visible to you. Open cards come in queue order: impact multiplied by uncertainty.",
		Middlewares: reviewers,
	}, func(ctx context.Context, in *cardsIn) (*cardsOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		cs, err := s.Cards(ctx, vis, filter(in.filterIn, in.Status == "decided"), in.Limit)
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
		case errors.Is(err, errMembers):
			return api.Problem(http.StatusUnprocessableEntity, "members", "Pick two or more members of the cluster.")
		case errors.Is(err, errCardKind):
			return api.Problem(http.StatusUnprocessableEntity, "card_kind", "Decide this card on its own page.")
		}
		return api.Internal(err)
	}
	type acceptIn struct {
		ID   int64 `path:"id"`
		Body Edits
	}
	huma.Register(a, huma.Operation{
		OperationID: "acceptCard", Method: http.MethodPost, Path: "/api/v1/cards/{id}/accept", Tags: tags,
		Summary: "Accept a card", Description: "A merge card merges its entities. A link card adds the claim of its mention. An unnamed card makes its entity active. The canonical name is optional.",
		Middlewares: reviewers, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *acceptIn) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		return nil, cardErr(s.Accept(ctx, vis, in.ID, in.Body, api.Caller(ctx).User.ID))
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

	huma.Register(a, huma.Operation{
		OperationID: "cardNeedsEvidence", Method: http.MethodPost, Path: "/api/v1/cards/{id}/needs-evidence", Tags: tags,
		Summary: "Ask for evidence on a card", Description: "A reason is required. The proposer sees it with the result.",
		Middlewares: reviewers, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *rejectIn) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		return nil, cardErr(s.NeedsEvidence(ctx, vis, in.ID, in.Body.Reason, api.Caller(ctx).User.ID))
	})

	type bandIn struct {
		Body struct {
			Band string `json:"band" enum:"high,medium,low"`
			filterBody
		}
	}
	type bandOut struct {
		Body struct {
			Accepted []int64 `json:"accepted"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "acceptBand", Method: http.MethodPost, Path: "/api/v1/cards/accept-band", Tags: tags,
		Summary:     "Accept all open cards of one band in a filter",
		Description: "Accepts each open card of the band that the filter selects, with no edits. A cluster card merges all its members.",
		Middlewares: reviewers,
	}, func(ctx context.Context, in *bandIn) (*bandOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		b := in.Body
		f := CardFilter{Kind: b.Kind, Band: b.Band, Connection: b.Connection, Type: b.Type, Domain: b.Domain, Query: b.Q}
		ids, err := s.AcceptBand(ctx, vis, f, api.Caller(ctx).User.ID)
		if err != nil {
			return nil, cardErr(err)
		}
		out := &bandOut{}
		out.Body.Accepted = ids
		return out, nil
	})

	type notSameIn struct {
		Body struct {
			Keys   []string `json:"keys" minItems:"2" maxItems:"2"`
			Reason string   `json:"reason,omitempty" maxLength:"1000"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "notTheSame", Method: http.MethodPost, Path: "/api/v1/merges/not-same", Tags: tags,
		Summary:     "Store that two entities are not the same",
		Description: "Resolution does not suggest the pair again, and the open merge cards of the pair are rejected.",
		Middlewares: reviewers, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *notSameIn) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		reason := in.Body.Reason
		if reason == "" {
			reason = "Not the same."
		}
		return nil, cardErr(s.NotSame(ctx, vis, in.Body.Keys, reason, api.Caller(ctx).User.ID))
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
