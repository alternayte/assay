package users

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alternayte/auth-all/plugins/admin"
	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

// Invite is an open invite.
type Invite struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (s *Service) registerInvites(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}

	type createInput struct {
		Body struct {
			Email string `json:"email" format:"email" maxLength:"320"`
			Role  string `json:"role" enum:"member,reviewer,admin"`
		}
	}
	type createOutput struct {
		Body struct {
			Invite
			URL     string `json:"url" doc:"The invite link. It shows only once."`
			Emailed bool   `json:"emailed" doc:"True when Assay also sent the link by email."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "createInvite", Method: http.MethodPost, Path: "/api/v1/invites", Tags: []string{"users"},
		Summary: "Invite a user", DefaultStatus: http.StatusCreated, Middlewares: admins,
	}, func(ctx context.Context, in *createInput) (*createOutput, error) {
		email := strings.TrimSpace(in.Body.Email)
		if u, err := s.Auth.GetUserByEmail(ctx, email); err == nil && u != nil {
			return nil, api.Problem(http.StatusConflict, "user_exists", "A user with this email exists.")
		}
		token, hash, err := newToken()
		if err != nil {
			return nil, api.Internal(err)
		}
		caller := api.Caller(ctx).User
		expires := s.now().Add(inviteTTL)
		var row InsertInviteRow
		err = s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			row, err = q.InsertInvite(ctx, InsertInviteParams{Email: email, Role: in.Body.Role, TokenHash: hash, CreatedBy: caller.ID, ExpiresAt: expires})
			if err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "user.invite", Target: fmt.Sprintf("invite:%d", row.ID),
				Details: map[string]any{"email": email, "role": in.Body.Role}})
		})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &createOutput{}
		out.Body.Invite = Invite{ID: row.ID, Email: email, Role: in.Body.Role, CreatedBy: caller.Email, CreatedAt: row.CreatedAt, ExpiresAt: expires}
		out.Body.URL = s.BaseURL + "/invite#" + token
		if s.Mail != nil {
			body := fmt.Sprintf("You have an invite to Assay.\n\nOpen this link to set your password:\n%s\n\nThe link expires on %s.\n",
				out.Body.URL, expires.UTC().Format("2 January 2006 15:04 MST"))
			sent, err := s.Mail(ctx, email, "Your invite to Assay", body)
			if err != nil {
				return nil, api.Problem(http.StatusBadGateway, "smtp_failed", "Assay created the invite but cannot send the email: "+err.Error())
			}
			out.Body.Emailed = sent
		}
		return out, nil
	})

	type listOutput struct {
		Body struct {
			Invites []Invite `json:"invites"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listInvites", Method: http.MethodGet, Path: "/api/v1/invites", Tags: []string{"users"},
		Summary: "List open invites", Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*listOutput, error) {
		rows, err := New(s.Pool).ListOpenInvites(ctx, s.now())
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOutput{}
		out.Body.Invites = make([]Invite, 0, len(rows))
		for _, r := range rows {
			out.Body.Invites = append(out.Body.Invites, Invite{ID: r.ID, Email: r.Email, Role: r.Role, CreatedBy: r.CreatedByEmail, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt})
		}
		return out, nil
	})

	type idInput struct {
		ID int64 `path:"id"`
	}
	huma.Register(a, huma.Operation{
		OperationID: "revokeInvite", Method: http.MethodDelete, Path: "/api/v1/invites/{id}", Tags: []string{"users"},
		Summary: "Revoke an open invite", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *idInput) (*struct{}, error) {
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			n, err := q.RevokeInvite(ctx, RevokeInviteParams{ID: in.ID, Now: ptr(s.now())})
			if err != nil {
				return err
			}
			if n == 0 {
				return api.ErrNotFound
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "user.invite_revoke", Target: fmt.Sprintf("invite:%d", in.ID)})
		})
		return nil, wrap(err)
	})

	type lookupInput struct {
		Body struct {
			Token string `json:"token" minLength:"1"`
		}
	}
	type lookupOutput struct {
		Body struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "lookupInvite", Method: http.MethodPost, Path: "/api/v1/invites/lookup", Tags: []string{"users"},
		Summary: "Read an invite by its token", Description: "Returns 404 for an unknown, used, revoked or expired invite.",
	}, func(ctx context.Context, in *lookupInput) (*lookupOutput, error) {
		inv, err := s.openInvite(ctx, New(s.Pool), in.Body.Token)
		if err != nil {
			return nil, err
		}
		out := &lookupOutput{}
		out.Body.Email, out.Body.Role = inv.Email, inv.Role
		return out, nil
	})

	type acceptInput struct {
		Body struct {
			Token    string `json:"token" minLength:"1"`
			Name     string `json:"name" minLength:"1" maxLength:"200"`
			Password string `json:"password" minLength:"8" maxLength:"4096"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "acceptInvite", Method: http.MethodPost, Path: "/api/v1/invites/accept", Tags: []string{"users"},
		Summary: "Accept an invite and set a password", DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *acceptInput) (*userOutput, error) {
		var userID string
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			inv, err := s.openInvite(ctx, q, in.Body.Token)
			if err != nil {
				return err
			}
			// The claim locks the invite row, so a second use waits and
			// then finds it accepted.
			n, err := q.ClaimInvite(ctx, ClaimInviteParams{ID: inv.ID, Now: ptr(s.now())})
			if err != nil {
				return err
			}
			if n == 0 {
				return errInviteInvalid
			}
			u, _, err := s.Auth.Admin.CreateUser(ctx, admin.CreateUserInput{Email: inv.Email, Name: in.Body.Name, Role: inv.Role, Password: in.Body.Password})
			if err != nil {
				return api.FromAuth(err)
			}
			userID = u.ID
			if err := q.SetInviteUser(ctx, SetInviteUserParams{ID: inv.ID, UserID: &u.ID}); err != nil {
				return err
			}
			return audit.WriteAs(ctx, tx, u.ID, audit.Entry{Action: "user.invite_accept", Target: fmt.Sprintf("invite:%d", inv.ID),
				Details: map[string]any{"user": u.ID, "role": inv.Role}})
		})
		if err != nil {
			return nil, wrap(err)
		}
		u, err := s.user(ctx, userID)
		return &userOutput{Body: u}, err
	})
}

var errInviteInvalid = api.Problem(http.StatusNotFound, "invite_invalid", "The invite link is not valid. It is used, revoked or expired.")

func (s *Service) openInvite(ctx context.Context, q *Queries, token string) (GetInviteByHashRow, error) {
	inv, err := q.GetInviteByHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, errInviteInvalid
	}
	if err != nil {
		return inv, api.Internal(err)
	}
	if inv.AcceptedAt != nil || inv.RevokedAt != nil || !s.now().Before(inv.ExpiresAt) {
		return inv, errInviteInvalid
	}
	return inv, nil
}

// wrap passes an API error through and hides any other error.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae
	}
	return api.Internal(err)
}
