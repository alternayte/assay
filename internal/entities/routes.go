package entities

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/visibility"
)

// keyIn is an entity key in the path. Keys hold slashes, so clients escape
// the key as one path segment.
type keyIn struct {
	Key string `path:"key" doc:"The entity key, escaped as one path segment, such as jira:jira.acme.test:PAY-1."`
}

func (k keyIn) key() string {
	if u, err := url.PathUnescape(k.Key); err == nil {
		return u
	}
	return k.Key
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return api.ErrNotFound
	}
	return api.Internal(err)
}

// Register adds the entity, trace, merge and resolution routes.
func (s *Service) Register(a huma.API) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	reviewers := huma.Middlewares{api.Require(auth.RoleReviewer)}
	tags := []string{"entities"}

	type listIn struct {
		Type     string `query:"type" doc:"An entity type."`
		DomainID int64  `query:"domain"`
		Status   string `query:"status" enum:"active,proposed,"`
		Q        string `query:"q" maxLength:"200" doc:"Text in a name or an alias."`
		Limit    int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
	}
	type listOut struct {
		Body struct {
			Entities []Summary `json:"entities"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listEntities", Method: http.MethodGet, Path: "/api/v1/entities", Tags: tags,
		Summary: "List the visible entities", Description: "Newest change first. An entity is visible when it has a visible source.",
		Middlewares: members,
	}, func(ctx context.Context, in *listIn) (*listOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		es, err := s.Explore(ctx, vis, Filter{Type: in.Type, DomainID: in.DomainID, Status: in.Status, Query: in.Q, Limit: in.Limit})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOut{}
		out.Body.Entities = es
		return out, nil
	})

	type oneOut struct{ Body Detail }
	huma.Register(a, huma.Operation{
		OperationID: "getEntity", Method: http.MethodGet, Path: "/api/v1/entities/{key}", Tags: tags,
		Summary:     "Get an entity with its claims",
		Description: "Claims carry their status for you. A merged key gives the winner. A hidden entity and a missing key give the same 404.",
		Middlewares: members,
	}, func(ctx context.Context, in *keyIn) (*oneOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		d, err := s.Get(ctx, vis, in.key())
		if err != nil {
			return nil, notFound(err)
		}
		return &oneOut{Body: *d}, nil
	})

	type traceIn struct {
		Key        string `path:"key" doc:"The entity key, escaped as one path segment."`
		Depth      int    `query:"depth" default:"3" doc:"1 to 3."`
		Direction  string `query:"direction" enum:"out,in,both" default:"both"`
		Predicates string `query:"predicates" doc:"Comma-separated predicates. Empty follows all."`
	}
	type traceOut struct{ Body Trace }
	huma.Register(a, huma.Operation{
		OperationID: "traceEntity", Method: http.MethodGet, Path: "/api/v1/entities/{key}/trace", Tags: tags,
		Summary:     "Trace the relationships of an entity",
		Description: "Follows visible entity-valued claims to depth 3 and at most 200 edges. truncated is set when the limit cuts the result.",
		Middlewares: members,
	}, func(ctx context.Context, in *traceIn) (*traceOut, error) {
		if in.Depth < 1 || in.Depth > MaxDepth {
			return nil, api.Problem(http.StatusBadRequest, "bad_depth", "The depth must be 1, 2 or 3.")
		}
		var preds []string
		for _, p := range strings.Split(in.Predicates, ",") {
			if p = strings.TrimSpace(p); p != "" {
				if _, ok := claims.Registry[p]; !ok {
					return nil, api.Problem(http.StatusBadRequest, "bad_predicate", "The predicate "+p+" does not exist.")
				}
				preds = append(preds, p)
			}
		}
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		t, err := s.Trace(ctx, vis, keyIn{Key: in.Key}.key(), preds, in.Depth, in.Direction)
		if err != nil {
			return nil, notFound(err)
		}
		return &traceOut{Body: *t}, nil
	})

	type predicate struct {
		Name            string   `json:"name"`
		Subjects        []string `json:"subjects"`
		Objects         []string `json:"objects"`
		Multi           bool     `json:"multi"`
		TimeScoped      bool     `json:"timeScoped"`
		DefaultPriority []string `json:"defaultPriority"`
	}
	type predicatesOut struct {
		Body struct {
			Predicates  []predicate `json:"predicates"`
			EntityTypes []string    `json:"entityTypes"`
			SourceTypes []string    `json:"sourceTypes"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listPredicates", Method: http.MethodGet, Path: "/api/v1/predicates", Tags: tags,
		Summary: "List the predicate registry", Description: "The registry is code. Source priority per predicate is a setting.",
		Middlewares: members,
	}, func(context.Context, *struct{}) (*predicatesOut, error) {
		out := &predicatesOut{}
		names := make([]string, 0, len(claims.Registry))
		for n := range claims.Registry {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			p := claims.Registry[n]
			out.Body.Predicates = append(out.Body.Predicates, predicate{Name: n, Subjects: p.Subjects, Objects: p.Objects, Multi: p.Multi,
				TimeScoped: p.TimeScoped, DefaultPriority: p.Priority})
		}
		out.Body.EntityTypes = claims.EntityTypes
		out.Body.SourceTypes = claims.SourceTypes
		return out, nil
	})

	type mergeIn struct {
		Body struct {
			Keys          []string `json:"keys" minItems:"2" maxItems:"20"`
			CanonicalName string   `json:"canonicalName,omitempty" maxLength:"200"`
		}
	}
	type mergeOut struct {
		Status int
		Body   struct {
			ID       int64  `json:"id,omitempty"`
			Winner   string `json:"winner,omitempty" doc:"The key of the winner."`
			Proposal int64  `json:"proposal,omitempty" doc:"The merge card of a member."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "mergeEntities", Method: http.MethodPost, Path: "/api/v1/merges", Tags: tags,
		Summary: "Merge entities",
		Description: "A reviewer merges at once (201): the registry entity wins, else the oldest, and the losers keep their aliases. " +
			"A member creates a merge card for review (202).",
		Middlewares: members, DefaultStatus: http.StatusCreated,
		Responses: map[string]*huma.Response{"202": {Description: "A merge card for review."}},
	}, func(ctx context.Context, in *mergeIn) (*mergeOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &mergeOut{Status: http.StatusCreated}
		var id, proposal int64
		var winner string
		if api.AtLeast(ctx, auth.RoleReviewer) {
			id, winner, err = s.Merge(ctx, vis, MergeRequest{Keys: in.Body.Keys, Canonical: in.Body.CanonicalName, By: api.Caller(ctx).User.ID})
		} else {
			out.Status = http.StatusAccepted
			proposal, err = s.ProposeMerge(ctx, vis, in.Body.Keys, api.Caller(ctx).User.ID)
		}
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.ErrNotFound
		case err != nil && strings.HasPrefix(err.Error(), "entities: "):
			return nil, api.Problem(http.StatusUnprocessableEntity, "bad_merge", "Select two or more different entities.")
		case err != nil:
			return nil, api.Internal(err)
		}
		out.Body.ID, out.Body.Winner, out.Body.Proposal = id, winner, proposal
		return out, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "splitMerge", Method: http.MethodPost, Path: "/api/v1/merges/{id}/split", Tags: tags,
		Summary: "Split a merge", Description: "The merged entities become separate again.",
		Middlewares: reviewers, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		err = s.Split(ctx, vis, in.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.ErrNotFound
		case errors.Is(err, ErrMergeUndone):
			return nil, api.Problem(http.StatusConflict, "already_split", "The merge is already split.")
		case errors.Is(err, ErrMergeChanged):
			return nil, api.Problem(http.StatusConflict, "merge_changed", "A later merge includes these entities. Split the later merge first.")
		case err != nil:
			return nil, api.Internal(err)
		}
		return nil, nil
	})

	type resolveOut struct {
		Body struct {
			ID int64 `json:"id"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "resolveClaim", Method: http.MethodPost, Path: "/api/v1/claims/{id}/resolution", Tags: tags,
		Summary:     "Choose a claim as the winner of its conflict",
		Description: "The other claims of the subject, predicate and scope become Superseded. New support for a loser reopens the resolution.",
		Middlewares: reviewers, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*resolveOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &resolveOut{}
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			out.Body.ID, err = claims.Resolve(ctx, tx, vis, in.ID, api.Caller(ctx).User.ID, s.now())
			return err
		})
		if errors.Is(err, claims.ErrNotVisible) {
			return nil, api.ErrNotFound
		}
		if err != nil {
			return nil, api.Internal(err)
		}
		return out, nil
	})

	s.registerEdits(a, tags, members)
}

