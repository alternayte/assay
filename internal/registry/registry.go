// Package registry holds the registry: the built-in connection where
// members write records for things and terms (SDD section 13). Each save is
// a version with the editor. Fields become passages and claims with the
// origin human.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/visibility"
)

// Record kinds.
const (
	KindThing = "thing"
	KindTerm  = "term"
)

// TermRecord statuses.
const (
	StatusDraft      = "draft"
	StatusApproved   = "approved"
	StatusDeprecated = "deprecated"
)

// RecordRelation is a claim of a thing about another entity.
type RecordRelation struct {
	Predicate string `json:"predicate" minLength:"1"`
	Object    string `json:"object" minLength:"1" doc:"The key of a visible entity."`
}

// ThingRecord is a registry record of a thing.
type ThingRecord struct {
	Type        string           `json:"type" doc:"An entity type other than term."`
	Name        string           `json:"name" minLength:"1" maxLength:"200"`
	Aliases     []string         `json:"aliases,omitempty" maxItems:"50"`
	Description string           `json:"description,omitempty" maxLength:"4000"`
	Owner       string           `json:"owner,omitempty" doc:"The key of a team or person."`
	Relations   []RecordRelation `json:"relations,omitempty" maxItems:"50"`
	Links       []string         `json:"links,omitempty" maxItems:"20"`
}

// TermRecord is a registry record of a term.
type TermRecord struct {
	Name       string   `json:"name" minLength:"1" maxLength:"200"`
	Synonyms   []string `json:"synonyms,omitempty" maxItems:"50"`
	Definition string   `json:"definition,omitempty" maxLength:"4000"`
	DomainID   int64    `json:"domainId,omitempty"`
	Owner      string   `json:"owner,omitempty" doc:"The key of a team or person."`
	Preferred  string   `json:"preferred,omitempty" doc:"The key of the preferred term."`
	Related    []string `json:"related,omitempty" maxItems:"50" doc:"Keys of related entities."`
	Status     string   `json:"status,omitempty" enum:"draft,approved,deprecated," doc:"Only reviewers approve and deprecate."`
}

// RegistryRecord is a thing or a term.
type RegistryRecord struct {
	Kind  string       `json:"kind" enum:"thing,term"`
	Thing *ThingRecord `json:"thing,omitempty"`
	Term  *TermRecord  `json:"term,omitempty"`
}

func recordName(r RegistryRecord) string {
	if r.Kind == KindTerm && r.Term != nil {
		return r.Term.Name
	}
	if r.Thing != nil {
		return r.Thing.Name
	}
	return ""
}

func recordType(r RegistryRecord) string {
	if r.Kind == KindTerm {
		return "term"
	}
	return r.Thing.Type
}

// RecordKey is the entity key of a new record: registry:<type>:<slug of name>.
func RecordKey(r RegistryRecord) string {
	return "registry:" + recordType(r) + ":" + strings.ReplaceAll(entities.NormalizeAlias(recordName(r)), " ", "-")
}

// Service saves and reads registry records.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Errors of a save.
var (
	ErrExists     = errors.New("registry: a record with this type and name exists")
	ErrNotFound   = errors.New("registry: the record does not exist")
	ErrStatusRole = errors.New("registry: only reviewers approve and deprecate terms")
)

// InvalidError is a record that fails validation.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return "registry: " + e.Msg }

func invalid(format string, a ...any) error { return &InvalidError{Msg: fmt.Sprintf(format, a...)} }

// Editor is who saves a record.
type Editor struct {
	UserID string
	Name   string
	Role   string
	Vis    visibility.Set
}

// SavedRecord is the result of a save. Proposal is set when a member edit of an
// approved definition became a proposal instead of a version.
type SavedRecord struct {
	Key      string `json:"key"`
	Version  int64  `json:"version,omitempty"`
	Proposal int64  `json:"proposal,omitempty"`
}

// Save creates a record when key is empty, else saves a new version of the
// record with that key.
func (s *Service) Save(ctx context.Context, key string, rec RegistryRecord, ed Editor) (*SavedRecord, error) {
	var out *SavedRecord
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.SaveTx(ctx, tx, key, rec, ed)
		return err
	})
	return out, err
}

