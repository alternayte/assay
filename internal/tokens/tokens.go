// Package tokens lets each user create, list and revoke access tokens for
// agents. The tokens are auth-all API keys: auth-all stores the SHA-256
// digest, checks expiry and revocation, and writes the last use time.
package tokens

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alternayte/auth-all/apierr"
	"github.com/alternayte/auth-all/plugins/apikeys"
	"github.com/alternayte/auth-all/store"
	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

// DefaultDays is the life of a token when the user gives none.
const DefaultDays = 90

// Service manages the tokens of the caller.
type Service struct {
	Pool *pgxpool.Pool
	Keys *apikeys.Plugin
	Now  func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Token is one access token. It never carries the value.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Start      string     `json:"start" doc:"The first characters, so that a person recognizes the token."`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	Scopes     []string   `json:"scopes" enum:"read,propose"`
}

func toToken(k store.APIKey) Token {
	return Token{ID: k.ID, Name: k.Name, Start: k.Start, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt,
		RevokedAt: k.RevokedAt, Scopes: k.ScopeList()}
}

// sessionOnly refuses a request that a token authenticates: a token never
// creates, lists or revokes tokens.
func sessionOnly(ctx huma.Context, next func(huma.Context)) {
	if p := api.Caller(ctx.Context()); p != nil && p.APIKey != nil {
		api.WriteError(ctx, api.Problem(http.StatusForbidden, "session_required", "Sign in to manage access tokens."))
		return
	}
	next(ctx)
}

// Register adds the token routes.
func (s *Service) Register(a huma.API) {
	mw := huma.Middlewares{api.Require(auth.RoleMember), sessionOnly}
	tags := []string{"tokens"}

	type listOut struct {
		Body struct {
			Tokens []Token `json:"tokens"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listTokens", Method: http.MethodGet, Path: "/api/v1/tokens", Tags: tags,
		Summary: "List your access tokens", Description: "Values never show again after create.", Middlewares: mw,
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		ks, err := s.Keys.List(ctx, api.Caller(ctx).User.ID)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOut{}
		out.Body.Tokens = make([]Token, len(ks))
		for i, k := range ks {
			out.Body.Tokens[i] = toToken(k)
		}
		return out, nil
	})

	type createIn struct {
		Body struct {
			Name          string   `json:"name" minLength:"1" maxLength:"100"`
			ExpiresInDays int      `json:"expiresInDays,omitempty" minimum:"1" maximum:"365" doc:"Default 90."`
			Scopes        []string `json:"scopes,omitempty" enum:"read,propose" uniqueItems:"true" doc:"read reads; propose proposes findings and reports problems. Default read."`
		}
	}
	type createOut struct {
		Body struct {
			Token Token  `json:"token"`
			Value string `json:"value" doc:"The token value. It shows only in this response."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "createToken", Method: http.MethodPost, Path: "/api/v1/tokens", Tags: tags,
		Summary:     "Create an access token",
		Description: "The token has your role, your visible set and its scopes. It expires after 90 days by default, at most 365.",
		Middlewares: mw, DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIn) (*createOut, error) {
		days := in.Body.ExpiresInDays
		if days == 0 {
			days = DefaultDays
		}
		exp := s.now().Add(time.Duration(days) * 24 * time.Hour)
		user := api.Caller(ctx).User
		k, value, err := s.Keys.Create(ctx, user, apikeys.CreateInput{UserID: user.ID, Name: in.Body.Name, ExpiresAt: &exp,
			Scopes: strings.Join(in.Body.Scopes, " ")})
		if err != nil {
			return nil, api.FromAuth(err)
		}
		if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "token.create", Target: "token:" + k.ID,
			Details: map[string]any{"name": k.Name, "expiresAt": exp, "scopes": k.ScopeList()}}); err != nil {
			return nil, api.Internal(err)
		}
		out := &createOut{}
		out.Body.Token, out.Body.Value = toToken(*k), value
		return out, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "revokeToken", Method: http.MethodDelete, Path: "/api/v1/tokens/{id}", Tags: tags,
		Summary: "Revoke an access token", Description: "The next request with the token gets 401.",
		Middlewares: mw, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *struct {
		ID string `path:"id"`
	}) (*struct{}, error) {
		err := s.Keys.Revoke(ctx, in.ID, api.Caller(ctx).User, false)
		switch {
		case errors.Is(err, apierr.ErrNotFound):
			return nil, api.ErrNotFound
		case err != nil:
			return nil, api.Internal(err)
		}
		if err := audit.Write(ctx, s.Pool, audit.Entry{Action: "token.revoke", Target: "token:" + in.ID}); err != nil {
			return nil, api.Internal(err)
		}
		return nil, nil
	})
}