// editOut is the answer of an edit: 204 for a reviewer, who applies it,
// and 202 with the card for a member.
type editOut struct {
	Status int
	Body   struct {
		Proposal int64 `json:"proposal,omitempty" doc:"The entity_edit card of a member."`
	}
}

// registerEdits adds the routes that edit an entity from its page.
func (s *Service) registerEdits(a huma.API, tags []string, members huma.Middlewares) {
	edit := func(ctx context.Context, e Edit) (*editOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		reviewer := api.AtLeast(ctx, auth.RoleReviewer)
		id, err := s.EditEntity(ctx, vis, e, api.Caller(ctx).User.ID, reviewer)
		if status, code, msg := editError(err); status != 0 {
			return nil, api.Problem(status, code, msg)
		}
		if err != nil {
			return nil, notFound(err)
		}
		out := &editOut{Status: http.StatusNoContent}
		if !reviewer {
			out.Status = http.StatusAccepted
			out.Body.Proposal = id
		}
		return out, nil
	}
	responses := map[string]*huma.Response{"202": {Description: "An entity_edit card for review."}}
	desc := " A reviewer's edit applies at once (204). A member's edit becomes an entity_edit card (202)."

	type nameIn struct {
		Key  string `path:"key" doc:"The entity key, escaped as one path segment."`
		Body struct {
			Name string `json:"name" minLength:"1" maxLength:"200"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "renameEntity", Method: http.MethodPut, Path: "/api/v1/entities/{key}/name", Tags: tags,
		Summary:     "Rename an entity",
		Description: "Writes the name in each visible source connection of the entity. A sync does not change it." + desc,
		Middlewares: members, DefaultStatus: http.StatusNoContent, Responses: responses,
	}, func(ctx context.Context, in *nameIn) (*editOut, error) {
		return edit(ctx, Edit{Key: keyIn{Key: in.Key}.key(), Op: EditRename, Name: in.Body.Name})
	})

	type aliasIn struct {
		Key  string `path:"key" doc:"The entity key, escaped as one path segment."`
		Body struct {
			Alias string `json:"alias" minLength:"1" maxLength:"200"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "addEntityAlias", Method: http.MethodPost, Path: "/api/v1/entities/{key}/aliases", Tags: tags,
		Summary:     "Add an alias to an entity",
		Description: "Writes the alias in each visible source connection of the entity." + desc,
		Middlewares: members, DefaultStatus: http.StatusNoContent, Responses: responses,
	}, func(ctx context.Context, in *aliasIn) (*editOut, error) {
		return edit(ctx, Edit{Key: keyIn{Key: in.Key}.key(), Op: EditAddAlias, Alias: in.Body.Alias})
	})

	type removeIn struct {
		Key     string `path:"key" doc:"The entity key, escaped as one path segment."`
		AliasID int64  `path:"aliasId"`
	}
	huma.Register(a, huma.Operation{
		OperationID: "removeEntityAlias", Method: http.MethodDelete, Path: "/api/v1/entities/{key}/aliases/{aliasId}", Tags: tags,
		Summary: "Remove an alias from an entity",
		Description: "Removes the alias from the visible connections. A sync or a mention does not add it again. " +
			"The last name cannot be removed; rename the entity instead." + desc,
		Middlewares: members, DefaultStatus: http.StatusNoContent, Responses: responses,
	}, func(ctx context.Context, in *removeIn) (*editOut, error) {
		return edit(ctx, Edit{Key: keyIn{Key: in.Key}.key(), Op: EditRemoveAlias, AliasID: in.AliasID})
	})

	type typeIn struct {
		Key  string `path:"key" doc:"The entity key, escaped as one path segment."`
		Body struct {
			Type string `json:"type" doc:"An entity type. term and repository cannot be set or changed."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "setEntityType", Method: http.MethodPut, Path: "/api/v1/entities/{key}/type", Tags: tags,
		Summary: "Change the type of an entity",
		Description: "A sync does not change the type again. A current claim that does not take the type refuses the change with 409, " +
			"and the message names its predicates." + desc,
		Middlewares: members, DefaultStatus: http.StatusNoContent, Responses: responses,
	}, func(ctx context.Context, in *typeIn) (*editOut, error) {
		return edit(ctx, Edit{Key: keyIn{Key: in.Key}.key(), Op: EditType, Type: in.Body.Type})
	})

	huma.Register(a, huma.Operation{
		OperationID: "deleteEntity", Method: http.MethodDelete, Path: "/api/v1/entities/{key}", Tags: tags,
		Summary: "Delete an entity",
		Description: "Deletes an entity that no source system owns, such as an unnamed entity, with its claims. " +
			"Resolution does not make it again. Any other entity returns 409." + desc,
		Middlewares: members, DefaultStatus: http.StatusNoContent, Responses: responses,
	}, func(ctx context.Context, in *keyIn) (*editOut, error) {
		return edit(ctx, Edit{Key: in.key(), Op: EditDelete})
	})
}
