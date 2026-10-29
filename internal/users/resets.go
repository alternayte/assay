package users

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/alternayte/auth-all/plugins/admin"
	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

func (s *Service) registerResets(a huma.API) {
	type linkOutput struct {
		Body struct {
			URL       string    `json:"url" doc:"The reset link. It shows only once."`
			ExpiresAt time.Time `json:"expiresAt"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "createResetLink", Method: http.MethodPost, Path: "/api/v1/users/{id}/reset-link", Tags: []string{"users"},
		Summary: "Create a one-time password reset link", DefaultStatus: http.StatusCreated,
		Middlewares: huma.Middlewares{api.Require(auth.RoleAdmin)},
	}, func(ctx context.Context, in *userIDInput) (*linkOutput, error) {
		if _, err := s.user(ctx, in.ID); err != nil {
			return nil, err
		}
		token, hash, err := newToken()
		if err != nil {
			return nil, api.Internal(err)
		}
		expires := s.now().Add(resetTTL)
		err = s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			if _, err := q.InsertReset(ctx, InsertResetParams{UserID: in.ID, TokenHash: hash, CreatedBy: api.Caller(ctx).User.ID, ExpiresAt: expires}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "user.reset_link", Target: "user:" + in.ID})
		})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &linkOutput{}
		out.Body.URL = s.BaseURL + "/reset-password#" + token
		out.Body.ExpiresAt = expires
		return out, nil
	})

	type acceptInput struct {
		Body struct {
			Token    string `json:"token" minLength:"1"`
			Password string `json:"password" minLength:"8" maxLength:"4096"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "acceptResetLink", Method: http.MethodPost, Path: "/api/v1/password-resets/accept", Tags: []string{"users"},
		Summary: "Set a new password with a reset link", DefaultStatus: http.StatusNoContent,
		Description: "The link works once. All sessions of the user end.",
	}, func(ctx context.Context, in *acceptInput) (*struct{}, error) {
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			userID, err := q.ClaimReset(ctx, ClaimResetParams{TokenHash: hashToken(in.Body.Token), Now: ptr(s.now())})
			if errors.Is(err, pgx.ErrNoRows) {
				return api.Problem(http.StatusNotFound, "reset_invalid", "The reset link is not valid. It is used or expired.")
			}
			if err != nil {
				return err
			}
			if _, err := s.Auth.Admin.ResetPassword(ctx, userID, admin.ResetOptions{Password: in.Body.Password}); err != nil {
				return api.FromAuth(err)
			}
			return audit.WriteAs(ctx, tx, userID, audit.Entry{Action: "user.password_reset", Target: "user:" + userID})
		})
		return nil, wrap(err)
	})
}
