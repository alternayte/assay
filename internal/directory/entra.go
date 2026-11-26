package directory

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/entra"
	"github.com/alternayte/assay/internal/outbound"
)

// EntraOptions configure the Entra ID provider.
type EntraOptions struct {
	// GraphURL is the Graph v1.0 base URL. Empty means the public Graph.
	GraphURL string
	// TokenURL is the token endpoint of the tenant.
	TokenURL     string
	ClientID     string
	ClientSecret string
}

// Entra reads Entra ID through Microsoft Graph. It needs the application
// permissions User.Read.All and GroupMember.Read.All. Graph reads use the
// GET-only connector client. The token comes from the entra token client,
// which sends one POST to the token URL.
type Entra struct {
	settings outbound.Settings
	graph    string
	tokens   *entra.TokenSource
}

// NewEntra returns the Entra ID provider.
func NewEntra(s outbound.Settings, o EntraOptions) (*Entra, error) {
	graph := strings.TrimRight(o.GraphURL, "/")
	if graph == "" {
		graph = entra.GraphURL
	}
	scope, err := entra.ScopeFor(graph)
	if err != nil {
		return nil, err
	}
	ts, err := entra.NewTokenSource(s, entra.Credentials{TokenURL: o.TokenURL, ClientID: o.ClientID, ClientSecret: o.ClientSecret, Scope: scope})
	if err != nil {
		return nil, err
	}
	return &Entra{settings: s, graph: graph, tokens: ts}, nil
}

// client returns a Graph client with a valid token.
func (e *Entra) client(ctx context.Context) (*connectors.Client, error) {
	authz, err := e.tokens.Authorization(ctx)
	if err != nil {
		return nil, err
	}
	return connectors.NewClient(e.settings, connectors.ClientOptions{BaseURL: e.graph, Authorization: authz, Concurrency: 2, PerSecond: 10})
}

// request is one Graph request below the base URL.
type request struct {
	path  string
	query url.Values
}

// follow turns a next link or a delta link of Graph into a request. The link
// must stay below the Graph base URL.
func (e *Entra) follow(link string) (request, error) {
	u, err := url.Parse(link)
	if err != nil {
		return request{}, fmt.Errorf("directory: Graph returned the link %q: %w", link, err)
	}
	base, _ := url.Parse(e.graph)
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return request{}, fmt.Errorf("directory: Graph returned a link to another host: %s", u.Host)
	}
	rel, ok := strings.CutPrefix(u.Path, base.Path)
	if !ok || !strings.HasPrefix(rel, "/") {
		return request{}, fmt.Errorf("directory: Graph returned a link outside %s", e.graph)
	}
	return request{path: rel, query: u.Query()}, nil
}

type graphPage[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

// pages reads a Graph collection page by page and calls fn for each page. It
// returns the delta link of the last page, if any.
func pages[T any](ctx context.Context, e *Entra, first request, fn func([]T) error) (string, error) {
	c, err := e.client(ctx)
	if err != nil {
		return "", err
	}
	req := first
	for {
		var page graphPage[T]
		if err := c.GetJSON(ctx, req.path, req.query, &page); err != nil {
			return "", err
		}
		if err := fn(page.Value); err != nil {
			return "", err
		}
		if page.NextLink == "" {
			return page.DeltaLink, nil
		}
		if req, err = e.follow(page.NextLink); err != nil {
			return "", err
		}
	}
}

type graphUser struct {
	ID             string `json:"id"`
	Mail           string `json:"mail"`
	AccountEnabled *bool  `json:"accountEnabled"`
	Removed        *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

// Users reads GET /users/delta. An empty since starts a new delta round,
// which lists every user.
func (e *Entra) Users(ctx context.Context, since string) (Users, error) {
	out := Users{Full: since == ""}
	first := request{path: "/users/delta", query: url.Values{"$select": {"id,accountEnabled,mail"}}}
	if since != "" {
		var err error
		if first, err = e.follow(since); err != nil {
			return Users{}, err
		}
	}
	next, err := pages(ctx, e, first, func(list []graphUser) error {
		for _, u := range list {
			if u.ID == "" {
				continue
			}
			out.Users = append(out.Users, User{ID: u.ID, Mail: u.Mail, Enabled: u.AccountEnabled, Removed: u.Removed != nil})
		}
		return nil
	})
	if err != nil {
		var se *connectors.StatusError
		// Graph answers 410 Gone for an expired delta token, and 400 with
		// a sync state error for a token it no longer knows.
		if since != "" && errors.As(err, &se) && (se.Status == 410 || se.Status == 400) {
			return Users{}, ErrResync
		}
		return Users{}, err
	}
	if next == "" {
		return Users{}, errors.New("directory: Graph returned no delta link")
	}
	out.Next = next
	return out, nil
}

type graphObject struct {
	Type string `json:"@odata.type"`
	ID   string `json:"id"`
	Name string `json:"displayName"`
}

// Members reads GET /groups/{id}/transitiveMembers and keeps the users.
func (e *Entra) Members(ctx context.Context, groupID string) ([]string, error) {
	var ids []string
	_, err := pages(ctx, e, request{path: "/groups/" + url.PathEscape(groupID) + "/transitiveMembers",
		query: url.Values{"$select": {"id"}, "$top": {"999"}}}, func(list []graphObject) error {
		for _, o := range list {
			if o.Type == "#microsoft.graph.user" && o.ID != "" {
				ids = append(ids, o.ID)
			}
		}
		return nil
	})
	if errors.Is(err, connectors.ErrNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrGroupNotFound, groupID)
	}
	return ids, err
}

// Groups reads GET /groups with a startswith filter on the display name.
func (e *Entra) Groups(ctx context.Context, prefix string) ([]Group, error) {
	q := url.Values{"$select": {"id,displayName"}, "$top": {"50"}}
	if p := strings.TrimSpace(prefix); p != "" {
		q.Set("$filter", "startswith(displayName,'"+strings.ReplaceAll(p, "'", "''")+"')")
	}
	c, err := e.client(ctx)
	if err != nil {
		return nil, err
	}
	var page graphPage[graphObject]
	if err := c.GetJSON(ctx, "/groups", q, &page); err != nil {
		return nil, err
	}
	out := make([]Group, 0, len(page.Value))
	for _, g := range page.Value {
		out = append(out, Group{ID: g.ID, Name: g.Name})
	}
	return out, nil
}
