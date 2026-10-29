// Package tree reads the knowledge files of repositories. GitHub and Azure
// DevOps Server list the tree of a branch with a blob ID per file. Sync
// fetches only the files whose blob ID changed. Catalog and CODEOWNERS
// files become facts.
package tree

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	gosync "sync"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/catalog"
	"github.com/alternayte/assay/internal/connectors/codeowners"
	"github.com/alternayte/assay/internal/content"
)

// Repo is one repository of a host.
type Repo struct {
	// ID is owner/name on GitHub and project/name on Azure DevOps. It is
	// the prefix of the item IDs.
	ID     string
	Name   string
	Branch string
	// Key is the provider-qualified entity key of the repository.
	Key string
}

// Entry is one file of a tree.
type Entry struct {
	Path string
	Blob string
	// Size is the file size in bytes, or -1 when the host does not list it.
	Size int64
}

// Host is one repository host. GitHub and Azure DevOps Server implement it.
type Host interface {
	// User returns the source user of the token.
	User(ctx context.Context) (string, error)
	// Repos lists the repositories that the token reads.
	Repos(ctx context.Context) ([]Repo, error)
	// Repo returns one repository with its default branch.
	Repo(ctx context.Context, id string) (Repo, error)
	// Tree lists the files of the default branch.
	Tree(ctx context.Context, r Repo) ([]Entry, error)
	// Blob returns the content of a file.
	Blob(ctx context.Context, r Repo, e Entry) ([]byte, error)
	// FileURL is the browse URL of a file.
	FileURL(r Repo, file string) string
	// Provider and WebHost qualify entity keys, such as github and
	// github.com.
	Provider() string
	WebHost() string
}

// DefaultGlobs are the default include globs of a repository connection
// (SDD section 8).
var DefaultGlobs = []string{"README*", "docs/**/*.md", "**/adr/**/*.md", "**/decisions/**/*.md", "CODEOWNERS", ".github/CODEOWNERS",
	"**/catalog-info.yaml", "**/openapi*.{yaml,yml,json}", "**/*.proto", "go.mod", "package.json", "*.csproj",
	"Directory.Packages.props", "Chart.yaml", "**/values*.yaml", "deploy/**/*.yaml", "k8s/**/*.yaml"}

// Globs select the files of a repository.
type Globs struct {
	Include []string
	Exclude []string
}

// Match reports whether a path matches an include glob and no exclude
// glob. Globs use ** for any number of directories and {a,b} for choices.
func (g Globs) Match(p string) bool {
	in := false
	for _, pat := range g.Include {
		if ok, _ := doublestar.Match(pat, p); ok {
			in = true
			break
		}
	}
	if !in {
		return false
	}
	for _, pat := range g.Exclude {
		if ok, _ := doublestar.Match(pat, p); ok {
			return false
		}
	}
	return true
}

// Connector reads the selected repositories of a host.
type Connector struct {
	host   Host
	repos  []string
	globs  Globs
	limits connectors.Limits

	mu       gosync.Mutex
	listings map[string]*listing
}

// listing is the matched files of one repository, sorted by path.
type listing struct {
	repo   Repo
	files  []Entry
	index  map[string]int
	owners string // the CODEOWNERS file that GitHub uses
}

// New returns a connector for the repositories with the IDs in repos.
func New(h Host, repos []string, g Globs, limits connectors.Limits) *Connector {
	return &Connector{host: h, repos: repos, globs: g, limits: limits, listings: map[string]*listing{}}
}

// Validate checks the token. The sample is the number of matching files in
// the selected repositories, or the number of repositories when none is
// selected.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	user, err := k.host.User(ctx)
	if err != nil {
		return connectors.Validation{}, err
	}
	if len(k.repos) == 0 {
		rs, err := k.host.Repos(ctx)
		if err != nil {
			return connectors.Validation{}, err
		}
		return connectors.Validation{User: user, Sample: len(rs)}, nil
	}
	n := 0
	for _, id := range k.repos {
		l, err := k.list(ctx, id, true)
		if err != nil {
			return connectors.Validation{}, err
		}
		n += len(l.files)
	}
	return connectors.Validation{User: user, Sample: n}, nil
}

// Scopes lists the repositories.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
	rs, err := k.host.Repos(ctx)
	if err != nil {
		return nil, err
	}
	out := []connectors.Scope{}
	for _, r := range rs {
		out = append(out, connectors.Scope{ID: r.ID, Name: r.Name})
	}
	return out, nil
}

// Changed lists every matching file with its blob ID as the version. A
// tree has no update times, so since is not used. Sync fetches only the
// files whose stored revision differs.
func (k *Connector) Changed(ctx context.Context, _ time.Time, fn func(connectors.Ref) error) error {
	return k.each(ctx, func(id string, e Entry) error { return fn(connectors.Ref{ID: id, Version: e.Blob}) })
}

