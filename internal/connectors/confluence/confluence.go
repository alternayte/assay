// Package confluence reads Confluence Data Center through /rest/api/content
// with a personal access token.
package confluence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
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
}

// New returns a connector. scope is the JSON scope of the connection.
func New(c *connectors.Client, scope json.RawMessage, rules connectors.Rules, limits connectors.Limits) (*Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("confluence: the scope is not valid: %w", err)
		}
	}
	return &Connector{c: c, scope: s, rules: rules, limits: limits}, nil
}

func quoted(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = strconv.Quote(v)
	}
	return strings.Join(q, ",")
}

func (k *Connector) cql() string {
	parts := []string{"type=page"}
	if len(k.scope.Spaces) > 0 {
		parts = append(parts, "space in ("+quoted(k.scope.Spaces)+")")
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
	Results []struct {
		ID      string `json:"id"`
		Version struct {
			When string `json:"when"`
		} `json:"version"`
	} `json:"results"`
	Size      int `json:"size"`
	TotalSize int `json:"totalSize"`
}

// Validate checks the token and counts the pages in scope.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	var u struct {
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
	}
	if err := k.c.GetJSON(ctx, "/rest/api/user/current", nil, &u); err != nil {
		return connectors.Validation{}, err
	}
	var p searchPage
	if err := k.c.GetJSON(ctx, "/rest/api/content/search", url.Values{"cql": {k.cql()}, "limit": {"1"}}, &p); err != nil {
		return connectors.Validation{}, err
	}
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	return connectors.Validation{User: name, Sample: max(p.TotalSize, p.Size)}, nil
}

// Scopes lists the spaces.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
	out := []connectors.Scope{}
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

func (k *Connector) search(ctx context.Context, cql, expand string, fn func(id, when string) error) error {
	const limit = 100
	for start := 0; ; start += limit {
		q := url.Values{"cql": {cql}, "start": {strconv.Itoa(start)}, "limit": {strconv.Itoa(limit)}}
		if expand != "" {
			q.Set("expand", expand)
		}
		var p searchPage
		if err := k.c.GetJSON(ctx, "/rest/api/content/search", q, &p); err != nil {
			return err
		}
		for _, r := range p.Results {
			if err := fn(r.ID, r.Version.When); err != nil {
				return err
			}
		}
		if len(p.Results) < limit {
			return nil
		}
	}
}

// Changed lists the pages modified at or after since. CQL dates use the
// time zone of the token user, so the query starts Overlap earlier.
func (k *Connector) Changed(ctx context.Context, since time.Time, fn func(connectors.Ref) error) error {
	cql := k.cql()
	if !since.IsZero() {
		cql += fmt.Sprintf(` AND lastmodified >= "%s"`, since.Add(-connectors.Overlap).UTC().Format("2006/01/02 15:04"))
	}
	cql += " order by lastmodified asc"
	return k.search(ctx, cql, "version", func(id, when string) error {
		t, _ := time.Parse(time.RFC3339, when)
		return fn(connectors.Ref{ID: id, Updated: t})
	})
}

// ListIDs lists all page IDs in scope.
func (k *Connector) ListIDs(ctx context.Context, fn func(string) error) error {
	return k.search(ctx, k.cql(), "", func(id, _ string) error { return fn(id) })
}

type person struct {
	Username    string `json:"username"`
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
	} `json:"metadata"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

// Fetch reads one page.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	var p pageJSON
	q := url.Values{"expand": {"body.storage,version,ancestors,metadata.labels,space,history"}}
	if err := k.c.GetJSON(ctx, "/rest/api/content/"+url.PathEscape(id), q, &p); err != nil {
		return nil, err
	}
	if k.limits.MaxItemBytes > 0 && len(p.Body.Storage.Value) > k.limits.MaxItemBytes {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	return k.item(&p)
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
		URL:    k.c.URL(p.Links.WebUI, nil),
		Author: p.History.CreatedBy.name(), EditedBy: p.Version.By.name(),
		Created: created, Updated: updated, Body: body,
		Metadata: map[string]any{"space": p.Space.Key, "spaceName": p.Space.Name, "ancestors": ancestors,
			"labels": labels, "version": p.Version.Number},
	}
	if strings.Contains(p.Links.WebUI, "?") {
		// The web UI link carries its own query.
		u, err := url.Parse(p.Links.WebUI)
		if err == nil {
			it.URL = k.c.URL(u.Path, u.Query())
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
