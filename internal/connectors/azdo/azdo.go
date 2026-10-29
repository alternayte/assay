// Package azdo reads Git repositories on Azure DevOps Server through the
// items API with Basic authentication and a personal access token. The
// base URL is the collection URL, such as https://ado.acme.test/tfs/Acme.
package azdo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/tree"
)

// DefaultAPIVersion is the api-version of a connection without the option.
const DefaultAPIVersion = "7.1"

// Scope selects the repositories of a connection.
type Scope struct {
	Repositories []string `json:"repositories" doc:"Repositories as project/name."`
}

// Options are the options of a connection.
type Options struct {
	APIVersion string `json:"apiVersion,omitempty" doc:"The api-version of the requests. The default is 7.1."`
}

// Host is the Azure DevOps Server API of one connection.
type Host struct {
	c       *connectors.Client
	version string
}

// New returns a connector. scope and options are the JSON scope and
// options of the connection.
func New(c *connectors.Client, scope, options json.RawMessage, g tree.Globs, limits connectors.Limits) (*tree.Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("azdo: the scope is not valid: %w", err)
		}
	}
	var o Options
	if len(options) > 0 {
		if err := json.Unmarshal(options, &o); err != nil {
			return nil, fmt.Errorf("azdo: the options are not valid: %w", err)
		}
	}
	if o.APIVersion = strings.TrimSpace(o.APIVersion); o.APIVersion == "" {
		o.APIVersion = DefaultAPIVersion
	}
	return tree.New(&Host{c: c, version: o.APIVersion}, s.Repositories, g, limits), nil
}

func (h *Host) query(extra url.Values) url.Values {
	q := url.Values{"api-version": {h.version}}
	for k, v := range extra {
		q[k] = v
	}
	return q
}

type repoJSON struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	Project       struct {
		Name string `json:"name"`
	} `json:"project"`
}

func (h *Host) repo(r repoJSON) tree.Repo {
	id := r.Project.Name + "/" + r.Name
	return tree.Repo{ID: id, Name: r.Name, Branch: strings.TrimPrefix(r.DefaultBranch, "refs/heads/"), Key: "azdo:" + h.c.Host() + ":" + strings.ToLower(id)}
}

// Provider is azdo.
func (h *Host) Provider() string { return "azdo" }

// WebHost is the host of the server.
func (h *Host) WebHost() string { return h.c.Host() }

// User returns the display name of the token user.
func (h *Host) User(ctx context.Context) (string, error) {
	var d struct {
		AuthenticatedUser struct {
			ProviderDisplayName string `json:"providerDisplayName"`
		} `json:"authenticatedUser"`
	}
	// connectionData is a preview API. A release api-version fails on it,
	// so this request has none.
	if err := h.c.GetJSON(ctx, "/_apis/connectionData", nil, &d); err != nil {
		return "", err
	}
	return d.AuthenticatedUser.ProviderDisplayName, nil
}

// Repos lists the repositories of the collection.
func (h *Host) Repos(ctx context.Context) ([]tree.Repo, error) {
	var rs struct {
		Value []repoJSON `json:"value"`
	}
	if err := h.c.GetJSON(ctx, "/_apis/git/repositories", h.query(nil), &rs); err != nil {
		return nil, err
	}
	out := make([]tree.Repo, 0, len(rs.Value))
	for _, r := range rs.Value {
		out = append(out, h.repo(r))
	}
	return out, nil
}

func repoPath(id string) (string, error) {
	project, name, ok := strings.Cut(id, "/")
	if !ok || project == "" || name == "" {
		return "", fmt.Errorf("azdo: the repository %q is not project/name", id)
	}
	return "/" + url.PathEscape(project) + "/_apis/git/repositories/" + url.PathEscape(name), nil
}

// Repo returns one repository.
func (h *Host) Repo(ctx context.Context, id string) (tree.Repo, error) {
	p, err := repoPath(id)
	if err != nil {
		return tree.Repo{}, err
	}
	var r repoJSON
	if err := h.c.GetJSON(ctx, p, h.query(nil), &r); err != nil {
		return tree.Repo{}, err
	}
	return h.repo(r), nil
}

// Tree lists the blobs of the default branch.
func (h *Host) Tree(ctx context.Context, r tree.Repo) ([]tree.Entry, error) {
	p, err := repoPath(r.ID)
	if err != nil {
		return nil, err
	}
	var items struct {
		Value []struct {
			ObjectID      string `json:"objectId"`
			GitObjectType string `json:"gitObjectType"`
			Path          string `json:"path"`
		} `json:"value"`
	}
	q := h.query(url.Values{"recursionLevel": {"Full"}, "versionDescriptor.version": {r.Branch},
		"versionDescriptor.versionType": {"branch"}})
	if err := h.c.GetJSON(ctx, p+"/items", q, &items); err != nil {
		return nil, err
	}
	var out []tree.Entry
	for _, it := range items.Value {
		if it.GitObjectType == "blob" {
			out = append(out, tree.Entry{Path: strings.TrimPrefix(it.Path, "/"), Blob: it.ObjectID, Size: -1})
		}
	}
	return out, nil
}

// Blob returns the content of a blob.
func (h *Host) Blob(ctx context.Context, r tree.Repo, e tree.Entry) ([]byte, error) {
	p, err := repoPath(r.ID)
	if err != nil {
		return nil, err
	}
	return h.c.Get(ctx, p+"/blobs/"+url.PathEscape(e.Blob), h.query(url.Values{"$format": {"octetstream"}}), "application/octet-stream")
}

// FileURL is the browse URL of a file on the default branch.
func (h *Host) FileURL(r tree.Repo, file string) string {
	project, name, _ := strings.Cut(r.ID, "/")
	return h.c.URL("/"+project+"/_git/"+name, url.Values{"path": {"/" + file}, "version": {"GB" + r.Branch}})
}
