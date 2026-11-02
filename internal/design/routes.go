package design

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/visibility"
)

type keyIn struct {
	Key string `path:"key" doc:"The entity key, escaped as one path segment."`
}

func (k keyIn) key() string {
	if u, err := url.PathUnescape(k.Key); err == nil {
		return u
	}
	return k.Key
}

type sectionIn struct {
	Key  string `path:"key" doc:"The entity key, escaped as one path segment."`
	Slug string `path:"slug" doc:"The section, such as overview or component:internal/sync, escaped as one path segment."`
}

func (s sectionIn) key() string { return keyIn{Key: s.Key}.key() }

func (s sectionIn) slug() string {
	if u, err := url.PathUnescape(s.Slug); err == nil {
		return u
	}
	return s.Slug
}

func routeErr(err error) error {
	var inv *InvalidError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return api.ErrNotFound
	case errors.As(err, &inv):
		return api.Problem(http.StatusUnprocessableEntity, inv.Code, inv.Message)
	case errors.Is(err, models.ErrDisabled):
		return api.Problem(http.StatusConflict, "model_off", "The design model is off. An admin turns it on on the Models page.")
	case errors.Is(err, models.ErrBudgetExceeded):
		return api.Problem(http.StatusConflict, "budget", "The monthly budget of the design function is used up.")
	}
	return api.Internal(err)
}

