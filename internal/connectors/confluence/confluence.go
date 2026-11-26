// Package confluence reads Confluence Data Center through /rest/api/content
// with a personal access token, and Confluence Cloud with an API token. On
// Cloud, CQL search lists the pages and REST v2 reads one page.
package confluence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/content"
)

// Scope selects the pages of a connection.
type Scope struct {
	Spaces    []string `json:"spaces" doc:"Space keys."`
	Ancestors []string `json:"ancestors,omitempty" doc:"Optional page IDs. Only their descendants are read."`
	Labels    []string `json:"labels,omitempty" doc:"Optional labels. Only pages with one of them are read."`
}

// Connector reads one Confluence connection.
type Connector struct {
	c      *connectors.Client
	scope  Scope
	rules  connectors.Rules
	limits connectors.Limits
	// cloud reads Confluence Cloud below /wiki. CQL search still lists the
	// pages: REST v2 has no CQL and no filter by change time.
	cloud bool
	// The names that a Cloud page refers to by ID, read once per
	// connector.
	mu     gosync.Mutex
	spaces map[string]spaceRef
	users  map[string]string
	titles map[string]string
	// pages are the page IDs of each part that are stored or listed in
	// this run. An attachment is read only when its page is one of them.
	pages map[string]map[string]bool
}

type spaceRef struct{ Key, Name string }

// New returns a connector for Confluence Data Center. scope is the JSON
// scope of the connection.
func New(c *connectors.Client, scope json.RawMessage, rules connectors.Rules, limits connectors.Limits) (*Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("confluence: the scope is not valid: %w", err)
		}
	}
	return &Connector{c: c, scope: s, rules: rules, limits: limits, pages: map[string]map[string]bool{}}, nil
}

// NewCloud returns a connector for Confluence Cloud. The client base URL is
// the site URL or the API host of a scoped token, without /wiki.
func NewCloud(c *connectors.Client, scope json.RawMessage, rules connectors.Rules, limits connectors.Limits) (*Connector, error) {
	k, err := New(c, scope, rules, limits)
	if err != nil {
		return nil, err
	}
	k.cloud = true
	k.spaces, k.users, k.titles = map[string]spaceRef{}, map[string]string{}, map[string]string{}
	return k, nil
}

// prefix is the path of Confluence below the base URL: /wiki on Cloud.
func (k *Connector) prefix() string {
	if k.cloud {
		return "/wiki"
	}
	return ""
}

func quoted(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = strconv.Quote(v)
	}
	return strings.Join(q, ",")
}

// cql is the query of the pages in scope. spaces narrows it to the spaces
// of one part; nil means the spaces of the scope.
func (k *Connector) cql(spaces []string) string {
	if spaces == nil {
		spaces = k.scope.Spaces
	}
	parts := []string{"type=page"}
	if len(spaces) > 0 {
		parts = append(parts, "space in ("+quoted(spaces)+")")
	}
	if len(k.scope.Ancestors) > 0 {
		parts = append(parts, "ancestor in ("+strings.Join(k.scope.Ancestors, ",")+")")
	}
	if len(k.scope.Labels) > 0 {
		parts = append(parts, "label in ("+quoted(k.scope.Labels)+")")
	}
	return strings.Join(parts, " AND ")
}

type searchPage struct {
	Results   []pageJSON `json:"results"`
	Size      int        `json:"size"`
	TotalSize int        `json:"totalSize"`
	Links     struct {
		Context string `json:"context"`
		Next    string `json:"next"`
	} `json:"_links"`
}

// pageExpand is the expansion of a whole page.
const pageExpand = "body.storage,version,ancestors,metadata.labels,space,history"

