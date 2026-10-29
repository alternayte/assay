// Package github reads repositories on GitHub and GitHub Enterprise Server
// through REST v3 with a personal access token. The base URL is
// https://api.github.com for GitHub and https://<host>/api/v3 for
// Enterprise Server.
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/tree"
)

// Scope selects the repositories of a connection.
type Scope struct {
	Repositories []string `json:"repositories" doc:"Repositories as owner/name."`
}

// Host is the GitHub API of one connection.
type Host struct {
	c   *connectors.Client
	web string // host of the repository browse URLs
}

// New returns a connector. scope is the JSON scope of the connection.
func New(c *connectors.Client, scope json.RawMessage, g tree.Globs, limits connectors.Limits) (*tree.Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("github: the scope is not valid: %w", err)
		}
	}
	web := c.Host()
	if web == "api.github.com" {
		web = "github.com"
	}
	return tree.New(&Host{c: c, web: web}, s.Repositories, g, limits), nil
}

type repoJSON struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

func (h *Host) repo(r repoJSON) tree.Repo {
	return tree.Repo{ID: r.FullName, Name: r.Name, Branch: r.DefaultBranch, Key: "github:" + h.web + ":" + strings.ToLower(r.FullName)}
}

// Provider is github.
func (h *Host) Provider() string { return "github" }

// WebHost is the host of the browse URLs.
func (h *Host) WebHost() string { return h.web }

// User returns the login of the token.
func (h *Host) User(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := h.c.GetJSON(ctx, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Name != "" {
		return u.Name + " (" + u.Login + ")", nil
	}
	return u.Login, nil
}

// Repos lists the repositories of the token user.
func (h *Host) Repos(ctx context.Context) ([]tree.Repo, error) {
	const per = 100
	var out []tree.Repo
	for page := 1; ; page++ {
		var rs []repoJSON
		q := url.Values{"per_page": {strconv.Itoa(per)}, "page": {strconv.Itoa(page)}, "sort": {"full_name"}}
		if err := h.c.GetJSON(ctx, "/user/repos", q, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			out = append(out, h.repo(r))
		}
		if len(rs) < per {
			return out, nil
		}
	}
}

// Repo returns one repository.
func (h *Host) Repo(ctx context.Context, id string) (tree.Repo, error) {
	var r repoJSON
	if err := h.c.GetJSON(ctx, "/repos/"+id, nil, &r); err != nil {
		return tree.Repo{}, err
	}
	return h.repo(r), nil
}

// Tree lists the blobs of the default branch.
func (h *Host) Tree(ctx context.Context, r tree.Repo) ([]tree.Entry, error) {
	var t struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size int64  `json:"size"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	if err := h.c.GetJSON(ctx, "/repos/"+r.ID+"/git/trees/"+url.PathEscape(r.Branch), url.Values{"recursive": {"1"}}, &t); err != nil {
		return nil, err
	}
	if t.Truncated {
		return nil, fmt.Errorf("github: the tree of %s has more entries than GitHub lists in one answer", r.ID)
	}
	var out []tree.Entry
	for _, e := range t.Tree {
		if e.Type == "blob" {
			out = append(out, tree.Entry{Path: e.Path, Blob: e.SHA, Size: e.Size})
		}
	}
	return out, nil
}

// Blob returns the content of a blob.
func (h *Host) Blob(ctx context.Context, r tree.Repo, e tree.Entry) ([]byte, error) {
	var b struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := h.c.GetJSON(ctx, "/repos/"+r.ID+"/git/blobs/"+e.Blob, nil, &b); err != nil {
		return nil, err
	}
	if b.Encoding != "base64" {
		return []byte(b.Content), nil
	}
	out, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(b.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("github: blob %s of %s: %w", e.Blob, r.ID, err)
	}
	return out, nil
}

// FileURL is the browse URL of a file on the default branch.
func (h *Host) FileURL(r tree.Repo, file string) string {
	u := url.URL{Scheme: "https", Host: h.web, Path: "/" + r.ID + "/blob/" + r.Branch + "/" + file}
	if h.web != "github.com" {
		// Enterprise Server and the fixture serve the pages next to the API.
		base, _ := url.Parse(h.c.URL("", nil))
		u.Scheme = base.Scheme
		u.Path = strings.TrimSuffix(base.Path, "/api/v3") + u.Path
	}
	return u.String()
}