// SaveTx is Save in the transaction tx.
func (s *Service) SaveTx(ctx context.Context, tx pgx.Tx, key string, rec RegistryRecord, ed Editor) (*SavedRecord, error) {
	if err := normalize(&rec); err != nil {
		return nil, err
	}
	q := New(tx)
	reg, err := q.RegistryID(ctx)
	if err != nil {
		return nil, err
	}
	regSet := []int64{reg}
	create := key == ""
	if create {
		key = RecordKey(rec)
	}
	item, err := q.RecordItem(ctx, RecordItemParams{Visible: regSet, Key: key})
	switch {
	case errors.Is(err, pgx.ErrNoRows) && create:
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrNotFound
	case err != nil:
		return nil, err
	case create:
		return nil, ErrExists
	case item.Kind != rec.Kind:
		return nil, invalid("the record is a %s", item.Kind)
	}
	var old *RegistryRecord
	if !create && item.VersionID != nil {
		v, err := q.RecordVersion(ctx, RecordVersionParams{ItemID: item.ID, ID: *item.VersionID})
		if err != nil {
			return nil, err
		}
		old, err = recordOf(v.Metadata)
		if err != nil {
			return nil, err
		}
	}
	if rec.Kind == KindTerm {
		if p, err := s.termRules(ctx, tx, key, &rec, old, ed, reg); err != nil || p != 0 {
			if err != nil {
				return nil, err
			}
			return &SavedRecord{Key: key, Proposal: p}, nil
		}
	}
	everyone, err := q.EveryoneConnections(ctx)
	if err != nil {
		return nil, err
	}
	refs, err := s.refs(ctx, tx, rec, ed.Vis, everyone)
	if err != nil {
		return nil, err
	}
	it, err := build(key, rec, refs)
	if err != nil {
		return nil, err
	}
	it.EditedBy, it.Updated, it.Origin, it.DropStale = ed.Name, s.now(), "human", true
	if create {
		it.Created = it.Updated
	}
	if item.EntityID != nil {
		if err := q.ClearRecordAliases(ctx, ClearRecordAliasesParams{EntityID: *item.EntityID, Visible: regSet}); err != nil {
			return nil, err
		}
	}
	if _, err := content.Store(ctx, tx, reg, nil, it, s.Log); err != nil {
		return nil, err
	}
	saved, err := q.RecordItem(ctx, RecordItemParams{Visible: regSet, Key: key})
	if err != nil {
		return nil, err
	}
	if rec.Kind == KindTerm && saved.EntityID != nil {
		var domain *int64
		if rec.Term.DomainID != 0 {
			domain = &rec.Term.DomainID
		}
		if err := q.SetEntityDomain(ctx, SetEntityDomainParams{ID: *saved.EntityID, DomainID: domain}); err != nil {
			return nil, err
		}
	}
	out := &SavedRecord{Key: key}
	if saved.VersionID != nil {
		out.Version = *saved.VersionID
	}
	return out, audit.Write(ctx, tx, audit.Entry{Action: "registry.save", Target: "entity:" + key,
		Details: map[string]any{"kind": rec.Kind, "version": out.Version, "created": create}})
}

// termRules applies the term status rules (REQ-REG-003). A member creates
// drafts and keeps the status of an existing term. A member edit of an
// approved definition becomes a term_edit proposal.
func (s *Service) termRules(ctx context.Context, tx pgx.Tx, key string, rec *RegistryRecord, old *RegistryRecord, ed Editor, reg int64) (int64, error) {
	reviewer := ed.Role == auth.RoleReviewer || ed.Role == auth.RoleAdmin
	t := rec.Term
	switch {
	case old == nil && t.Status == "":
		t.Status = StatusDraft
	case old != nil && t.Status == "":
		t.Status = old.Term.Status
	}
	if reviewer {
		return 0, nil
	}
	if (old == nil && t.Status != StatusDraft) || (old != nil && t.Status != old.Term.Status) {
		return 0, ErrStatusRole
	}
	if old == nil || old.Term.Status != StatusApproved || old.Term.Definition == t.Definition {
		return 0, nil
	}
	payload, err := json.Marshal(map[string]any{"key": key, "record": rec, "old": old})
	if err != nil {
		return 0, err
	}
	var proposer *string
	if ed.UserID != "" {
		proposer = &ed.UserID
	}
	id, err := New(tx).InsertTermEdit(ctx, InsertTermEditParams{Target: "entity:" + key, Payload: payload, ProposerID: proposer, Connections: []int64{reg}})
	if err != nil {
		return 0, err
	}
	return id, audit.Write(ctx, tx, audit.Entry{Action: "registry.propose", Target: "entity:" + key, Details: map[string]any{"proposal": id}})
}

