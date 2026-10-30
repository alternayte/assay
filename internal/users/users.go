// Package users holds the first setup, invites, roles, deactivation, password
// reset links and groups. auth-all stores the users. Assay owns the invites,
// the reset links and the groups.
package users

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

const (
	inviteTTL = 7 * 24 * time.Hour
	resetTTL  = 24 * time.Hour
)

// Service serves the user routes.
type Service struct {
	Pool       *pgxpool.Pool
	Auth       *auth.Auth
	BaseURL    string
	SetupToken string
	// Now is the clock. Tests set it.
	Now func() time.Time
	// Mail sends a message when SMTP is configured. It reports false when
	// SMTP is not configured.
	Mail func(ctx context.Context, to, subject, body string) (bool, error)
	// OnSetup runs after setup creates the first admin. The demo creates
	// its connections here.
	OnSetup func(ctx context.Context, adminID string) error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// UserRecord is a user as the API shows it.
type UserRecord struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role" enum:"member,reviewer,admin"`
	Status    string    `json:"status" enum:"active,deactivated"`
	CreatedAt time.Time `json:"createdAt"`
}

func toUser(id, email, name, role string, disabled *time.Time, created time.Time) UserRecord {
	if role == "" {
		role = auth.RoleMember
	}
	status := "active"
	if disabled != nil {
		status = "deactivated"
	}
	return UserRecord{ID: id, Email: email, Name: name, Role: role, Status: status, CreatedAt: created}
}

// newToken returns a random token and its hash.
func newToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	t := base64.RawURLEncoding.EncodeToString(b)
	return t, hashToken(t), nil
}

func hashToken(t string) []byte {
	h := sha256.Sum256([]byte(t))
	return h[:]
}

func (s *Service) tx(ctx context.Context, fn func(q *Queries, tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error { return fn(New(tx), tx) })
}

// Register adds the user routes to the API.
func (s *Service) Register(a huma.API) {
	s.registerSetup(a)
	s.registerUsers(a)
	s.registerInvites(a)
	s.registerResets(a)
	s.registerGroups(a)
}

type userOutput struct {
	Body UserRecord
}

type usersOutput struct {
	Body struct {
		Users []UserRecord `json:"users"`
	}
}

type userIDInput struct {
	ID string `path:"id" format:"uuid"`
}

func (s *Service) registerUsers(a huma.API) {
	admin := huma.Middlewares{api.Require(auth.RoleAdmin)}

	huma.Register(a, huma.Operation{
		OperationID: "getMe", Method: http.MethodGet, Path: "/api/v1/me", Tags: []string{"users"},
		Summary: "Get the current user", Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(ctx context.Context, _ *struct{}) (*userOutput, error) {
		p := api.Caller(ctx)
		u := p.User
		return &userOutput{Body: toUser(u.ID, u.Email, u.DisplayName, api.Role(p), u.DisabledAt, u.CreatedAt)}, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "listUsers", Method: http.MethodGet, Path: "/api/v1/users", Tags: []string{"users"},
		Summary: "List users", Middlewares: admin,
	}, func(ctx context.Context, _ *struct{}) (*usersOutput, error) {
		rows, err := New(s.Pool).ListUsers(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &usersOutput{}
		out.Body.Users = make([]UserRecord, 0, len(rows))
		for _, r := range rows {
			out.Body.Users = append(out.Body.Users, toUser(r.ID, r.Email, r.DisplayName, r.Role, r.DisabledAt, r.CreatedAt))
		}
		return out, nil
	})

	type roleInput struct {
		ID   string `path:"id" format:"uuid"`
		Body struct {
			Role string `json:"role" enum:"member,reviewer,admin"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "setUserRole", Method: http.MethodPut, Path: "/api/v1/users/{id}/role", Tags: []string{"users"},
		Summary: "Change the role of a user", Middlewares: admin,
	}, func(ctx context.Context, in *roleInput) (*userOutput, error) {
		before, err := s.user(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if _, err := s.Auth.Admin.SetRole(ctx, in.ID, in.Body.Role); err != nil {
			return nil, api.FromAuth(err)
		}
		if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "user.role", Target: "user:" + in.ID,
			Details: map[string]any{"old": before.Role, "new": in.Body.Role}}); err != nil {
			return nil, api.Internal(err)
		}
		u, err := s.user(ctx, in.ID)
		return &userOutput{Body: u}, err
	})

	huma.Register(a, huma.Operation{
		OperationID: "deactivateUser", Method: http.MethodPost, Path: "/api/v1/users/{id}/deactivate", Tags: []string{"users"},
		Summary:     "Deactivate a user",
		Description: "Deactivation revokes all sessions and access tokens of the user.", Middlewares: admin,
	}, func(ctx context.Context, in *userIDInput) (*userOutput, error) {
		if _, err := s.user(ctx, in.ID); err != nil {
			return nil, err
		}
		if api.Caller(ctx).User.ID == in.ID {
			return nil, api.Problem(http.StatusForbidden, "own_account", "You cannot deactivate your own account.")
		}
		if _, err := s.Auth.Admin.Disable(ctx, in.ID); err != nil {
			return nil, api.FromAuth(err)
		}
		if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "user.deactivate", Target: "user:" + in.ID}); err != nil {
			return nil, api.Internal(err)
		}
		u, err := s.user(ctx, in.ID)
		return &userOutput{Body: u}, err
	})

	huma.Register(a, huma.Operation{
		OperationID: "reactivateUser", Method: http.MethodPost, Path: "/api/v1/users/{id}/reactivate", Tags: []string{"users"},
		Summary: "Reactivate a user", Middlewares: admin,
	}, func(ctx context.Context, in *userIDInput) (*userOutput, error) {
		if _, err := s.user(ctx, in.ID); err != nil {
			return nil, err
		}
		if _, err := s.Auth.Admin.Enable(ctx, in.ID); err != nil {
			return nil, api.FromAuth(err)
		}
		if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "user.reactivate", Target: "user:" + in.ID}); err != nil {
			return nil, api.Internal(err)
		}
		u, err := s.user(ctx, in.ID)
		return &userOutput{Body: u}, err
	})
}

func (s *Service) user(ctx context.Context, id string) (UserRecord, error) {
	r, err := New(s.Pool).GetUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, api.ErrNotFound
	}
	if err != nil {
		return UserRecord{}, api.Internal(err)
	}
	return toUser(r.ID, r.Email, r.DisplayName, r.Role, r.DisabledAt, r.CreatedAt), nil
}

func ptr[T any](v T) *T { return &v }