// Validate checks the token and counts the pages in scope.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	var u struct {
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
	}
	if err := k.c.GetJSON(ctx, k.prefix()+"/rest/api/user/current", nil, &u); err != nil {
		return connectors.Validation{}, err
	}
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	if k.cloud {
		// The CQL search of Cloud can leave out the total. One page then
		// gives the count, and the scope can hold more.
		var p searchPage
		if err := k.c.GetJSON(ctx, "/wiki/rest/api/content/search", url.Values{"cql": {k.cql(nil)}, "limit": {"100"}}, &p); err != nil {
			return connectors.Validation{}, err
		}
		if p.TotalSize > 0 {
			return connectors.Validation{User: name, Sample: p.TotalSize}, nil
		}
		return connectors.Validation{User: name, Sample: max(p.Size, len(p.Results)), More: p.Links.Next != ""}, nil
	}
	var p searchPage
	if err := k.c.GetJSON(ctx, "/rest/api/content/search", url.Values{"cql": {k.cql(nil)}, "limit": {"1"}}, &p); err != nil {
		return connectors.Validation{}, err
	}
	return connectors.Validation{User: name, Sample: max(p.TotalSize, p.Size)}, nil
}

// Scopes lists the spaces.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
	out := []connectors.Scope{}
	if k.cloud {
		err := k.cursor(ctx, "/wiki/api/v2/spaces", url.Values{"limit": {"250"}}, func(raw json.RawMessage) error {
			var sp struct {
				ID   string `json:"id"`
				Key  string `json:"key"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(raw, &sp); err != nil {
				return err
			}
			k.mu.Lock()
			k.spaces[sp.ID] = spaceRef{Key: sp.Key, Name: sp.Name}
			k.mu.Unlock()
			out = append(out, connectors.Scope{ID: sp.Key, Name: sp.Name})
			return nil
		})
		return out, err
	}
	for start := 0; ; start += 100 {
		var p struct {
			Results []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := k.c.GetJSON(ctx, "/rest/api/space", url.Values{"start": {strconv.Itoa(start)}, "limit": {"100"}}, &p); err != nil {
			return nil, err
		}
		for _, r := range p.Results {
			out = append(out, connectors.Scope{ID: r.Key, Name: r.Name})
		}
		if len(p.Results) < 100 {
			return out, nil
		}
	}
}

// Parts are the spaces of the scope, one part per space. A scope without
// spaces reads every space of the token.
func (k *Connector) Parts(ctx context.Context) ([]connectors.Part, error) {
	keys := k.scope.Spaces
	if len(keys) == 0 {
		spaces, err := k.Scopes(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range spaces {
			keys = append(keys, s.ID)
		}
	}
	out := make([]connectors.Part, 0, len(keys))
	for _, key := range keys {
		out = append(out, connectors.Part{Key: key})
	}
	return out, nil
}

// search calls fn for each page of a CQL query. It follows the next link
// of each answer: the server can cap the page size below the limit.
func (k *Connector) search(ctx context.Context, cql, expand string, limit int, fn func(*pageJSON) error) error {
	path := k.prefix() + "/rest/api/content/search"
	q := url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}}
	if expand != "" {
		q.Set("expand", expand)
	}
	for {
		var p searchPage
		if err := k.c.GetJSON(ctx, path, q, &p); err != nil {
			return err
		}
		for i := range p.Results {
			if err := fn(&p.Results[i]); err != nil {
				return err
			}
		}
		if p.Links.Next == "" || len(p.Results) == 0 {
			return nil
		}
		next, err := url.Parse(p.Links.Next)
		if err != nil {
			return fmt.Errorf("confluence: the next link %q is not valid: %w", p.Links.Next, err)
		}
		path, q = next.Path, next.Query()
		// The next link is relative to the base URL, which holds the
		// context path. Some servers repeat the context path. On Cloud the
		// context is /wiki, which the prefix adds again.
		if p.Links.Context != "" {
			path = strings.TrimPrefix(path, p.Links.Context)
		}
		path = k.prefix() + path
	}
}

// cursor calls fn for each result of a REST v2 listing. It follows the
// cursor in the next link of each answer.
func (k *Connector) cursor(ctx context.Context, path string, q url.Values, fn func(json.RawMessage) error) error {
	for {
		var p struct {
			Results []json.RawMessage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := k.c.GetJSON(ctx, path, q, &p); err != nil {
			return err
		}
		for _, r := range p.Results {
			if err := fn(r); err != nil {
				return err
			}
		}
		if p.Links.Next == "" || len(p.Results) == 0 {
			return nil
		}
		next, err := url.Parse(p.Links.Next)
		if err != nil {
			return fmt.Errorf("confluence: the next link %q is not valid: %w", p.Links.Next, err)
		}
		// The link starts at the site root with /wiki. A scoped token
		// reads below /ex/confluence/<cloud ID>, which the base URL holds.
		path, q = next.Path, next.Query()
		if i := strings.Index(path, "/wiki/"); i >= 0 {
			path = path[i:]
		} else {
			path = "/wiki" + path
		}
	}
}

// Changed lists the pages of a space modified at or after since, each with
// its body. CQL dates use the time zone of the token user, so the query
// starts Overlap earlier.
func (k *Connector) Changed(ctx context.Context, part connectors.Part, since time.Time, fn func(connectors.Ref) error) error {
	cql := k.cql([]string{part.Key})
	if !since.IsZero() {
		cql += fmt.Sprintf(` AND lastmodified >= "%s"`, since.Add(-connectors.Overlap).UTC().Format("2006/01/02 15:04"))
	}
	cql += " order by lastmodified asc"
	err := k.search(ctx, cql, pageExpand, 50, func(p *pageJSON) error {
		t, _ := time.Parse(time.RFC3339, p.Version.When)
		r := connectors.Ref{ID: p.ID, Updated: t, Version: revision(p.Version.Number)}
		if k.limits.MaxItemBytes > 0 && len(p.Body.Storage.Value) > k.limits.MaxItemBytes {
			r.Skip = "too_large"
			return fn(r)
		}
		it, err := k.item(p)
		if err != nil {
			return err
		}
		r.Item = it
		k.addPage(part.Key, p.ID)
		return fn(r)
	})
	if err != nil {
		return err
	}
	return k.changedAttachments(ctx, part, since, fn)
}

// ListIDs lists all page IDs of a space in scope, then the IDs of the
// attachments of those pages.
func (k *Connector) ListIDs(ctx context.Context, part connectors.Part, fn func(string) error) error {
	pages := map[string]bool{}
	err := k.search(ctx, k.cql([]string{part.Key}), "", 100, func(p *pageJSON) error {
		pages[p.ID] = true
		return fn(p.ID)
	})
	if err != nil {
		return err
	}
	return k.listAttachments(ctx, part, pages, fn)
}

// PartOf returns the space of a page. A page of a space outside the scope,
// or one that the ancestors or labels of the scope leave out, is out of
// scope. An attachment is never named alone: sync reads it with its page.
func (k *Connector) PartOf(ctx context.Context, it *content.Item) (string, bool, error) {
	space, _ := it.Metadata["space"].(string)
	if space == "" || strings.HasPrefix(it.ExternalID, attachmentPrefix) {
		return "", false, nil
	}
	if len(k.scope.Spaces) > 0 && !slices.Contains(k.scope.Spaces, space) {
		return "", false, nil
	}
	if len(k.scope.Ancestors) == 0 && len(k.scope.Labels) == 0 {
		return space, true, nil
	}
	// A page ID is digits only, so that it is safe in CQL.
	if it.ExternalID == "" || strings.Trim(it.ExternalID, "0123456789") != "" {
		return "", false, nil
	}
	found := false
	err := k.search(ctx, k.cql([]string{space})+" AND id = "+it.ExternalID, "", 1, func(*pageJSON) error {
		found = true
		return nil
	})
	return space, found, err
}

type person struct {
	Username    string `json:"username"`
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

func (p person) name() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Username
}

type pageJSON struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Space struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"space"`
	History struct {
		CreatedBy   person `json:"createdBy"`
		CreatedDate string `json:"createdDate"`
	} `json:"history"`
	Version struct {
		Number int    `json:"number"`
		When   string `json:"when"`
		By     person `json:"by"`
	} `json:"version"`
	Ancestors []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	} `json:"ancestors"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Metadata struct {
		Labels struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		} `json:"labels"`
		// MediaType is the media type of an attachment.
		MediaType string `json:"mediaType"`
	} `json:"metadata"`
	// Container is the page of an attachment.
	Container struct {
		ID    string `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
	} `json:"container"`
	// Extensions hold the media type and the file size of an attachment.
	Extensions struct {
		MediaType string `json:"mediaType"`
		FileSize  int64  `json:"fileSize"`
	} `json:"extensions"`
	Links struct {
		WebUI string `json:"webui"`
		// Download is the download link of an attachment, relative to the
		// context path.
		Download string `json:"download"`
	} `json:"_links"`
}

// Fetch reads one page, or one attachment by its item ID att:<id>.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	if att, ok := strings.CutPrefix(id, attachmentPrefix); ok {
		return k.fetchAttachment(ctx, att)
	}
	if k.cloud {
		return k.fetchCloud(ctx, id)
	}
	var p pageJSON
	q := url.Values{"expand": {pageExpand}}
	if err := k.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id), q, &p); err != nil {
		return nil, err
	}
	if k.limits.MaxItemBytes > 0 && len(p.Body.Storage.Value) > k.limits.MaxItemBytes {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	return k.item(&p)
}

