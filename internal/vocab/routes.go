package vocab

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/visibility"
)

// TermDetail is a term with its usage per connection.
type TermDetail struct {
	Term
	Usage []Usage `json:"usage"`
}

// Linked is a text with its term links and the terms that they name.
type Linked struct {
	Links []Match `json:"links"`
	Terms []Term  `json:"terms"`
}

// LinkText links the approved visible terms in a text.
func LinkText(ts []Term, text string, domain int64) Linked {
	out := Linked{Links: []Match{}, Terms: []Term{}}
	byKey := map[string]Term{}
	for _, t := range ts {
		byKey[t.Key] = t
	}
	seen := map[string]bool{}
	for _, m := range LinkerOf(ts).Link(text, domain) {
		out.Links = append(out.Links, m)
		for _, k := range m.Terms {
			if !seen[k] {
				seen[k] = true
				out.Terms = append(out.Terms, byKey[k])
			}
		}
	}
	return out
}

// Register adds the term routes.
func (s *Service) Register(a huma.API) {
	members := huma.Middlewares{api.Require(auth.RoleMember)}
	tags := []string{"vocabulary"}

	type listIn struct {
		Q        string `query:"q" maxLength:"200" doc:"Text in a name, a synonym or the definition."`
		Status   string `query:"status" enum:"draft,approved,deprecated,"`
		DomainID int64  `query:"domain"`
		Owner    string `query:"owner" maxLength:"200" doc:"Text in the owner name."`
	}
	type listOut struct {
		Body struct {
			Terms []Term `json:"terms"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listTerms", Method: http.MethodGet, Path: "/api/v1/terms", Tags: tags,
		Summary: "List the visible terms", Description: "The Glossary. Names, synonyms and definitions come from visible connections only.",
		Middlewares: members,
	}, func(ctx context.Context, in *listIn) (*listOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		ts, err := s.Terms(ctx, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		q := entities.NormalizeAlias(in.Q)
		out := &listOut{}
		out.Body.Terms = []Term{}
		for _, t := range ts {
			hay := entities.NormalizeAlias(strings.Join(append(FormsOf(t), t.Definition), " "))
			if (in.Status == "" || t.Status == in.Status) && (in.DomainID == 0 || t.DomainID == in.DomainID) &&
				(in.Owner == "" || strings.Contains(strings.ToLower(t.Owner), strings.ToLower(in.Owner))) &&
				(q == "" || strings.Contains(hay, q)) {
				out.Body.Terms = append(out.Body.Terms, t)
			}
		}
		return out, nil
	})

	type oneOut struct{ Body TermDetail }
	huma.Register(a, huma.Operation{
		OperationID: "getTerm", Method: http.MethodGet, Path: "/api/v1/terms/{key}", Tags: tags,
		Summary: "Get a term with its usage", Description: "Usage counts visible items per connection, with deprecated forms apart.",
		Middlewares: members,
	}, func(ctx context.Context, in *struct {
		Key string `path:"key" doc:"The term key, escaped as one path segment."`
	}) (*oneOut, error) {
		key := in.Key
		if u, err := url.PathUnescape(key); err == nil {
			key = u
		}
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		ts, err := s.Terms(ctx, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		for _, t := range ts {
			if t.Key == key {
				u, err := s.UsageOf(ctx, vis, t)
				if err != nil {
					return nil, api.Internal(err)
				}
				return &oneOut{Body: TermDetail{Term: t, Usage: u}}, nil
			}
		}
		return nil, api.ErrNotFound
	})

	type linkIn struct {
		Body struct {
			Text     string `json:"text" maxLength:"200000"`
			DomainID int64  `json:"domainId,omitempty" doc:"The domain of the source connection or entity. It decides homonyms."`
		}
	}
	type linkOut struct{ Body Linked }
	huma.Register(a, huma.Operation{
		OperationID: "linkTerms", Method: http.MethodPost, Path: "/api/v1/terms/links", Tags: tags,
		Summary: "Link approved terms in a text", Description: "Offsets are bytes of the text. Only approved terms with a visible source link.",
		Middlewares: members,
	}, func(ctx context.Context, in *linkIn) (*linkOut, error) {
		vis, err := visibility.OfCaller(ctx, s.Pool)
		if err != nil {
			return nil, api.Internal(err)
		}
		ts, err := s.Approved(ctx, vis)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &linkOut{Body: LinkText(ts, in.Body.Text, in.Body.DomainID)}, nil
	})
}
