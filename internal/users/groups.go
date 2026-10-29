package users

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
)

// Group is a visibility group.
type Group struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Builtin     bool   `json:"builtin" doc:"True for Everyone, which holds all active users and cannot change."`
	MemberCount int64  `json:"memberCount"`
}

// Member is a user in a group.
type Member struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
	Status string `json:"status" enum:"active,deactivated"`
}

var errBuiltin = api.Problem(http.StatusForbidden, "builtin_group", "Everyone holds all active users. It cannot change.")

func (s *Service) registerGroups(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}

	type listOutput struct {
		Body struct {
			Groups []Group `json:"groups"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listGroups", Method: http.MethodGet, Path: "/api/v1/groups", Tags: []string{"groups"},
		Summary: "List groups", Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*listOutput, error) {
		rows, err := New(s.Pool).ListGroups(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOutput{}
		out.Body.Groups = make([]Group, 0, len(rows))
		for _, r := range rows {
			out.Body.Groups = append(out.Body.Groups, Group{ID: r.ID, Name: r.Name, Builtin: r.Builtin, MemberCount: r.MemberCount})
		}
		return out, nil
	})

	type nameBody struct {
		Name string `json:"name" minLength:"1" maxLength:"100"`
	}
	type groupOutput struct {
		Body Group
	}
	huma.Register(a, huma.Operation{
		OperationID: "createGroup", Method: http.MethodPost, Path: "/api/v1/groups", Tags: []string{"groups"},
		Summary: "Create a group", DefaultStatus: http.StatusCreated, Middlewares: admins,
	}, func(ctx context.Context, in *struct{ Body nameBody }) (*groupOutput, error) {
		name := strings.TrimSpace(in.Body.Name)
		var id int64
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			var err error
			if id, err = q.InsertGroup(ctx, name); err != nil {
				return uniqueName(err)
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "group.create", Target: fmt.Sprintf("group:%d", id), Details: map[string]any{"name": name}})
		})
		if err != nil {
			return nil, wrap(err)
		}
		return &groupOutput{Body: Group{ID: id, Name: name}}, nil
	})

	type renameInput struct {
		ID   int64 `path:"id"`
		Body nameBody
	}
	huma.Register(a, huma.Operation{
		OperationID: "renameGroup", Method: http.MethodPatch, Path: "/api/v1/groups/{id}", Tags: []string{"groups"},
		Summary: "Rename a group", Middlewares: admins,
	}, func(ctx context.Context, in *renameInput) (*groupOutput, error) {
		name := strings.TrimSpace(in.Body.Name)
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			g, err := s.group(ctx, q, in.ID)
			if err != nil {
				return err
			}
			if g.Builtin {
				return errBuiltin
			}
			if _, err := q.RenameGroup(ctx, RenameGroupParams{ID: in.ID, Name: name}); err != nil {
				return uniqueName(err)
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "group.rename", Target: fmt.Sprintf("group:%d", in.ID),
				Details: map[string]any{"old": g.Name, "new": name}})
		})
		if err != nil {
			return nil, wrap(err)
		}
		return &groupOutput{Body: Group{ID: in.ID, Name: name}}, nil
	})

	type idInput struct {
		ID int64 `path:"id"`
	}
	huma.Register(a, huma.Operation{
		OperationID: "deleteGroup", Method: http.MethodDelete, Path: "/api/v1/groups/{id}", Tags: []string{"groups"},
		Summary: "Delete a group", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *idInput) (*struct{}, error) {
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			g, err := s.group(ctx, q, in.ID)
			if err != nil {
				return err
			}
			if g.Builtin {
				return errBuiltin
			}
			if _, err := q.DeleteGroup(ctx, in.ID); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "group.delete", Target: fmt.Sprintf("group:%d", in.ID), Details: map[string]any{"name": g.Name}})
		})
		return nil, wrap(err)
	})

	type membersOutput struct {
		Body struct {
			Members []Member `json:"members"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listGroupMembers", Method: http.MethodGet, Path: "/api/v1/groups/{id}/members", Tags: []string{"groups"},
		Summary: "List the members of a group", Middlewares: admins,
	}, func(ctx context.Context, in *idInput) (*membersOutput, error) {
		q := New(s.Pool)
		g, err := s.group(ctx, q, in.ID)
		if err != nil {
			return nil, err
		}
		out := &membersOutput{}
		out.Body.Members = []Member{}
		if g.Builtin {
			rows, err := q.ListActiveUsers(ctx)
			if err != nil {
				return nil, api.Internal(err)
			}
			for _, r := range rows {
				out.Body.Members = append(out.Body.Members, Member{ID: r.ID, Email: r.Email, Name: r.DisplayName, Status: "active"})
			}
			return out, nil
		}
		rows, err := q.ListGroupMembers(ctx, in.ID)
		if err != nil {
			return nil, api.Internal(err)
		}
		for _, r := range rows {
			status := "active"
			if r.DisabledAt != nil {
				status = "deactivated"
			}
			out.Body.Members = append(out.Body.Members, Member{ID: r.ID, Email: r.Email, Name: r.DisplayName, Status: status})
		}
		return out, nil
	})

	type memberInput struct {
		ID     int64  `path:"id"`
		UserID string `path:"userId" format:"uuid"`
	}
	huma.Register(a, huma.Operation{
		OperationID: "addGroupMember", Method: http.MethodPut, Path: "/api/v1/groups/{id}/members/{userId}", Tags: []string{"groups"},
		Summary: "Add a user to a group", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *memberInput) (*struct{}, error) {
		if _, err := s.user(ctx, in.UserID); err != nil {
			return nil, err
		}
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			g, err := s.group(ctx, q, in.ID)
			if err != nil {
				return err
			}
			if g.Builtin {
				return errBuiltin
			}
			if err := q.AddGroupMember(ctx, AddGroupMemberParams{GroupID: in.ID, UserID: in.UserID}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "group.member_add", Target: fmt.Sprintf("group:%d", in.ID), Details: map[string]any{"user": in.UserID}})
		})
		return nil, wrap(err)
	})

	huma.Register(a, huma.Operation{
		OperationID: "removeGroupMember", Method: http.MethodDelete, Path: "/api/v1/groups/{id}/members/{userId}", Tags: []string{"groups"},
		Summary: "Remove a user from a group", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *memberInput) (*struct{}, error) {
		err := s.tx(ctx, func(q *Queries, tx pgx.Tx) error {
			g, err := s.group(ctx, q, in.ID)
			if err != nil {
				return err
			}
			if g.Builtin {
				return errBuiltin
			}
			n, err := q.RemoveGroupMember(ctx, RemoveGroupMemberParams{GroupID: in.ID, UserID: in.UserID})
			if err != nil {
				return err
			}
			if n == 0 {
				return api.ErrNotFound
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "group.member_remove", Target: fmt.Sprintf("group:%d", in.ID), Details: map[string]any{"user": in.UserID}})
		})
		return nil, wrap(err)
	})
}

func (s *Service) group(ctx context.Context, q *Queries, id int64) (GetGroupRow, error) {
	g, err := q.GetGroup(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, api.ErrNotFound
	}
	return g, err
}

func uniqueName(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return api.Problem(http.StatusConflict, "name_taken", "A group with this name exists.")
	}
	return err
}
