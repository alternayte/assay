// Package vocab holds the vocabulary: domains, terms, the term linker, usage
// and the glossary export.
package vocab

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

// Domain is an area of the company with its own vocabulary.
type Domain struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type domainBody struct {
	Name        string `json:"name" minLength:"1" maxLength:"100"`
	Description string `json:"description" maxLength:"1000"`
}

// RegisterDomains adds the domain routes. Members read the list. Admins
// change it.
func RegisterDomains(a huma.API, pool *pgxpool.Pool) {
	tags := []string{"domains"}
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}

	type listOut struct {
		Body struct {
			Domains []Domain `json:"domains"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listDomains", Method: http.MethodGet, Path: "/api/v1/domains", Tags: tags,
		Summary: "List domains", Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		rows, err := New(pool).ListDomains(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOut{}
		out.Body.Domains = []Domain{}
		for _, r := range rows {
			out.Body.Domains = append(out.Body.Domains, Domain{ID: r.ID, Name: r.Name, Description: r.Description})
		}
		return out, nil
	})

	type idOut struct {
		Body struct {
			ID int64 `json:"id"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "createDomain", Method: http.MethodPost, Path: "/api/v1/domains", Tags: tags,
		Summary: "Create a domain", DefaultStatus: http.StatusCreated, Middlewares: admins,
	}, func(ctx context.Context, in *struct{ Body domainBody }) (*idOut, error) {
		out := &idOut{}
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			id, err := New(tx).InsertDomain(ctx, InsertDomainParams{Name: strings.TrimSpace(in.Body.Name), Description: strings.TrimSpace(in.Body.Description)})
			if err != nil {
				return taken(err)
			}
			out.Body.ID = id
			return audit.Write(ctx, tx, audit.Entry{Action: "domains.create", Target: fmt.Sprintf("domain:%d", id), Details: map[string]any{"name": in.Body.Name}})
		})
		return out, wrap(err)
	})

	type updateIn struct {
		ID   int64 `path:"id"`
		Body domainBody
	}
	huma.Register(a, huma.Operation{
		OperationID: "updateDomain", Method: http.MethodPut, Path: "/api/v1/domains/{id}", Tags: tags,
		Summary: "Change a domain", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *updateIn) (*struct{}, error) {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			n, err := New(tx).UpdateDomain(ctx, UpdateDomainParams{ID: in.ID, Name: strings.TrimSpace(in.Body.Name), Description: strings.TrimSpace(in.Body.Description)})
			if err != nil {
				return taken(err)
			}
			if n == 0 {
				return api.ErrNotFound
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "domains.update", Target: fmt.Sprintf("domain:%d", in.ID), Details: map[string]any{"name": in.Body.Name}})
		})
		return nil, wrap(err)
	})

	huma.Register(a, huma.Operation{
		OperationID: "deleteDomain", Method: http.MethodDelete, Path: "/api/v1/domains/{id}", Tags: tags,
		Summary: "Delete a domain", Description: "Connections and entities with this domain keep no domain.",
		DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *struct {
		ID int64 `path:"id"`
	}) (*struct{}, error) {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			n, err := New(tx).DeleteDomain(ctx, in.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return api.ErrNotFound
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "domains.delete", Target: fmt.Sprintf("domain:%d", in.ID)})
		})
		return nil, wrap(err)
	})
}

func taken(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return api.Problem(http.StatusConflict, "name_taken", "A domain with this name exists.")
	}
	return err
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	var e *api.Error
	if errors.As(err, &e) {
		return e
	}
	return api.Internal(err)
}
