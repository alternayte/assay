// Package bitbucket reads Git repositories on Bitbucket Data Center through
// REST 1.0 with an HTTP access token as a Bearer token. The base URL is the
// Bitbucket URL, such as https://bitbucket.acme.test.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/tree"
)

// api is the path of REST 1.0 below the Bitbucket URL.
const api = "/rest/api/1.0"

// pageLimit is the page size of every list.
const pageLimit = 500

// ErrNoNextPage is a page that is not the last page and has no
// nextPageStart. Bitbucket before 7.14 answers so, and a silent stop
// loses files.
var ErrNoNextPage = errors.New("Bitbucket returned no next page. Upgrade Bitbucket to 7.14 or later.") //nolint:staticcheck,revive // The message is for people.

// Scope selects the repositories of a connection.
type Scope struct {
	Repositories []string `json:"repositories" doc:"Repositories as project key/repository slug, such as PLAT/billing-gateway."`
}

// Host is the Bitbucket Data Center API of one connection.
type Host struct {
	c *connectors.Client
}

// New returns a connector. scope is the JSON scope of the connection.
func New(c *connectors.Client, scope json.RawMessage, g tree.Globs, limits connectors.Limits) (*tree.Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("bitbucket: the scope is not valid: %w", err)
		}
	}
	return tree.New(&Host{c: c}, s.Repositories, g, limits), nil
}

// Provider is bitbucket.
func (h *Host) Provider() string { return "bitbucket" }

// WebHost is the host of the connection URL.
func (h *Host) WebHost() string { return h.c.Host() }

type repoJSON struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Project struct {
		Key string `json:"key"`
	} `json:"project"`
}

func (h *Host) repo(r repoJSON) tree.Repo {
	id := r.Project.Key + "/" + r.Slug
	return tree.Repo{ID: id, Name: r.Slug, Key: "bitbucket:" + h.c.Host() + ":" + strings.ToLower(id)}
}

// repoPath is the path of a repository below a prefix: /projects/<key>/repos/<slug>.
func repoPath(id string) (string, error) {
	key, slug, ok := strings.Cut(id, "/")
	if !ok || key == "" || slug == "" || strings.Contains(slug, "/") {
		return "", fmt.Errorf("bitbucket: the repository %q is not project key/repository slug", id)
	}
	return "/projects/" + url.PathEscape(key) + "/repos/" + url.PathEscape(slug), nil
}

// escapePath escapes each segment of a file or directory path.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// User returns the display name and the slug of the token user. Bitbucket
// names the user of a request in the X-AUSERNAME header.
func (h *Host) User(ctx context.Context) (string, error) {
	var page json.RawMessage
	hdr, err := h.c.GetJSONHeader(ctx, api+"/repos", url.Values{"permission": {"REPO_READ"}, "limit": {"1"}}, &page)
	if err != nil {
		return "", err
	}
	slug := hdr.Get("X-AUSERNAME")
	if slug == "" {
		return "", errors.New("bitbucket: the server answered without a user. Check the token")
	}
	var u struct {
		DisplayName string `json:"displayName"`
	}
	// The bot user of a project or repository token has no user page.
	if err := h.c.GetJSON(ctx, api+"/users/"+url.PathEscape(slug), nil, &u); err != nil && !errors.Is(err, connectors.ErrNotFound) {
		return "", err
	}
	if u.DisplayName != "" {
		return u.DisplayName + " (" + slug + ")", nil
	}
	return slug, nil
}

// pages calls fn with the values of each page of a paged list. It
// continues from nextPageStart until isLastPage.
func (h *Host) pages(ctx context.Context, p string, q url.Values, fn func(json.RawMessage) error) error {
	start := 0
	for {
		pq := url.Values{"start": {strconv.Itoa(start)}, "limit": {strconv.Itoa(pageLimit)}}
		for k, v := range q {
			pq[k] = v
		}
		var page struct {
			Values        json.RawMessage `json:"values"`
			IsLastPage    bool            `json:"isLastPage"`
			NextPageStart *int            `json:"nextPageStart"`
		}
		if err := h.c.GetJSON(ctx, p, pq, &page); err != nil {
			return err
		}
		if err := fn(page.Values); err != nil {
			return fmt.Errorf("bitbucket: the answer for %s is not valid: %w", p, err)
		}
		if page.IsLastPage {
			return nil
		}
		if page.NextPageStart == nil {
			return ErrNoNextPage
		}
		if *page.NextPageStart <= start {
			return fmt.Errorf("bitbucket: the next page of %s starts at %d, not after %d", p, *page.NextPageStart, start)
		}
		start = *page.NextPageStart
	}
}

// Repos lists the repositories that the token reads.
func (h *Host) Repos(ctx context.Context) ([]tree.Repo, error) {
	var out []tree.Repo
	err := h.pages(ctx, api+"/repos", url.Values{"permission": {"REPO_READ"}}, func(raw json.RawMessage) error {
		var rs []repoJSON
		if err := json.Unmarshal(raw, &rs); err != nil {
			return err
		}
		for _, r := range rs {
			out = append(out, h.repo(r))
		}
		return nil
	})
	return out, err
}

