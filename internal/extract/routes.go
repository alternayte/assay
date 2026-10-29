package extract

import (
	"context"
	"fmt"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/visibility"
)

// Register adds the admin extraction routes.
func (s *Service) Register(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}
	tags := []string{"connections"}
	type idIn struct {
		ID int64 `path:"id"`
	}
	type estimateOut struct{ Body Estimate }
	huma.Register(a, huma.Operation{
		OperationID: "estimateExtraction", Method: http.MethodGet, Path: "/api/v1/connections/{id}/extraction", Tags: tags,
		Summary:     "Estimate the extraction of a connection",
		Description: "Counts the passages with no cached result and estimates the cost. Sends no model request.",
		Middlewares: admins,
	}, func(ctx context.Context, in *idIn) (*estimateOut, error) {
		e, err := s.EstimateConnection(ctx, in.ID)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &estimateOut{Body: e}, nil
	})

	type startIn struct {
		ID   int64 `path:"id"`
		Body struct {
			Passages int64 `json:"passages" minimum:"1" doc:"The passage count of the estimate that you confirm."`
		}
	}
	type startOut struct {
		Body struct {
			Queued int `json:"queued"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "startExtraction", Method: http.MethodPost, Path: "/api/v1/connections/{id}/extraction", Tags: tags,
		Summary:     "Extract the claims of a connection",
		Description: "Confirm the passage count of the estimate. A different count returns 409 with the new estimate in the message.",
		Middlewares: admins, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, in *startIn) (*startOut, error) {
		e, err := s.EstimateConnection(ctx, in.ID)
		if err != nil {
			return nil, api.Internal(err)
		}
		if !e.On {
			return nil, api.Problem(http.StatusConflict, "extract_off", "Turn on the extract function first.")
		}
		if e.Passages != in.Body.Passages {
			return nil, api.Problem(http.StatusConflict, "estimate_changed",
				fmt.Sprintf("The estimate changed to %d passages. Check the estimate again.", e.Passages))
		}
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		n, err := s.QueueConnection(ctx, in.ID, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "extraction.start", Target: fmt.Sprintf("connection:%d", in.ID),
			Details: map[string]any{"passages": n, "costUsd": e.CostUSD}}); err != nil {
			return nil, api.Internal(err)
		}
		out := &startOut{}
		out.Body.Queued = n
		return out, nil
	})
}