// pageV2 is a page of REST v2. It names its space, people and ancestors
// by ID.
type pageV2 struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	SpaceID   string `json:"spaceId"`
	AuthorID  string `json:"authorId"`
	CreatedAt string `json:"createdAt"`
	Version   struct {
		Number    int    `json:"number"`
		CreatedAt string `json:"createdAt"`
		AuthorID  string `json:"authorId"`
	} `json:"version"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

// fetchCloud reads one page through REST v2 with its storage body, and
// the labels, ancestors, space and people that make the same item as the
// CQL listing.
func (k *Connector) fetchCloud(ctx context.Context, id string) (*content.Item, error) {
	var v pageV2
	if err := k.c.GetJSON(ctx, "/wiki/api/v2/pages/"+url.PathEscape(id), url.Values{"body-format": {"storage"}}, &v); err != nil {
		return nil, err
	}
	if k.limits.MaxItemBytes > 0 && len(v.Body.Storage.Value) > k.limits.MaxItemBytes {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	p := pageJSON{ID: v.ID, Title: v.Title}
	p.Body.Storage.Value = v.Body.Storage.Value
	p.Links.WebUI = v.Links.WebUI
	p.History.CreatedDate = v.CreatedAt
	p.Version.Number, p.Version.When = v.Version.Number, v.Version.CreatedAt
	sp, err := k.space(ctx, v.SpaceID)
	if err != nil {
		return nil, err
	}
	p.Space.Key, p.Space.Name = sp.Key, sp.Name
	if p.History.CreatedBy.DisplayName, err = k.user(ctx, v.AuthorID); err != nil {
		return nil, err
	}
	if p.Version.By.DisplayName, err = k.user(ctx, v.Version.AuthorID); err != nil {
		return nil, err
	}
	err = k.cursor(ctx, "/wiki/api/v2/pages/"+url.PathEscape(id)+"/labels", url.Values{"limit": {"250"}}, func(raw json.RawMessage) error {
		var l struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			return err
		}
		p.Metadata.Labels.Results = append(p.Metadata.Labels.Results, struct {
			Name string `json:"name"`
		}{l.Name})
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The ancestors come root first, as in the CQL expansion.
	err = k.cursor(ctx, "/wiki/api/v2/pages/"+url.PathEscape(id)+"/ancestors", url.Values{"limit": {"250"}}, func(raw json.RawMessage) error {
		var a struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		title, err := k.title(ctx, a.ID)
		if err != nil {
			return err
		}
		p.Ancestors = append(p.Ancestors, struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}{a.ID, title})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return k.item(&p)
}

// space returns the key and name of a Cloud space by ID.
func (k *Connector) space(ctx context.Context, id string) (spaceRef, error) {
	k.mu.Lock()
	sp, ok := k.spaces[id]
	k.mu.Unlock()
	if ok || id == "" {
		return sp, nil
	}
	var v struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if err := k.c.GetJSON(ctx, "/wiki/api/v2/spaces/"+url.PathEscape(id), nil, &v); err != nil {
		return sp, err
	}
	sp = spaceRef{Key: v.Key, Name: v.Name}
	k.mu.Lock()
	k.spaces[id] = sp
	k.mu.Unlock()
	return sp, nil
}

// user returns the display name of a Cloud account. REST v2 names people
// only by account ID.
func (k *Connector) user(ctx context.Context, accountID string) (string, error) {
	k.mu.Lock()
	name, ok := k.users[accountID]
	k.mu.Unlock()
	if ok || accountID == "" {
		return name, nil
	}
	var u person
	err := k.c.GetJSON(ctx, "/wiki/rest/api/user", url.Values{"accountId": {accountID}}, &u)
	if err != nil && !errors.Is(err, connectors.ErrNotFound) {
		return "", err
	}
	name = u.DisplayName
	k.mu.Lock()
	k.users[accountID] = name
	k.mu.Unlock()
	return name, nil
}

// title returns the title of a Cloud page by ID.
func (k *Connector) title(ctx context.Context, id string) (string, error) {
	k.mu.Lock()
	t, ok := k.titles[id]
	k.mu.Unlock()
	if ok {
		return t, nil
	}
	var v struct {
		Title string `json:"title"`
	}
	if err := k.c.GetJSON(ctx, "/wiki/api/v2/pages/"+url.PathEscape(id), nil, &v); err != nil {
		return "", err
	}
	k.mu.Lock()
	k.titles[id] = v.Title
	k.mu.Unlock()
	return v.Title, nil
}

func (k *Connector) item(p *pageJSON) (*content.Item, error) {
	body, err := ToMarkdown(p.Body.Storage.Value)
	if err != nil {
		return nil, fmt.Errorf("confluence: page %s: %w", p.ID, err)
	}
	labels := []string{}
	for _, l := range p.Metadata.Labels.Results {
		labels = append(labels, l.Name)
	}
	ancestors := []string{}
	for _, a := range p.Ancestors {
		ancestors = append(ancestors, a.Title)
	}
	created, _ := time.Parse(time.RFC3339, p.History.CreatedDate)
	updated, _ := time.Parse(time.RFC3339, p.Version.When)
	it := &content.Item{
		ExternalID: p.ID, Kind: "page", Title: p.Title,
		URL:    k.c.Link(k.prefix()+p.Links.WebUI, nil),
		Author: p.History.CreatedBy.name(), EditedBy: p.Version.By.name(),
		Created: created, Updated: updated, Body: body, Revision: revision(p.Version.Number),
		Metadata: map[string]any{"space": p.Space.Key, "spaceName": p.Space.Name, "ancestors": ancestors,
			"labels": labels, "version": p.Version.Number},
	}
	if strings.Contains(p.Links.WebUI, "?") {
		// The web UI link carries its own query.
		u, err := url.Parse(p.Links.WebUI)
		if err == nil {
			it.URL = k.c.Link(k.prefix()+u.Path, u.Query())
		}
	}
	if slices.ContainsFunc(labels, func(l string) bool { return slices.Contains(k.rules.GlossaryLabels, l) }) {
		it.Facts = append(it.Facts, connectors.GlossaryFacts(body, "confluence", k.c.Host())...)
	}
	it.Entities = append(it.Entities, connectors.Acronyms(body, "confluence", k.c.Host())...)
	if slices.ContainsFunc(labels, func(l string) bool { return slices.Contains(k.rules.DecisionLabels, l) }) {
		it.Entity = &content.EntityRef{Type: "decision", Key: "confluence:" + k.c.Host() + ":" + p.ID, Name: p.Title}
	}
	return it, nil
}

// revision is the page version as the revision of an item. Sync skips a
// page whose version is stored.
func revision(version int) string {
	if version <= 0 {
		return ""
	}
	return strconv.Itoa(version)
}
