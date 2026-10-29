package search

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/visibility"
)

// Register adds the search route.
func (s *Service) Register(a huma.API) {
	type in struct {
		Q            string    `query:"q" maxLength:"500" doc:"The query. Quotes match a phrase."`
		SourceType   string    `query:"sourceType" enum:"registry,confluence,jira,github,azdo," doc:"The kind of the source connection."`
		ConnectionID int64     `query:"connection"`
		EntityType   string    `query:"entityType" doc:"Items about an entity of this type."`
		DomainID     int64     `query:"domain"`
		Since        time.Time `query:"since" doc:"Items updated at or after this time."`
		Limit        int       `query:"limit" minimum:"1" maximum:"50" default:"20"`
	}
	type out struct{ Body Response }
	huma.Register(a, huma.Operation{
		OperationID: "search", Method: http.MethodGet, Path: "/api/v1/search", Tags: []string{"search"},
		Summary:     "Search the visible items",
		Description: "Hybrid search: full-text, trigram and vectors, fused by reciprocal rank. Matching entities and terms come first.",
		Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(ctx context.Context, in *in) (*out, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		f := Filters{SourceType: in.SourceType, ConnectionID: in.ConnectionID, EntityType: in.EntityType, DomainID: in.DomainID}
		if !in.Since.IsZero() {
			f.Since = &in.Since
		}
		res, err := s.Search(ctx, vis, in.Q, f, in.Limit)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &out{Body: *res}, nil
	})
}
