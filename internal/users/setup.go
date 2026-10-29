package users

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/alternayte/auth-all/plugins/admin"
	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
)

// SetupDone reports whether a user exists. After that, setup is closed.
func (s *Service) SetupDone(ctx context.Context) (bool, error) {
	n, err := New(s.Pool).CountUsers(ctx)
	return n > 0, err
}

func (s *Service) registerSetup(a huma.API) {
	type statusOutput struct {
		Body struct {
			Required bool `json:"required"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "getSetup", Method: http.MethodGet, Path: "/api/v1/setup", Tags: []string{"setup"},
		Summary:     "Report whether the first setup is open",
		Description: "Returns 404 after the first admin exists.",
	}, func(ctx context.Context, _ *struct{}) (*statusOutput, error) {
		done, err := s.SetupDone(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		if done {
			return nil, api.ErrNotFound
		}
		out := &statusOutput{}
		out.Body.Required = true
		return out, nil
	})

	type setupInput struct {
		Body struct {
			Token    string `json:"token" minLength:"1"`
			Email    string `json:"email" format:"email" maxLength:"320"`
			Name     string `json:"name" minLength:"1" maxLength:"200"`
			Password string `json:"password" minLength:"8" maxLength:"4096"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "completeSetup", Method: http.MethodPost, Path: "/api/v1/setup", Tags: []string{"setup"},
		Summary: "Create the first admin", DefaultStatus: http.StatusCreated,
		Description: "Needs the setup token from ASSAY_SETUP_TOKEN. Returns 404 after the first admin exists.",
	}, func(ctx context.Context, in *setupInput) (*userOutput, error) {
		done, err := s.SetupDone(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		if done {
			return nil, api.ErrNotFound
		}
		if s.SetupToken == "" {
			return nil, api.Problem(http.StatusForbidden, "setup_disabled", "Setup is off. Set ASSAY_SETUP_TOKEN, or run assay admin create.")
		}
		if subtle.ConstantTimeCompare([]byte(in.Body.Token), []byte(s.SetupToken)) != 1 {
			return nil, api.Problem(http.StatusForbidden, "setup_token", "The setup token is not correct.")
		}
		created, err := s.Auth.Admin.Bootstrap(ctx, admin.Credentials{Email: in.Body.Email, Password: in.Body.Password, Name: in.Body.Name})
		if err != nil {
			return nil, api.FromAuth(err)
		}
		if !created {
			return nil, api.ErrNotFound
		}
		u, err := s.Auth.GetUserByEmail(ctx, in.Body.Email)
		if err != nil {
			return nil, api.Internal(err)
		}
		if err := audit.WriteAs(ctx, s.Pool, u.ID, audit.Entry{Action: "user.setup", Target: "user:" + u.ID}); err != nil {
			return nil, api.Internal(err)
		}
		out, err := s.user(ctx, u.ID)
		return &userOutput{Body: out}, err
	})
}
