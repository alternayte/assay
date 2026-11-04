// Package backstage reads the software catalog of a Backstage backend
// through the catalog API with a static external access token. Each
// catalog entity becomes an item whose body is the entity as YAML, with the
// same entities and facts as a catalog-info.yaml file in a repository.
package backstage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/catalog"
	"github.com/alternayte/assay/internal/connectors/code"
	"github.com/alternayte/assay/internal/content"
)

// Kinds are the catalog kinds that become items. Location, Template and
// other kinds are ignored.
var Kinds = []string{"Component", "API", "System", "Group", "User", "Resource", "Domain"}

// pageSize is the page size of a catalog query.
const pageSize = 500

// Scope selects the namespaces of a connection. Empty means all.
type Scope struct {
	Namespaces []string `json:"namespaces,omitempty" doc:"Catalog namespaces. Empty means all namespaces."`
}

// Options are the options of a connection.
type Options struct {
	FrontendURL string `json:"frontendUrl,omitempty" doc:"The Backstage web address for item links. Empty means the base URL."`
}

// Connector reads one Backstage connection.
type Connector struct {
	c        *connectors.Client
	scope    Scope
	frontend string
	limits   connectors.Limits
}

// New returns a connector. scope and options are the JSON scope and
// options of the connection.
func New(c *connectors.Client, scope, options json.RawMessage, limits connectors.Limits) (*Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("backstage: the scope is not valid: %w", err)
		}
	}
	var o Options
	if len(options) > 0 {
		if err := json.Unmarshal(options, &o); err != nil {
			return nil, fmt.Errorf("backstage: the options are not valid: %w", err)
		}
	}
	frontend := strings.TrimRight(o.FrontendURL, "/")
	if frontend == "" {
		frontend = c.URL("", nil)
	}
	return &Connector{c: c, scope: s, frontend: frontend, limits: limits}, nil
}

// filters are the filter parameters of a catalog query: one per kind and
// namespace. Backstage joins filter parameters with OR.
func (k *Connector) filters() []string {
	var out []string
	for _, kind := range Kinds {
		if len(k.scope.Namespaces) == 0 {
			out = append(out, "kind="+kind)
			continue
		}
		for _, ns := range k.scope.Namespaces {
			out = append(out, "kind="+kind+",metadata.namespace="+ns)
		}
	}
	return out
}

type queryPage struct {
	Items      []json.RawMessage `json:"items"`
	TotalItems int               `json:"totalItems"`
	PageInfo   struct {
		NextCursor string `json:"nextCursor"`
	} `json:"pageInfo"`
}

// query calls fn for each entity of the catalog query. A cursor carries
// the filters of the first request.
func (k *Connector) query(ctx context.Context, fields string, limit int, fn func(json.RawMessage) error) (int, error) {
	q := url.Values{"filter": k.filters(), "limit": {strconv.Itoa(limit)}}
	if fields != "" {
		q.Set("fields", fields)
	}
	total := 0
	for {
		var p queryPage
		if err := k.c.GetJSON(ctx, "/api/catalog/entities/by-query", q, &p); err != nil {
			return 0, err
		}
		total = p.TotalItems
		for _, it := range p.Items {
			if err := fn(it); err != nil {
				return total, err
			}
		}
		if p.PageInfo.NextCursor == "" || len(p.Items) == 0 || limit == 1 {
			return total, nil
		}
		q = url.Values{"cursor": {p.PageInfo.NextCursor}, "limit": {strconv.Itoa(limit)}}
		if fields != "" {
			q.Set("fields", fields)
		}
	}
}

// Validate checks the token and counts the entities in scope.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	n, err := k.query(ctx, "kind,metadata.namespace,metadata.name", 1, func(json.RawMessage) error { return nil })
	if err != nil {
		return connectors.Validation{}, err
	}
	return connectors.Validation{User: "Backstage", Sample: n}, nil
}

// Scopes lists the namespaces of the catalog.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
	var f struct {
		Facets map[string][]struct {
			Value string `json:"value"`
		} `json:"facets"`
	}
	if err := k.c.GetJSON(ctx, "/api/catalog/entity-facets", url.Values{"facet": {"metadata.namespace"}}, &f); err != nil {
		return nil, err
	}
	out := []connectors.Scope{}
	for _, v := range f.Facets["metadata.namespace"] {
		out = append(out, connectors.Scope{ID: v.Value, Name: v.Value})
	}
	slices.SortFunc(out, func(a, b connectors.Scope) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// head is the identity of a catalog entity.
type head struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Title     string `json:"title"`
		Etag      string `json:"etag"`
	} `json:"metadata"`
}