// Register adds the design routes.
func (s *Service) Register(a huma.API, base string) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	reviewers := huma.Middlewares{api.Require(auth.RoleReviewer)}
	tags := []string{"design"}

	type pageOut struct{ Body DesignPage }
	huma.Register(a, huma.Operation{
		OperationID: "getDesignPage", Method: http.MethodGet, Path: "/api/v1/entities/{key}/design", Tags: tags,
		Summary: "Get the design page of an entity",
		Description: "A repository with indexed code, a system, or a service of two or more repositories has a design page. " +
			"Each sentence, diagram node and diagram edge cites a source passage or a claim. Other entities, hidden entities and missing keys give 404.",
		Middlewares: members,
	}, func(ctx context.Context, in *keyIn) (*pageOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		p, err := s.Get(ctx, vis, in.key())
		if err != nil {
			return nil, routeErr(err)
		}
		return &pageOut{Body: *p}, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "compileDesignPage", Method: http.MethodPost, Path: "/api/v1/entities/{key}/design/compile", Tags: tags,
		Summary: "Write the overview of a system design page",
		Description: "Writes the overview of a system or service page for your visibility key when it is missing or stale. " +
			"Repository pages build after each sync.",
		Middlewares: members, Metadata: api.TokenScope(auth.ScopeRead),
	}, func(ctx context.Context, in *keyIn) (*pageOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		p, err := s.CompileSystem(ctx, vis, in.key(), false)
		if err != nil {
			return nil, routeErr(err)
		}
		return &pageOut{Body: *p}, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "regenerateDesignPage", Method: http.MethodPost, Path: "/api/v1/entities/{key}/design/regenerate", Tags: tags,
		Summary:     "Regenerate a design page",
		Description: "Queues a build of a repository page, or writes the overview of a system page again for your visibility key. Pinned sections stay.",
		Middlewares: reviewers, DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, in *keyIn) (*struct{}, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		p, err := s.Get(ctx, vis, in.key())
		if err != nil {
			return nil, routeErr(err)
		}
		if p.Level == "system" {
			_, err = s.CompileSystem(ctx, vis, in.key(), true)
		} else {
			err = s.QueueRebuild(ctx, vis, in.key())
		}
		return nil, routeErr(err)
	})

	type editIn struct {
		Key  string `path:"key" doc:"The entity key, escaped as one path segment."`
		Slug string `path:"slug" doc:"The section, such as overview or component:internal/sync, escaped as one path segment."`
		Body SectionEdit
	}
	type editOut struct{ Body SavedEdit }
	huma.Register(a, huma.Operation{
		OperationID: "editDesignSection", Method: http.MethodPut, Path: "/api/v1/entities/{key}/design/sections/{slug}", Tags: tags,
		Summary: "Correct a section of a design page",
		Description: "A reviewer saves a new version at once. It is pinned: regeneration does not replace it. " +
			"The edit of a member becomes a review card. Keep markers such as [S1] to keep their sources.",
		Middlewares: members, Metadata: api.TokenScope(auth.ScopePropose),
	}, func(ctx context.Context, in *editIn) (*editOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		c := api.Caller(ctx)
		token := c.APIKey != nil
		sec := sectionIn{Key: in.Key, Slug: in.Slug}
		res, err := s.Edit(ctx, vis, sec.key(), sec.slug(), in.Body, c.User.ID, !token && api.AtLeast(ctx, auth.RoleReviewer), via(token))
		if err != nil {
			return nil, routeErr(err)
		}
		return &editOut{Body: *res}, nil
	})

	type problemIn struct {
		Key  string `path:"key" doc:"The entity key, escaped as one path segment."`
		Slug string `path:"slug" doc:"The section, such as overview or component:internal/sync, escaped as one path segment."`
		Body struct {
			Reason string `json:"reason" minLength:"1" maxLength:"2000" pattern:"\\S" patternDescription:"text"`
		}
	}
	type problemOut struct {
		Body struct {
			Proposal int64 `json:"proposal"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "reportDesignProblem", Method: http.MethodPost, Path: "/api/v1/entities/{key}/design/sections/{slug}/problems", Tags: tags,
		Summary:     "Report a problem with a section of a design page",
		Description: "Creates a review card with the reason. A reviewer regenerates the section with the reason or saves corrected text.",
		Middlewares: members, DefaultStatus: http.StatusCreated, Metadata: api.TokenScope(auth.ScopePropose),
	}, func(ctx context.Context, in *problemIn) (*problemOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		c := api.Caller(ctx)
		sec := sectionIn{Key: in.Key, Slug: in.Slug}
		id, err := s.Report(ctx, vis, sec.key(), sec.slug(), in.Body.Reason, c.User.ID, via(c.APIKey != nil))
		if err != nil {
			return nil, routeErr(err)
		}
		out := &problemOut{}
		out.Body.Proposal = id
		return out, nil
	})

	type mdOut struct {
		ContentType string `header:"Content-Type"`
		Body        []byte
	}
	huma.Register(a, huma.Operation{
		OperationID: "getDesignMarkdown", Method: http.MethodGet, Path: "/api/v1/entities/{key}/design.md", Tags: tags,
		Summary:     "Get the design page as Markdown",
		Description: "Footnotes cite the file and line range of each source passage. Diagrams are Mermaid code blocks. /e/{key}/design.md gives the same document.",
		Middlewares: members,
		Responses: map[string]*huma.Response{"200": {Description: "The design page as Markdown.",
			Content: map[string]*huma.MediaType{"text/markdown": {Schema: &huma.Schema{Type: "string"}}}}},
	}, func(ctx context.Context, in *keyIn) (*mdOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		p, err := s.Get(ctx, vis, in.key())
		if err != nil {
			return nil, routeErr(err)
		}
		return &mdOut{ContentType: "text/markdown; charset=utf-8", Body: []byte(p.Markdown(strings.TrimRight(base, "/")))}, nil
	})
}

// via is the channel of a proposal: an access token is an agent.
func via(token bool) string {
	if token {
		return "mcp"
	}
	return "ui"
}

// MarkdownPath maps /e/{key}/design.md to the API path of the design
// Markdown, or returns false for another path.
func MarkdownPath(rawPath string) (string, bool) {
	rest, ok := strings.CutPrefix(rawPath, "/e/")
	if !ok || !strings.HasSuffix(rest, "/design.md") {
		return "", false
	}
	key := strings.TrimSuffix(rest, "/design.md")
	if key == "" || strings.Contains(key, "/") {
		return "", false
	}
	return "/api/v1/entities/" + key + "/design.md", true
}
