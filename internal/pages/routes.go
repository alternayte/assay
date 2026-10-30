package pages

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/visibility"
)

// PageDeltaEvent is a piece of the summary as the model writes it.
type PageDeltaEvent struct {
	Text string `json:"text"`
}

// PageRetryEvent starts a new draft after a failed check. The client drops
// the text so far.
type PageRetryEvent struct {
	Failures []Failure `json:"failures"`
}

// PageErrorEvent ends a compile that failed.
type PageErrorEvent struct {
	Message string `json:"message"`
}

type keyIn struct {
	Key string `path:"key" doc:"The entity key, escaped as one path segment."`
}

func (k keyIn) key() string {
	if u, err := url.PathUnescape(k.Key); err == nil {
		return u
	}
	return k.Key
}

func notFound(err error) error {
	if errors.Is(err, entities.ErrNotFound) {
		return api.ErrNotFound
	}
	return api.Internal(err)
}

// Register adds the page routes.
func (s *Service) Register(a huma.API, base string) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	reviewers := huma.Middlewares{api.Require(auth.RoleReviewer)}
	tags := []string{"pages"}

	type pageOut struct{ Body Page }
	huma.Register(a, huma.Operation{
		OperationID: "getPage", Method: http.MethodGet, Path: "/api/v1/entities/{key}/page", Tags: tags,
		Summary: "Get the knowledge page of an entity",
		Description: "The sections come from your visible claims. The summary is the cached summary of your visibility key when it is fresh. " +
			"When the state is missing or stale, compile the page.",
		Middlewares: members,
	}, func(ctx context.Context, in *keyIn) (*pageOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		p, err := s.Get(ctx, vis, in.key())
		if err != nil {
			return nil, notFound(err)
		}
		return &pageOut{Body: *p}, nil
	})

	// visible gives the 404 of a hidden or missing entity before the stream
	// starts.
	visible := func(ctx huma.Context, next func(huma.Context)) {
		vis, err := visibility.OfCaller(ctx.Context(), s.Pool)
		if err == nil {
			_, err = s.Entities.Get(ctx.Context(), vis, keyIn{Key: ctx.Param("key")}.key())
		}
		switch {
		case errors.Is(err, entities.ErrNotFound):
			api.WriteError(ctx, api.ErrNotFound)
		case err != nil:
			s.Log.Error("compile failed", "error", err.Error())
			api.WriteError(ctx, api.Problem(http.StatusInternalServerError, "internal", "The server cannot complete the request."))
		default:
			next(ctx)
		}
	}
	sse.Register(a, huma.Operation{
		OperationID: "compilePage", Method: http.MethodPost, Path: "/api/v1/entities/{key}/page/compile", Tags: tags,
		Summary: "Compile the summary of a page",
		Description: "Streams server-sent events: delta events with the summary text, a retry event when a draft fails a check, " +
			"then the page. A hidden entity and a missing key give the same 404.",
		Middlewares: huma.Middlewares{api.Require(auth.RoleMember), visible},
	}, map[string]any{"delta": PageDeltaEvent{}, "retry": PageRetryEvent{}, "page": Page{}, "error": PageErrorEvent{}},
		func(ctx context.Context, in *keyIn, send sse.Sender) {
			vis, err := visibility.OfCaller(ctx, s.Pool)
			if err == nil {
				var p *Page
				p, err = s.Compile(ctx, vis, in.key(), Events{
					Delta: func(d string) { _ = send.Data(PageDeltaEvent{Text: d}) },
					Retry: func(fs []Failure) { _ = send.Data(PageRetryEvent{Failures: fs}) },
				})
				if err == nil {
					_ = send.Data(*p)
					return
				}
			}
			switch {
			case errors.Is(err, entities.ErrNotFound):
				_ = send.Data(PageErrorEvent{Message: "The page does not exist."})
			case ctx.Err() == nil:
				s.Log.Error("compile failed", "error", err.Error())
				_ = send.Data(PageErrorEvent{Message: "The server cannot compile the page."})
			}
		})

	huma.Register(a, huma.Operation{
		OperationID: "regeneratePage", Method: http.MethodPost, Path: "/api/v1/entities/{key}/page/regenerate", Tags: tags,
		Summary: "Regenerate the summary of a page", Description: "Compiles the summary again for your visibility key, also when it is fresh.",
		Middlewares: reviewers,
	}, func(ctx context.Context, in *keyIn) (*pageOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		p, err := s.Compile(ctx, vis, in.key(), Events{})
		if err != nil {
			return nil, notFound(err)
		}
		return &pageOut{Body: *p}, nil
	})

	type mdOut struct {
		ContentType string `header:"Content-Type"`
		Body        []byte
	}
	huma.Register(a, huma.Operation{
		OperationID: "getPageMarkdown", Method: http.MethodGet, Path: "/api/v1/entities/{key}/page.md", Tags: tags,
		Summary:     "Get the knowledge page as Markdown",
		Description: "Footnotes cite the source URLs. Approved terms link to their pages. /e/{key}.md gives the same document.",
		Middlewares: members,
		Responses: map[string]*huma.Response{"200": {Description: "The page as Markdown.",
			Content: map[string]*huma.MediaType{"text/markdown": {Schema: &huma.Schema{Type: "string"}}}}},
	}, func(ctx context.Context, in *keyIn) (*mdOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		p, err := s.Get(ctx, vis, in.key())
		if err != nil {
			return nil, notFound(err)
		}
		approved, err := s.Vocab.Approved(ctx, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &mdOut{ContentType: "text/markdown; charset=utf-8", Body: []byte(p.Markdown(strings.TrimRight(base, "/"), approved))}, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "getGlossaryMarkdown", Method: http.MethodGet, Path: "/api/v1/glossary.md", Tags: tags,
		Summary:     "Get the glossary as Markdown",
		Description: "The approved terms that you can read, with definitions and links. /glossary.md gives the same document.",
		Middlewares: members,
		Responses: map[string]*huma.Response{"200": {Description: "The glossary as Markdown.",
			Content: map[string]*huma.MediaType{"text/markdown": {Schema: &huma.Schema{Type: "string"}}}}},
	}, func(ctx context.Context, _ *struct{}) (*mdOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		approved, err := s.Vocab.Approved(ctx, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &mdOut{ContentType: "text/markdown; charset=utf-8", Body: []byte(GlossaryMarkdown(strings.TrimRight(base, "/"), approved))}, nil
	})
}

// GlossaryPath is the app path of the glossary Markdown.
const GlossaryPath = "/glossary.md"

// MarkdownPath maps /e/{key}.md to the API path of the page Markdown, or
// returns false for another path.
func MarkdownPath(rawPath string) (string, bool) {
	rest, ok := strings.CutPrefix(rawPath, "/e/")
	if !ok || !strings.HasSuffix(rest, ".md") || strings.Contains(rest, "/") {
		return "", false
	}
	return "/api/v1/entities/" + strings.TrimSuffix(rest, ".md") + "/page.md", true
}