func normalize(r *RegistryRecord) error {
	clean := func(ss []string) []string {
		var out []string
		for _, s := range ss {
			if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		return out
	}
	switch r.Kind {
	case KindThing:
		if r.Thing == nil || r.Term != nil {
			return invalid("a thing record needs the thing fields")
		}
		t := r.Thing
		t.Name, t.Description, t.Owner = strings.TrimSpace(t.Name), strings.TrimSpace(t.Description), strings.TrimSpace(t.Owner)
		t.Aliases, t.Links = clean(t.Aliases), clean(t.Links)
		if t.Type == "term" || !slices.Contains(claims.EntityTypes, t.Type) {
			return invalid("the type %q is not a thing type", t.Type)
		}
		for _, l := range t.Links {
			if !strings.HasPrefix(l, "https://") && !strings.HasPrefix(l, "http://") {
				return invalid("the link %q is not an http or https URL", l)
			}
		}
	case KindTerm:
		if r.Term == nil || r.Thing != nil {
			return invalid("a term record needs the term fields")
		}
		t := r.Term
		t.Name, t.Definition, t.Owner, t.Preferred = strings.TrimSpace(t.Name), strings.TrimSpace(t.Definition), strings.TrimSpace(t.Owner),
			strings.TrimSpace(t.Preferred)
		t.Synonyms, t.Related = clean(t.Synonyms), clean(t.Related)
	default:
		return invalid("unknown record kind %q", r.Kind)
	}
	if entities.NormalizeAlias(recordName(*r)) == "" {
		return invalid("the name has no letters or digits")
	}
	return nil
}

func recordOf(meta []byte) (*RegistryRecord, error) {
	var m struct {
		RegistryRecord *RegistryRecord `json:"record"`
	}
	if err := json.Unmarshal(meta, &m); err != nil || m.RegistryRecord == nil {
		return nil, fmt.Errorf("registry: the version has no record")
	}
	return m.RegistryRecord, nil
}

type ref struct {
	key, typ, name string
}

// refs loads the entities that the record names. Each must be visible to
// the editor. Names come from the visible set of Everyone, because every
// member reads the registry.
func (s *Service) refs(ctx context.Context, tx pgx.Tx, rec RegistryRecord, vis visibility.Set, everyone []int64) (map[string]ref, error) {
	var keys []string
	if rec.Kind == KindThing {
		if rec.Thing.Owner != "" {
			keys = append(keys, rec.Thing.Owner)
		}
		for _, r := range rec.Thing.Relations {
			keys = append(keys, r.Object)
		}
	} else {
		t := rec.Term
		for _, k := range append([]string{t.Owner, t.Preferred}, t.Related...) {
			if k != "" {
				keys = append(keys, k)
			}
		}
	}
	out := map[string]ref{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := New(tx).VisibleRefs(ctx, VisibleRefsParams{Keys: keys, Visible: vis, Everyone: everyone})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Key] = ref{key: r.Key, typ: r.Type, name: r.Name}
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			return nil, invalid("the entity %s does not exist", k)
		}
	}
	return out, nil
}