// ListIDs lists every matching file.
func (k *Connector) ListIDs(ctx context.Context, fn func(string) error) error {
	return k.each(ctx, func(id string, _ Entry) error { return fn(id) })
}

func (k *Connector) each(ctx context.Context, fn func(string, Entry) error) error {
	for _, id := range k.repos {
		l, err := k.list(ctx, id, true)
		if err != nil {
			return err
		}
		for _, e := range l.files {
			if err := fn(ItemID(id, e.Path), e); err != nil {
				return err
			}
		}
	}
	return nil
}

// ItemID is the external ID of a file.
func ItemID(repo, file string) string { return repo + ":" + file }

// list returns the listing of a repository. fresh reads the tree again.
func (k *Connector) list(ctx context.Context, id string, fresh bool) (*listing, error) {
	k.mu.Lock()
	l, ok := k.listings[id]
	k.mu.Unlock()
	if ok && !fresh {
		return l, nil
	}
	r, err := k.host.Repo(ctx, id)
	if err != nil {
		return nil, err
	}
	entries, err := k.host.Tree(ctx, r)
	if err != nil {
		return nil, err
	}
	l = &listing{repo: r, index: map[string]int{}}
	for _, e := range entries {
		if k.globs.Match(e.Path) {
			l.files = append(l.files, e)
		}
	}
	slices.SortFunc(l.files, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	for i, e := range l.files {
		l.index[e.Path] = i
	}
	for _, p := range []string{".github/CODEOWNERS", "CODEOWNERS", "docs/CODEOWNERS"} {
		if _, ok := l.index[p]; ok {
			l.owners = p
			break
		}
	}
	k.mu.Lock()
	k.listings[id] = l
	k.mu.Unlock()
	return l, nil
}

// Fetch reads one file. A file that is gone from the tree, or from the
// selected repositories, returns ErrNotFound.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	repoID, file, ok := strings.Cut(id, ":")
	if !ok || !slices.Contains(k.repos, repoID) {
		return nil, connectors.ErrNotFound
	}
	l, err := k.list(ctx, repoID, false)
	if err != nil {
		return nil, err
	}
	i, ok := l.index[file]
	if !ok {
		return nil, connectors.ErrNotFound
	}
	if k.limits.MaxFilesPerRepo > 0 && i >= k.limits.MaxFilesPerRepo {
		return nil, &connectors.SkipError{Reason: "file_limit"}
	}
	e := l.files[i]
	if k.limits.MaxItemBytes > 0 && e.Size > int64(k.limits.MaxItemBytes) {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	b, err := k.host.Blob(ctx, l.repo, e)
	if err != nil {
		return nil, err
	}
	if k.limits.MaxItemBytes > 0 && len(b) > k.limits.MaxItemBytes {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	b = []byte(strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n"))
	if !utf8.Valid(b) || slices.Contains(b, 0) {
		return nil, &connectors.SkipError{Reason: "binary"}
	}
	repo := content.EntityRef{Type: "repository", Key: l.repo.Key, Name: l.repo.Name, Aliases: []string{l.repo.ID}}
	it := &content.Item{
		ExternalID: id, Kind: "file", Title: l.repo.ID + "/" + file, URL: k.host.FileURL(l.repo, file),
		Body: Body(file, b), Revision: e.Blob,
		Metadata: map[string]any{"repository": l.repo.ID, "path": file, "branch": l.repo.Branch},
	}
	switch {
	case path.Dir(file) == "." && strings.HasPrefix(path.Base(file), "README"):
		it.Entity = &repo
	case path.Base(file) == "catalog-info.yaml":
		facts, err := catalog.Facts(b, catalog.Repository{Ref: repo, GitHubHost: k.githubHost()})
		if err != nil {
			it.Metadata["parseError"] = err.Error()
		}
		it.Facts = facts
		it.Entities = catalog.Entities(b)
	case file == l.owners:
		it.Facts = codeowners.Facts(b, repo, k.host.Provider(), k.host.WebHost())
	}
	return it, nil
}

func (k *Connector) githubHost() string {
	if k.host.Provider() == "github" {
		return k.host.WebHost()
	}
	return ""
}

// languages are the code fence languages by file extension.
var languages = map[string]string{".yaml": "yaml", ".yml": "yaml", ".json": "json", ".proto": "protobuf",
	".csproj": "xml", ".props": "xml", ".xml": "xml", ".mod": "go-mod", ".txt": "text"}

// Body returns the normalized Markdown body of a file. Markdown files stay
// as they are. Other files become one fenced code block.
func Body(file string, b []byte) string {
	s := strings.TrimRight(string(b), "\n")
	switch strings.ToLower(path.Ext(file)) {
	case ".md", ".markdown", ".mdx":
		return s
	}
	lang := languages[strings.ToLower(path.Ext(file))]
	if path.Base(file) == "go.mod" {
		lang = "go-mod"
	}
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fmt.Sprintf("%s%s\n%s\n%s", fence, lang, s, fence)
}