// Repo returns one repository with its default branch and the head commit
// of that branch. A repository without commits has no default branch.
func (h *Host) Repo(ctx context.Context, id string) (tree.Repo, error) {
	p, err := repoPath(id)
	if err != nil {
		return tree.Repo{}, err
	}
	var rj repoJSON
	if err := h.c.GetJSON(ctx, api+p, nil, &rj); err != nil {
		return tree.Repo{}, err
	}
	r := h.repo(rj)
	b, err := h.defaultBranch(ctx, p)
	if err != nil {
		return tree.Repo{}, err
	}
	r.Branch, r.Commit = b.DisplayID, b.LatestCommit
	return r, nil
}

type branchJSON struct {
	DisplayID    string `json:"displayId"`
	LatestCommit string `json:"latestCommit"`
}

// defaultBranch returns the default branch of a repository. A repository
// without commits answers 404 or no content, and has no branch.
func (h *Host) defaultBranch(ctx context.Context, repo string) (branchJSON, error) {
	var b branchJSON
	raw, err := h.c.Get(ctx, api+repo+"/default-branch", nil, "application/json")
	if errors.Is(err, connectors.ErrNotFound) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return b, nil
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, fmt.Errorf("bitbucket: the default branch of %s is not valid JSON: %w", repo, err)
	}
	return b, nil
}

// Tree lists the files at the head commit of the default branch. The files
// endpoint has no blob IDs, so the version of each file is the last commit
// that changed it: it changes only when the file changes.
func (h *Host) Tree(ctx context.Context, r tree.Repo) ([]tree.Entry, error) {
	if r.Commit == "" {
		return nil, nil
	}
	p, err := repoPath(r.ID)
	if err != nil {
		return nil, err
	}
	at := url.Values{"at": {r.Commit}}
	var files []string
	if err := h.pages(ctx, api+p+"/files", at, func(raw json.RawMessage) error {
		var vs []string
		if err := json.Unmarshal(raw, &vs); err != nil {
			return err
		}
		files = append(files, vs...)
		return nil
	}); err != nil {
		return nil, err
	}
	commits := map[string]map[string]string{} // directory to file name to commit
	out := make([]tree.Entry, 0, len(files))
	for _, f := range files {
		dir := path.Dir(f)
		cs, ok := commits[dir]
		if !ok {
			if cs, err = h.lastModified(ctx, p, dir, r.Commit); err != nil {
				return nil, err
			}
			commits[dir] = cs
		}
		c := cs[path.Base(f)]
		if c == "" {
			return nil, fmt.Errorf("bitbucket: %s of %s has no last commit at %s", f, r.ID, r.Commit)
		}
		out = append(out, tree.Entry{Path: f, Blob: c, Size: -1})
	}
	return out, nil
}

// lastModified returns the last commit that changed each child of a
// directory, by child name.
func (h *Host) lastModified(ctx context.Context, repo, dir, commit string) (map[string]string, error) {
	p := api + repo + "/last-modified"
	if dir != "." {
		p += "/" + escapePath(dir)
	}
	var lm struct {
		Files map[string]struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := h.c.GetJSON(ctx, p, url.Values{"at": {commit}}, &lm); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(lm.Files))
	for name, c := range lm.Files {
		out[name] = c.ID
	}
	return out, nil
}

// Blob returns the bytes of a file at the last commit that changed it.
// /browse answers JSON lines, so Blob reads /raw.
func (h *Host) Blob(ctx context.Context, r tree.Repo, e tree.Entry) ([]byte, error) {
	p, err := repoPath(r.ID)
	if err != nil {
		return nil, err
	}
	return h.c.Get(ctx, p+"/raw/"+escapePath(e.Path), url.Values{"at": {e.Blob}}, "*/*")
}

// Commit returns the head commit of the default branch that the tree was
// listed at.
func (h *Host) Commit(ctx context.Context, r tree.Repo) (string, error) {
	if r.Commit != "" {
		return r.Commit, nil
	}
	p, err := repoPath(r.ID)
	if err != nil {
		return "", err
	}
	b, err := h.defaultBranch(ctx, p)
	return b.LatestCommit, err
}

// LastCommit returns the message of the last commit of the default branch
// that changed a file, or "" when Bitbucket lists none.
func (h *Host) LastCommit(ctx context.Context, r tree.Repo, file string) (string, error) {
	p, err := repoPath(r.ID)
	if err != nil {
		return "", err
	}
	var cs struct {
		Values []struct {
			Message string `json:"message"`
		} `json:"values"`
	}
	q := url.Values{"path": {file}, "until": {r.Branch}, "limit": {"1"}}
	if err := h.c.GetJSON(ctx, p+"/commits", q, &cs); err != nil {
		return "", err
	}
	if len(cs.Values) == 0 {
		return "", nil
	}
	return cs.Values[0].Message, nil
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
	p, err := repoPath(r.ID)
	if err != nil {
		return ""
	}
	return h.c.URL(p+"/browse/"+escapePath(file), url.Values{"at": {ref}})
}
