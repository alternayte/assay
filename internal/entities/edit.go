package entities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/visibility"
)

// Edit operations.
const (
	EditRename      = "rename"
	EditAddAlias    = "add_alias"
	EditRemoveAlias = "remove_alias"
	EditType        = "type"
	EditDelete      = "delete"
)

// Edit is one change to an entity. A member's edit waits in an entity_edit
// card with this payload.
type Edit struct {
	Key     string `json:"key"`
	Op      string `json:"op" enum:"rename,add_alias,remove_alias,type,delete"`
	Name    string `json:"name,omitempty"`
	Alias   string `json:"alias,omitempty"`
	AliasID int64  `json:"aliasId,omitempty"`
	Type    string `json:"type,omitempty"`
}

// Edit errors.
var (
	// ErrRecord is an entity that a registry record holds. The registry
	// form edits it.
	ErrRecord = errors.New("entities: a registry record holds this entity")
	// ErrSourceOwned is an entity that a source system makes again on sync.
	ErrSourceOwned = errors.New("entities: a source system owns this entity")
	// ErrLastName is the removal of the last name of an entity.
	ErrLastName = errors.New("entities: the entity needs a name")
	// ErrBadEdit is an edit with a missing or wrong value.
	ErrBadEdit = errors.New("entities: the edit is not valid")
)

// TypeRefused is a type change that a current claim does not take.
type TypeRefused struct{ Predicates []string }

func (e *TypeRefused) Error() string {
	return "entities: the claims " + strings.Join(e.Predicates, ", ") + " do not take the type"
}

// fixedTypes are the types that an edit neither sets nor changes: terms
// belong to the glossary, repositories to the connectors and design pages,
// and contracts to the spec files and the code of their repository.
var fixedTypes = []string{"term", "repository", "operation", "event", "data_model"}

// unnamedPrefix is the key prefix of the entities that resolution makes
// from descriptions. No source system owns them.
const unnamedPrefix = "registry:unnamed:"

// errDryRun ends the check of a member's edit without a change.
var errDryRun = errors.New("entities: dry run")

// EditEntity applies the edit of a reviewer at once. The edit of a member
// is checked and becomes an entity_edit card, whose id it returns.
func (s *Service) EditEntity(ctx context.Context, vis visibility.Set, e Edit, by string, reviewer bool) (int64, error) {
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := s.ApplyEditTx(ctx, tx, vis, e, by); err != nil {
			return err
		}
		if !reviewer {
			return errDryRun
		}
		return nil
	})
	if reviewer || !errors.Is(err, errDryRun) {
		return 0, err
	}
	q := New(s.Pool)
	ent, err := q.VisibleEntityByKey(ctx, VisibleEntityByKeyParams{Key: e.Key, Visible: vis})
	if err != nil {
		return 0, err
	}
	members, err := q.Members(ctx, ent.ID)
	if err != nil {
		return 0, err
	}
	conns, err := q.VisibleSourceConnections(ctx, VisibleSourceConnectionsParams{Members: members, Visible: vis})
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	var id int64
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if id, err = New(tx).InsertEditCard(ctx, InsertEditCardParams{Target: "entity:" + ent.Key, Payload: payload, ProposerID: &by,
			Connections: conns}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "entities.edit_proposal", Target: "entity:" + ent.Key,
			Details: map[string]any{"proposal": id, "op": e.Op}, Connections: conns})
	})
	return id, err
}

// ApplyEdit accepts an entity_edit card for a reviewer.
func (s *Service) ApplyEdit(ctx context.Context, tx pgx.Tx, vis visibility.Set, payload []byte, reviewer string) error {
	var e Edit
	if err := json.Unmarshal(payload, &e); err != nil {
		return err
	}
	return s.ApplyEditTx(ctx, tx, vis, e, reviewer)
}

