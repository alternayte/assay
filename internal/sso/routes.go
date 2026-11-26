package sso

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

// SignInOptions is what the sign-in page offers.
type SignInOptions struct {
	SSO      bool   `json:"sso" doc:"True when SSO sign-in is configured."`
	Label    string `json:"label" doc:"The label of the SSO button. Empty means the default label."`
	Password bool   `json:"password" doc:"True when password sign-in is served."`
}

// PollStatus is the result of the directory polls.
type PollStatus struct {
	LastStartedAt *time.Time `json:"lastStartedAt"`
	LastSuccessAt *time.Time `json:"lastSuccessAt" doc:"The end of the last good poll."`
	LastErrorAt   *time.Time `json:"lastErrorAt"`
	LastError     string     `json:"lastError" doc:"The error of the last poll. Empty after a good poll."`
}

// SSOStatus is the state of the sign-in settings.
type SSOStatus struct {
	Effective   SignInConfig      `json:"effective" doc:"The configuration after the environment variables."`
	Locks       map[string]string `json:"locks" doc:"The fields that an environment variable sets, with the variable name."`
	SecretSet   bool              `json:"secretSet"`
	CallbackURL string            `json:"callbackUrl" doc:"The redirect URI to register at the identity provider."`
	Ready       bool              `json:"ready" doc:"True when the issuer, the client ID and the client secret are set."`
	AdminLinked bool              `json:"adminLinked" doc:"True when an enabled admin has a linked SSO account."`
	PasswordOn  bool              `json:"passwordOn" doc:"True when password sign-in is served now."`
	DirectoryOn bool              `json:"directoryOn"`
	Directory   PollStatus        `json:"directory"`
}

// DirectoryGroup is one group of the directory group picker.
type DirectoryGroup struct {
	ID   string `json:"id" doc:"The IdP group ID."`
	Name string `json:"name"`
}