func label(predicate string) string {
	s := strings.ReplaceAll(predicate, "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// build turns a record into an item: one paragraph per field, and a fact
// for each field that the predicate registry takes.
func build(key string, rec RegistryRecord, refs map[string]ref) (*content.Item, error) {
	it := &content.Item{ExternalID: key, Kind: rec.Kind, Title: recordName(rec), URL: "/registry/" + key,
		Metadata: map[string]any{"record": rec}}
	subject := content.EntityRef{Type: recordType(rec), Key: key, Name: recordName(rec)}
	var paras []string
	add := func(text string) string {
		text = oneLine(text)
		paras = append(paras, text)
		return text
	}
	fact := func(pred, quote string, obj string, text string) error {
		f := content.Fact{Subject: subject, Predicate: pred, Quote: quote, Text: text}
		if obj != "" {
			r := refs[obj]
			f.Object = &content.EntityRef{Type: r.typ, Key: r.key}
			if err := claims.Check(pred, subject.Type, r.typ); err != nil {
				return invalid("%s", err.Error())
			}
		}
		it.Facts = append(it.Facts, f)
		return nil
	}
	add("Name: " + recordName(rec))
	if rec.Kind == KindThing {
		t := rec.Thing
		subject.Aliases = t.Aliases
		add("Type: " + t.Type)
		if len(t.Aliases) > 0 {
			add("Aliases: " + strings.Join(t.Aliases, ", "))
		}
		if t.Description != "" {
			q := add("Description: " + t.Description)
			if err := fact("stated", q, "", t.Description); err != nil {
				return nil, err
			}
		}
		if t.Owner != "" {
			if err := fact("owned_by", add("Owner: "+refs[t.Owner].name), t.Owner, ""); err != nil {
				return nil, err
			}
		}
		for _, r := range t.Relations {
			if _, ok := claims.Registry[r.Predicate]; !ok {
				return nil, invalid("unknown predicate %s", r.Predicate)
			}
			if err := fact(r.Predicate, add(label(r.Predicate)+": "+refs[r.Object].name), r.Object, ""); err != nil {
				return nil, err
			}
		}
		for _, l := range t.Links {
			add("Link: " + l)
		}
	} else {
		t := rec.Term
		subject.Aliases = t.Synonyms
		if len(t.Synonyms) > 0 {
			add("Synonyms: " + strings.Join(t.Synonyms, ", "))
		}
		if t.Definition != "" {
			if err := fact("defined_as", add("Definition: "+t.Definition), "", t.Definition); err != nil {
				return nil, err
			}
		}
		if err := fact("status", add("Status: "+t.Status), "", t.Status); err != nil {
			return nil, err
		}
		if t.Owner != "" {
			if err := fact("owned_by", add("Owner: "+refs[t.Owner].name), t.Owner, ""); err != nil {
				return nil, err
			}
		}
		if t.Preferred != "" {
			if err := fact("preferred_term", add("Preferred term: "+refs[t.Preferred].name), t.Preferred, ""); err != nil {
				return nil, err
			}
		}
		for _, r := range t.Related {
			if err := fact("relates_to", add("Related: "+refs[r].name), r, ""); err != nil {
				return nil, err
			}
		}
	}
	it.Entity = &subject
	it.Body = strings.Join(paras, "\n\n")
	return it, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ApplyTermEdit saves the record of an accepted term_edit proposal as a
// new version by the proposer.
func (s *Service) ApplyTermEdit(ctx context.Context, tx pgx.Tx, vis visibility.Set, payload []byte, proposerID, reviewerID string) error {
	var p struct {
		Key    string          `json:"key"`
		Record *RegistryRecord `json:"record"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.Record == nil {
		return fmt.Errorf("registry: the term edit has no record")
	}
	userID := proposerID
	if userID == "" {
		userID = reviewerID
	}
	name, err := New(tx).UserName(ctx, userID)
	if err != nil {
		return err
	}
	// The reviewer accepted the edit, so the review rules apply.
	_, err = s.SaveTx(ctx, tx, p.Key, *p.Record, Editor{UserID: userID, Name: name, Role: auth.RoleReviewer, Vis: vis})
	return err
}

// Rename saves a new version of a record with another name, by the user.
func (s *Service) Rename(ctx context.Context, tx pgx.Tx, vis visibility.Set, key, name, userID string) error {
	q := New(tx)
	reg, err := q.RegistryID(ctx)
	if err != nil {
		return err
	}
	item, err := q.RecordItem(ctx, RecordItemParams{Visible: []int64{reg}, Key: key})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && item.VersionID == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	v, err := q.RecordVersion(ctx, RecordVersionParams{ItemID: item.ID, ID: *item.VersionID})
	if err != nil {
		return err
	}
	rec, err := recordOf(v.Metadata)
	if err != nil {
		return err
	}
	if rec.Kind == KindTerm {
		rec.Term.Name = name
	} else {
		rec.Thing.Name = name
	}
	editor := ""
	if userID != "" {
		if editor, err = q.UserName(ctx, userID); err != nil {
			return err
		}
	}
	_, err = s.SaveTx(ctx, tx, key, *rec, Editor{UserID: userID, Name: editor, Role: auth.RoleReviewer, Vis: vis})
	return err
}