// ApplyEditTx applies an edit in tx. Names and aliases go into the visible
// source connections of the entity, never into the registry: a registry
// alias is visible to everyone and would show the entity to users who
// cannot see its sources.
func (s *Service) ApplyEditTx(ctx context.Context, tx pgx.Tx, vis visibility.Set, e Edit, by string) error {
	q := New(tx)
	ent, err := q.VisibleEntityByKey(ctx, VisibleEntityByKeyParams{Key: e.Key, Visible: vis})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	members, err := q.Members(ctx, ent.ID)
	if err != nil {
		return err
	}
	keys, err := q.MemberKeys(ctx, members)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(keys, isRecord) {
		return ErrRecord
	}
	conns, err := q.VisibleSourceConnections(ctx, VisibleSourceConnectionsParams{Members: members, Visible: vis})
	if err != nil {
		return err
	}
	switch e.Op {
	case EditRename, EditAddAlias:
		name := strings.TrimSpace(e.Name)
		if e.Op == EditAddAlias {
			name = strings.TrimSpace(e.Alias)
		}
		norm := NormalizeAlias(name)
		if norm == "" || len([]rune(name)) > 200 || len(conns) == 0 {
			return ErrBadEdit
		}
		if e.Op == EditRename {
			if err := q.DropEditedNames(ctx, DropEditedNamesParams{EntityID: ent.ID, Visible: vis}); err != nil {
				return err
			}
		}
		if err := q.AddEditedAlias(ctx, AddEditedAliasParams{EntityID: ent.ID, Alias: name, AliasNorm: norm, IsName: e.Op == EditRename,
			Members: members, Visible: vis}); err != nil {
			return err
		}
		if err := q.Unsuppress(ctx, UnsuppressParams{Members: members, AliasNorm: norm}); err != nil {
			return err
		}
	case EditRemoveAlias:
		a, err := q.VisibleAlias(ctx, VisibleAliasParams{ID: e.AliasID, Members: members, Visible: vis})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := q.DropAlias(ctx, DropAliasParams{EntityID: a.EntityID, AliasNorm: a.AliasNorm, Visible: vis}); err != nil {
			return err
		}
		if err := q.Suppress(ctx, SuppressParams{EntityID: a.EntityID, AliasNorm: a.AliasNorm, By: &by}); err != nil {
			return err
		}
		n, err := q.VisibleNames(ctx, VisibleNamesParams{Members: members, Visible: vis})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrLastName
		}
	case EditType:
		if !slices.Contains(claims.EntityTypes, e.Type) || slices.Contains(fixedTypes, e.Type) || slices.Contains(fixedTypes, ent.Type) {
			return ErrBadEdit
		}
		bad, err := s.refusingPredicates(ctx, q, members, e.Type)
		if err != nil {
			return err
		}
		if len(bad) > 0 {
			return &TypeRefused{Predicates: bad}
		}
		if err := q.SetEditedType(ctx, SetEditedTypeParams{ID: ent.ID, Type: e.Type}); err != nil {
			return err
		}
	case EditDelete:
		if !slices.ContainsFunc(keys, func(k string) bool { return strings.HasPrefix(k, unnamedPrefix) }) ||
			slices.ContainsFunc(keys, func(k string) bool { return !strings.HasPrefix(k, unnamedPrefix) }) {
			return ErrSourceOwned
		}
		targets := make([]string, len(keys))
		for i, k := range keys {
			targets[i] = "entity:" + k
			if err := q.InsertDeletion(ctx, InsertDeletionParams{Key: k, By: &by}); err != nil {
				return err
			}
		}
		if err := q.CloseEntityCards(ctx, CloseEntityCardsParams{Reason: "The entity was deleted.", Targets: targets, Keys: keys}); err != nil {
			return err
		}
		if err := q.DeleteEntities(ctx, members); err != nil {
			return err
		}
	default:
		return ErrBadEdit
	}
	if e.Op != EditDelete && s.Edited != nil {
		if err := s.Edited(ctx, tx, ent.ID); err != nil {
			return err
		}
	}
	return audit.Write(ctx, tx, audit.Entry{Action: "entities.edit", Target: "entity:" + ent.Key,
		Details: map[string]any{"op": e.Op, "name": e.Name, "alias": e.Alias, "aliasId": e.AliasID, "type": e.Type}, Connections: conns})
}

// isRecord reports a key of a registry record. An unnamed entity has a
// registry key and no record.
func isRecord(key string) bool {
	return strings.HasPrefix(key, "registry:") && !strings.HasPrefix(key, unnamedPrefix)
}

// refusingPredicates are the predicates of the current claims of the
// entities that do not take the type on the side that the entities fill.
func (s *Service) refusingPredicates(ctx context.Context, q *Queries, members []int64, typ string) ([]string, error) {
	roles, err := q.CurrentRoles(ctx, members)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, r := range roles {
		p, ok := claims.Registry[r.Predicate]
		if !ok {
			continue
		}
		allowed := p.Objects
		if r.Subject {
			allowed = p.Subjects
		}
		if !slices.Contains(allowed, claims.ObjAny) && !slices.Contains(allowed, typ) && !slices.Contains(bad, r.Predicate) {
			bad = append(bad, r.Predicate)
		}
	}
	return bad, nil
}

// AllowedTypes are the types that a type edit of the entities can set.
func (s *Service) AllowedTypes(ctx context.Context, members []int64, current string) ([]string, error) {
	if slices.Contains(fixedTypes, current) {
		return []string{}, nil
	}
	out := []string{}
	for _, t := range claims.EntityTypes {
		if slices.Contains(fixedTypes, t) {
			continue
		}
		bad, err := s.refusingPredicates(ctx, New(s.Pool), members, t)
		if err != nil {
			return nil, err
		}
		if len(bad) == 0 {
			out = append(out, t)
		}
	}
	return out, nil
}

// editError is the message of an edit error for the API.
func editError(err error) (status int, code, msg string) {
	var refused *TypeRefused
	switch {
	case errors.Is(err, ErrRecord):
		return 409, "registry_record", "A registry record holds this entity. Edit the record in the registry."
	case errors.Is(err, ErrSourceOwned):
		return 409, "source_owned", "A source system owns this entity."
	case errors.Is(err, ErrLastName):
		return 409, "last_name", "The entity needs a name. Rename it instead."
	case errors.Is(err, ErrBadEdit):
		return 422, "bad_edit", "The edit has a missing or wrong value."
	case errors.As(err, &refused):
		return 409, "type_refused", fmt.Sprintf("These claims do not take the type: %s.", strings.Join(refused.Predicates, ", "))
	}
	return 0, "", ""
}