// Register adds the sign-in and directory routes.
func (s *Service) Register(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}

	type optionsOutput struct{ Body SignInOptions }
	huma.Register(a, huma.Operation{
		OperationID: "getSignInOptions", Method: http.MethodGet, Path: "/api/v1/sign-in/options", Tags: []string{"sign-in"},
		Summary: "Get the sign-in methods", Description: "The sign-in page calls it before sign-in.",
	}, func(ctx context.Context, _ *struct{}) (*optionsOutput, error) {
		e, err := s.Effective(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &optionsOutput{Body: SignInOptions{SSO: e.Ready(), Label: e.ButtonLabel, Password: e.PasswordOn()}}, nil
	})

	type statusOutput struct{ Body SSOStatus }
	huma.Register(a, huma.Operation{
		OperationID: "getSSOStatus", Method: http.MethodGet, Path: "/api/v1/sso", Tags: []string{"sign-in"},
		Summary: "Get the state of SSO and the directory sync", Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*statusOutput, error) {
		e, err := s.Effective(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		q := New(s.Pool)
		linked, err := q.AdminLinked(ctx, ProviderID)
		if err != nil {
			return nil, api.Internal(err)
		}
		st, err := q.GetState(ctx, ProviderID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, api.Internal(err)
		}
		return &statusOutput{Body: SSOStatus{
			Effective: e.SignInConfig, Locks: e.Locks, SecretSet: e.ClientSecret != "", Ready: e.Ready(), AdminLinked: linked,
			CallbackURL: strings.TrimRight(s.BaseURL, "/") + s.Auth.BasePath() + "/oauth/" + ProviderID + "/callback",
			PasswordOn:  e.PasswordOn(), DirectoryOn: e.DirectoryOn(),
			Directory: PollStatus{LastStartedAt: st.LastStartedAt, LastSuccessAt: st.LastSuccessAt,
				LastErrorAt: st.LastErrorAt, LastError: st.LastError},
		}}, nil
	})

	type secretInput struct {
		Body struct {
			Secret string `json:"secret" doc:"An empty value removes the secret." maxLength:"1024"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "setSSOSecret", Method: http.MethodPut, Path: "/api/v1/sso/secret", Tags: []string{"sign-in"},
		Summary: "Set the client secret", Description: "The secret is write-only.", DefaultStatus: http.StatusNoContent,
		Middlewares: admins,
	}, func(ctx context.Context, in *secretInput) (*struct{}, error) {
		if _, ok := s.Env[EnvClientSecret]; ok {
			return nil, api.Problem(http.StatusConflict, "locked", EnvClientSecret+" sets the client secret.")
		}
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			if in.Body.Secret == "" {
				err = s.Secrets.Delete(ctx, tx, SecretName)
			} else {
				_, err = s.Secrets.Put(ctx, tx, SecretName, []byte(in.Body.Secret))
			}
			if err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "settings.secret", Target: "secret:" + SecretName})
		})
		if err != nil {
			return nil, api.Internal(err)
		}
		return nil, nil
	})

	type groupsInput struct {
		Prefix string `query:"prefix" maxLength:"100" doc:"The start of the group name."`
	}
	type groupsOutput struct {
		Body struct {
			Groups []DirectoryGroup `json:"groups"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listDirectoryGroups", Method: http.MethodGet, Path: "/api/v1/sso/directory/groups", Tags: []string{"sign-in"},
		Summary: "List directory groups", Description: "Reads the groups of the directory for the group picker.", Middlewares: admins,
	}, func(ctx context.Context, in *groupsInput) (*groupsOutput, error) {
		e, err := s.Effective(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		if !e.DirectoryOn() {
			return nil, api.Problem(http.StatusConflict, "directory_off", "Turn on the directory sync to read the directory groups.")
		}
		p, err := s.directoryProvider(e)
		if err != nil {
			return nil, api.Internal(err)
		}
		list, err := p.Groups(ctx, in.Prefix)
		if err != nil {
			return nil, api.Problem(http.StatusBadGateway, "directory_failed", "The directory read failed: "+err.Error())
		}
		out := &groupsOutput{}
		out.Body.Groups = make([]DirectoryGroup, 0, len(list))
		for _, g := range list {
			out.Body.Groups = append(out.Body.Groups, DirectoryGroup(g))
		}
		return out, nil
	})

	type mappingInput struct {
		ID   int64 `path:"id"`
		Body struct {
			ExternalID   string `json:"externalId" minLength:"1" maxLength:"200" doc:"The IdP group ID. Entra ID uses the object ID."`
			ExternalName string `json:"externalName" maxLength:"300"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "setGroupMapping", Method: http.MethodPut, Path: "/api/v1/groups/{id}/mapping", Tags: []string{"groups"},
		Summary:       "Map an IdP group to a group",
		Description:   "The IdP then sets the members. The current members are removed, and sign-in and the directory poll add the IdP members.",
		DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *mappingInput) (*struct{}, error) {
		ext := strings.TrimSpace(in.Body.ExternalID)
		name := strings.TrimSpace(in.Body.ExternalName)
		if name == "" {
			name = ext
		}
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			q := New(tx)
			g, err := q.GetGroup(ctx, in.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return api.ErrNotFound
			}
			if err != nil {
				return err
			}
			if g.Builtin {
				return api.Problem(http.StatusForbidden, "builtin_group", "Everyone holds all active users. It cannot change.")
			}
			if err := q.PutMapping(ctx, PutMappingParams{GroupID: in.ID, Provider: ProviderID, ExternalID: ext, ExternalName: name}); err != nil {
				var pe *pgconn.PgError
				if errors.As(err, &pe) && pe.Code == "23505" {
					return api.Problem(http.StatusConflict, "mapping_taken", "Another group is mapped to this IdP group.")
				}
				return err
			}
			removed, err := q.ClearMembers(ctx, in.ID)
			if err != nil {
				return err
			}
			for _, u := range removed {
				if err := audit.Write(ctx, tx, audit.Entry{Action: "group.member_remove", Target: fmt.Sprintf("group:%d", in.ID),
					Details: map[string]any{"user": u, "source": "mapping"}}); err != nil {
					return err
				}
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "group.map", Target: fmt.Sprintf("group:%d", in.ID),
				Details: map[string]any{"externalId": ext, "externalName": name}})
		})
		if err != nil {
			return nil, wrap(err)
		}
		if err := s.QueuePoll(ctx); err != nil {
			return nil, api.Internal(err)
		}
		return nil, nil
	})

	type idInput struct {
		ID int64 `path:"id"`
	}
	huma.Register(a, huma.Operation{
		OperationID: "deleteGroupMapping", Method: http.MethodDelete, Path: "/api/v1/groups/{id}/mapping", Tags: []string{"groups"},
		Summary: "Remove the IdP mapping of a group", Description: "The members stay, and admins change them again.",
		DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *idInput) (*struct{}, error) {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			n, err := New(tx).DeleteMapping(ctx, in.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return api.ErrNotFound
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "group.unmap", Target: fmt.Sprintf("group:%d", in.ID)})
		})
		return nil, wrap(err)
	})
}

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