// ref is the entity ref of a catalog entity: kind:namespace/name in lower
// case. Other entities point at it, and it stays when the entity is
// registered again.
func (h head) ref() string {
	ns := h.Metadata.Namespace
	if ns == "" {
		ns = "default"
	}
	return strings.ToLower(h.Kind) + ":" + strings.ToLower(ns) + "/" + strings.ToLower(h.Metadata.Name)
}

// Changed lists every entity in scope with its item. The catalog has no
// updated-since filter: the etag is the revision, so sync skips the
// entities whose etag is stored.
func (k *Connector) Changed(ctx context.Context, _ time.Time, fn func(connectors.Ref) error) error {
	_, err := k.query(ctx, "", pageSize, func(raw json.RawMessage) error {
		var h head
		if err := json.Unmarshal(raw, &h); err != nil {
			return fmt.Errorf("backstage: an entity is not valid JSON: %w", err)
		}
		r := connectors.Ref{ID: h.ref(), Version: h.Metadata.Etag}
		if k.limits.MaxItemBytes > 0 && len(raw) > k.limits.MaxItemBytes {
			r.Skip = "too_large"
			return fn(r)
		}
		it, err := k.item(raw)
		if err != nil {
			return err
		}
		r.Item = it
		return fn(r)
	})
	return err
}

// ListIDs lists the entity refs in scope.
func (k *Connector) ListIDs(ctx context.Context, fn func(string) error) error {
	_, err := k.query(ctx, "kind,metadata.namespace,metadata.name", pageSize, func(raw json.RawMessage) error {
		var h head
		if err := json.Unmarshal(raw, &h); err != nil {
			return fmt.Errorf("backstage: an entity is not valid JSON: %w", err)
		}
		return fn(h.ref())
	})
	return err
}

// Fetch reads one entity by its ref.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	kind, rest, ok := strings.Cut(id, ":")
	ns, name, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 {
		return nil, connectors.ErrNotFound
	}
	b, err := k.c.Get(ctx, "/api/catalog/entities/by-name/"+url.PathEscape(kind)+"/"+url.PathEscape(ns)+"/"+url.PathEscape(name), nil,
		"application/json")
	if err != nil {
		return nil, err
	}
	if k.limits.MaxItemBytes > 0 && len(b) > k.limits.MaxItemBytes {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	return k.item(b)
}

// item turns a catalog entity into an item. The body is the entity as
// YAML, without its relations and status, split as a code file, so that
// the quotes of the facts are lines of the passages.
func (k *Connector) item(raw []byte) (*content.Item, error) {
	var h head
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, fmt.Errorf("backstage: an entity is not valid JSON: %w", err)
	}
	src, err := toYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("backstage: entity %s: %w", h.ref(), err)
	}
	ns := h.Metadata.Namespace
	if ns == "" {
		ns = "default"
	}
	title := h.Metadata.Title
	if title == "" {
		title = h.Metadata.Name
	}
	file := h.ref() + ".yaml"
	facts, err := catalog.Facts(src, catalog.Repository{})
	it := &content.Item{
		ExternalID: h.ref(), Kind: "catalog_entity", Title: title,
		URL:  k.frontend + "/catalog/" + url.PathEscape(ns) + "/" + url.PathEscape(strings.ToLower(h.Kind)) + "/" + url.PathEscape(h.Metadata.Name),
		Body: code.Body(file, src), Passages: code.Split(file, src), Revision: h.Metadata.Etag,
		Facts: facts, Entities: catalog.Entities(src), DropStale: true,
		Metadata: map[string]any{"kind": h.Kind, "namespace": ns, "name": h.Metadata.Name},
	}
	if err != nil {
		it.Metadata["parseError"] = err.Error()
	}
	return it, nil
}

// child returns the value of a key of a mapping node, or nil.
func child(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// toYAML writes a catalog entity as block YAML in the key order of the
// JSON, without relations, status and email addresses.
func toYAML(raw []byte) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the entity is not an object")
	}
	top := doc.Content[0]
	kept := top.Content[:0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if k := top.Content[i].Value; k == "relations" || k == "status" {
			continue
		}
		kept = append(kept, top.Content[i], top.Content[i+1])
	}
	top.Content = kept
	// Email addresses of users are not stored.
	if profile := child(child(top, "spec"), "profile"); profile != nil {
		kept := profile.Content[:0]
		for i := 0; i+1 < len(profile.Content); i += 2 {
			if profile.Content[i].Value != "email" {
				kept = append(kept, profile.Content[i], profile.Content[i+1])
			}
		}
		profile.Content = kept
	}
	var block func(n *yaml.Node)
	block = func(n *yaml.Node) {
		n.Style &^= yaml.FlowStyle | yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle
		if n.Kind == yaml.ScalarNode && n.Tag == "!!str" && strings.Contains(n.Value, "\n") {
			n.Style = yaml.LiteralStyle
		}
		for _, c := range n.Content {
			block(c)
		}
	}
	block(&doc)
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
