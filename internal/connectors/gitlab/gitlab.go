// Package gitlab reads repositories on GitLab and GitLab self-managed
// through REST v4. The base URL is the GitLab URL, such as
// https://gitlab.com or https://gitlab.acme.test. The client sends the
// token in the PRIVATE-TOKEN header.
package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/tree"
)

// TokenHeader is the header that carries the token.
const TokenHeader = "PRIVATE-TOKEN"

// api is the path of REST v4 below the GitLab URL.
const api = "/api/v4"

// perPage is the page size of every list. GitLab allows at most 100.
const perPage = 100

// Scope selects the repositories of a connection.
type Scope struct {
	Repositories []string `json:"repositories" doc:"Projects as the path with namespace, such as acme/payments/api."`
}

// Host is the GitLab API of one connection.
type Host struct {
	c *connectors.Client
	// web is the GitLab URL without a trailing slash, for browse URLs.
	web string
	// api is the path of REST v4 below the base URL: empty when the base
	// URL ends in /api/v4.
	api string
}

// New returns a connector. scope is the JSON scope of the connection.
func New(c *connectors.Client, scope json.RawMessage, g tree.Globs, limits connectors.Limits) (*tree.Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("gitlab: the scope is not valid: %w", err)
		}
	}
	h := &Host{c: c, web: strings.TrimRight(c.URL("", nil), "/"), api: api}
	if strings.HasSuffix(h.web, api) {
		h.web, h.api = strings.TrimSuffix(h.web, api), ""
	}
	return tree.New(h, s.Repositories, g, limits), nil
}

// Provider is gitlab.
func (h *Host) Provider() string { return "gitlab" }

// WebHost is the host of the connection URL.
func (h *Host) WebHost() string { return h.c.Host() }

type projectJSON struct {
	PathWithNamespace string `json:"path_with_namespace"`
	Path              string `json:"path"`
	DefaultBranch     string `json:"default_branch"`
}

func (h *Host) repo(p projectJSON) tree.Repo {
	return tree.Repo{ID: p.PathWithNamespace, Name: p.Path, Branch: p.DefaultBranch,
		Key: "gitlab:" + h.c.Host() + ":" + strings.ToLower(p.PathWithNamespace)}
}

// project is the API path of a project: the path with namespace,
// URL-encoded as one segment.
func (h *Host) project(id string) string {
	return h.api + "/projects/" + url.PathEscape(id)
}

// User returns the name and the username of the token user.
func (h *Host) User(ctx context.Context) (string, error) {
	var u struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := h.c.GetJSON(ctx, h.api+"/user", nil, &u); err != nil {
		return "", err
	}
	if u.Name != "" {
		return u.Name + " (" + u.Username + ")", nil
	}
	return u.Username, nil
}

// Repos lists the active projects that the token user is a member of.
// Keyset paging by id has no limit on the number of projects.
func (h *Host) Repos(ctx context.Context) ([]tree.Repo, error) {
	q := url.Values{"membership": {"true"}, "archived": {"false"}, "simple": {"true"}, "pagination": {"keyset"},
		"order_by": {"id"}, "sort": {"asc"}, "per_page": {strconv.Itoa(perPage)}}
	var out []tree.Repo
	err := h.pages(ctx, h.api+"/projects", q, func(raw json.RawMessage) error {
		var ps []projectJSON
		if err := json.Unmarshal(raw, &ps); err != nil {
			return err
		}
		for _, p := range ps {
			out = append(out, h.repo(p))
		}
		return nil
	})
	return out, err
}

// Repo returns one project with its default branch.
func (h *Host) Repo(ctx context.Context, id string) (tree.Repo, error) {
	var p projectJSON
	if err := h.c.GetJSON(ctx, h.project(id), nil, &p); err != nil {
		return tree.Repo{}, err
	}
	return h.repo(p), nil
}

// Tree lists the blobs of the default branch. A project without commits
// has no default branch and no files.
func (h *Host) Tree(ctx context.Context, r tree.Repo) ([]tree.Entry, error) {
	if r.Branch == "" {
		return nil, nil
	}
	q := url.Values{"recursive": {"true"}, "ref": {r.Branch}, "pagination": {"keyset"}, "per_page": {strconv.Itoa(perPage)}}
	var out []tree.Entry
	err := h.pages(ctx, h.project(r.ID)+"/repository/tree", q, func(raw json.RawMessage) error {
		var es []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &es); err != nil {
			return err
		}
		for _, e := range es {
			// GitLab lists no size. The item limits apply after Blob.
			if e.Type == "blob" {
				out = append(out, tree.Entry{Path: e.Path, Blob: e.ID, Size: -1})
			}
		}
		return nil
	})
	return out, err
}

// pages calls fn with each page of a keyset-paged list. The next page is
// the query of the rel="next" link of the Link header. The path stays the
// path of the first request, so that the project ID stays encoded.
func (h *Host) pages(ctx context.Context, path string, q url.Values, fn func(json.RawMessage) error) error {
	for {
		var raw json.RawMessage
		hdr, err := h.c.GetJSONHeader(ctx, path, q, &raw)
		if err != nil {
			return err
		}
		if err := fn(raw); err != nil {
			return fmt.Errorf("gitlab: the answer for %s is not valid: %w", path, err)
		}
		next := nextLink(hdr)
		if next == "" {
			return nil
		}
		u, err := url.Parse(next)
		if err != nil {
			return fmt.Errorf("gitlab: the next link of %s is not valid: %w", path, err)
		}
		q = u.Query()
	}
}

// nextLink returns the URL of the rel="next" link of a Link header, or "".
func nextLink(h http.Header) string {
	for _, v := range h.Values("Link") {
		for _, part := range strings.Split(v, ",") {
			target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
			if !ok {
				continue
			}
			for _, p := range strings.Split(params, ";") {
				k, val, _ := strings.Cut(strings.TrimSpace(p), "=")
				if strings.EqualFold(k, "rel") && strings.Trim(val, `"`) == "next" {
					return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(target), "<"), ">")
				}
			}
		}
	}
	return ""
}

// Blob returns the content of a blob by its ID from the tree.
func (h *Host) Blob(ctx context.Context, r tree.Repo, e tree.Entry) ([]byte, error) {
	return h.c.Get(ctx, h.project(r.ID)+"/repository/blobs/"+url.PathEscape(e.Blob)+"/raw", nil, "*/*")
}

// Commit returns the commit ID of the head of the default branch.
func (h *Host) Commit(ctx context.Context, r tree.Repo) (string, error) {
	var b struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := h.c.GetJSON(ctx, h.project(r.ID)+"/repository/branches/"+url.PathEscape(r.Branch), nil, &b); err != nil {
		return "", err
	}
	return b.Commit.ID, nil
}

// FileURL is the browse URL of a file on the default branch.
func (h *Host) FileURL(r tree.Repo, file string) string { return h.fileURL(r, r.Branch, file) }

// CommitURL is the browse URL of a file at the listed commit, or on the
// default branch when the commit is not known.
func (h *Host) CommitURL(r tree.Repo, file string) string {
	if r.Commit == "" {
		return h.FileURL(r, file)
	}
	return h.fileURL(r, r.Commit, file)
}

func (h *Host) fileURL(r tree.Repo, ref, file string) string {
	u, _ := url.Parse(h.web)
	u.Path = strings.TrimRight(u.Path, "/") + "/" + r.ID + "/-/blob/" + ref + "/" + file
	u.RawPath, u.RawQuery = "", ""
	return u.String()
}
