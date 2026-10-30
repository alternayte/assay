package review

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/visibility"
)

func reviewErr(err error) error {
	var inv *InvalidError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return api.ErrNotFound
	case errors.As(err, &inv):
		return api.Problem(http.StatusUnprocessableEntity, inv.Code, inv.Message)
	}
	return api.Internal(err)
}

// Register adds the report, finding and proposal routes.
func (s *Service) Register(a huma.API) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	tags := []string{"review"}

	type reportIn struct {
		ID   int64 `path:"id"`
		Body ProblemReport
	}
	type proposalOut struct {
		Body struct {
			Proposal int64 `json:"proposal"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "reportProblem", Method: http.MethodPost, Path: "/api/v1/claims/{id}/problems", Tags: tags,
		Summary:     "Report a problem with a claim",
		Description: "Creates a correction card. A reason is required, and wrong_value needs the correct value. The source system does not change.",
		Middlewares: members, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *reportIn) (*proposalOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &proposalOut{}
		out.Body.Proposal, err = s.Report(ctx, vis, in.ID, in.Body, api.Caller(ctx).User.ID, "ui")
		return out, reviewErr(err)
	})

	type findingOut struct {
		Body struct {
			Proposal int64 `json:"proposal"`
			Claim    int64 `json:"claim"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "proposeFinding", Method: http.MethodPost, Path: "/api/v1/findings", Tags: tags,
		Summary:     "Propose a finding",
		Description: "Creates a finding card and a Proposed claim. Each quote must be an exact part of a visible item. A hidden item gives 404.",
		Middlewares: members, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *struct{ Body ProposedFinding }) (*findingOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &findingOut{}
		out.Body.Proposal, out.Body.Claim, err = s.Propose(ctx, vis, in.Body, api.Caller(ctx).User.ID, "ui")
		return out, reviewErr(err)
	})

	type mineIn struct {
		Limit int `query:"limit" minimum:"1" maximum:"200" default:"50"`
	}
	type mineOut struct {
		Body struct {
			Proposals []Proposal `json:"proposals"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listMyProposals", Method: http.MethodGet, Path: "/api/v1/proposals", Tags: tags,
		Summary: "List your proposals with their results", Description: "Newest first. A decided proposal carries the reason of the reviewer.",
		Middlewares: members,
	}, func(ctx context.Context, in *mineIn) (*mineOut, error) {
		ps, err := s.Mine(ctx, api.Caller(ctx).User.ID, in.Limit)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &mineOut{}
		out.Body.Proposals = ps
		return out, nil
	})
}
