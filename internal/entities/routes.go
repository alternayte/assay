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
		err := s.Split(ctx, in.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.ErrNotFound
		case errors.Is(err, ErrMergeUndone):
			return nil, api.Problem(http.StatusConflict, "already_split", "The merge is already split.")
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
}
