// Package tree reads the knowledge files and the source code of
// repositories. GitHub and Azure DevOps Server list the tree of a branch
// with a blob ID per file. Sync fetches only the files whose blob ID
// changed. Catalog and CODEOWNERS files become facts. Source files become
// items of kind code with one passage per declaration.
package tree

import (
	"context"
	"errors"
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
	"github.com/alternayte/assay/internal/connectors/code"
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
	// Commit is the head commit of the branch when the tree was listed.
	Commit string
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
	// Commit returns the head commit of the default branch.
	Commit(ctx context.Context, r Repo) (string, error)
	// CommitURL is the browse URL of a file at the commit of the listing.
	CommitURL(r Repo, file string) string
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

// DefaultCodeGlobs are the default code globs of a new repository
// connection: the source files of Go, TypeScript, JavaScript, C# and
// Python, and the manifests that mark components.
var DefaultCodeGlobs = []string{"**/*.go", "**/*.{ts,tsx,mts,cts}", "**/*.{js,jsx,mjs,cjs}", "**/*.cs", "**/*.py",
	"**/go.mod", "**/package.json", "**/*.csproj", "**/pyproject.toml"}

// DefaultSkipGlobs are the paths of generated and vendored code that sync
// skips with the reason generated.
var DefaultSkipGlobs = []string{"**/vendor/**", "**/node_modules/**", "**/dist/**", "**/bin/**", "**/obj/**", "**/*.min.js"}

// Globs select the files of a repository. A file that matches Include is
// a knowledge file. Otherwise a file that matches Code is a source file.
// Exclude applies to both, and Skip removes generated code.
type Globs struct {
	Include []string
	Exclude []string
	Code    []string
	Skip    []string
}

func matchAny(pats []string, p string) bool {
	for _, pat := range pats {
		if ok, _ := doublestar.Match(pat, p); ok {
			return true
		}
	}
	return false
}

// Match reports whether a path matches an include glob and no exclude
// glob. Globs use ** for any number of directories and {a,b} for choices.
func (g Globs) Match(p string) bool {
	return matchAny(g.Include, p) && !matchAny(g.Exclude, p)
}

// MatchCode reports whether a path is a source file: it matches a code
// glob, no include glob, no exclude glob and no skip glob.
func (g Globs) MatchCode(p string) bool {
	return matchAny(g.Code, p) && !matchAny(g.Include, p) && !matchAny(g.Exclude, p) && !matchAny(g.Skip, p)
}

// skipped returns the skip glob that a code path matches, or "".
func (g Globs) skipped(p string) string {
	if !matchAny(g.Code, p) || matchAny(g.Include, p) || matchAny(g.Exclude, p) {
		return ""
	}
	for _, pat := range g.Skip {
		if ok, _ := doublestar.Match(pat, p); ok {
			return pat
		}
	}
	return ""
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
	repo  Repo
	files []Entry
	index map[string]int
	// rank is the position of a file among the files of its kind:
	// knowledge files and source files have their own limits.
	rank    map[string]int
	code    map[string]bool
	owners  string         // the CODEOWNERS file that GitHub uses
	skipped map[string]int // skip glob to the number of files
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
// files whose stored revision differs. Files past the file limit stay in
// the list, so that the run shows them as skipped.
func (k *Connector) Changed(ctx context.Context, _ time.Time, fn func(connectors.Ref) error) error {
	return k.each(ctx, false, func(id string, e Entry) error { return fn(connectors.Ref{ID: id, Version: e.Blob}) })
}

// ListIDs lists the matching files up to the file limit. Reconciliation
// tombstones a stored file past the limit, because sync no longer updates
// it.
func (k *Connector) ListIDs(ctx context.Context, fn func(string) error) error {
	return k.each(ctx, true, func(id string, _ Entry) error { return fn(id) })
}

// each calls fn for the files of each selected repository. A repository
// that the host no longer has gives no files, so that reconciliation
// tombstones its files and the other repositories still sync.
func (k *Connector) each(ctx context.Context, limit bool, fn func(string, Entry) error) error {
	for _, id := range k.repos {
		l, err := k.list(ctx, id, true)
		if errors.Is(err, connectors.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		for _, e := range l.files {
			if limit && k.overLimit(l, e.Path) {
				continue
			}
			if err := fn(ItemID(id, e.Path), e); err != nil {
				return err
			}
		}
	}
	return nil
}

// overLimit reports whether a file is past the file limit of its kind.
func (k *Connector) overLimit(l *listing, file string) bool {
	maxFiles := k.limits.MaxFilesPerRepo
	if l.code[file] {
		maxFiles = k.limits.MaxCodeFilesPerRepo
	}
	return maxFiles > 0 && l.rank[file] >= maxFiles
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
	if len(k.globs.Code) > 0 {
		if r.Commit, err = k.host.Commit(ctx, r); err != nil {
			return nil, err
		}
	}
	l = &listing{repo: r, index: map[string]int{}, rank: map[string]int{}, code: map[string]bool{}, skipped: map[string]int{}}
	for _, e := range entries {
		switch {
		case k.globs.Match(e.Path):
			l.files = append(l.files, e)
		case k.globs.MatchCode(e.Path):
			l.files = append(l.files, e)
			l.code[e.Path] = true
		default:
			if g := k.globs.skipped(e.Path); g != "" {
				l.skipped[g]++
			}
		}
	}
	slices.SortFunc(l.files, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	knowledge, code := 0, 0
	for i, e := range l.files {
		l.index[e.Path] = i
		if l.code[e.Path] {
			l.rank[e.Path] = code
			code++
		} else {
			l.rank[e.Path] = knowledge
			knowledge++
		}
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
	isCode := l.code[file]
	maxBytes := k.limits.MaxItemBytes
	if isCode {
		maxBytes = k.limits.MaxCodeItemBytes
	}
	if k.overLimit(l, file) {
		return nil, &connectors.SkipError{Reason: "file_limit"}
	}
	e := l.files[i]
	if maxBytes > 0 && e.Size > int64(maxBytes) {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	b, err := k.host.Blob(ctx, l.repo, e)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && len(b) > maxBytes {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	b = []byte(strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n"))
	if !utf8.Valid(b) || slices.Contains(b, 0) {
		return nil, &connectors.SkipError{Reason: "binary"}
	}
	repo := content.EntityRef{Type: "repository", Key: l.repo.Key, Name: l.repo.Name, Aliases: []string{l.repo.ID}}
	if isCode {
		if Generated(b) {
			return nil, &connectors.SkipError{Reason: "generated"}
		}
		return &content.Item{
			ExternalID: id, Kind: "code", Title: l.repo.ID + "/" + file, URL: k.host.CommitURL(l.repo, file),
			Body: code.Body(file, b), Passages: code.Split(file, b), Revision: e.Blob, Entities: []content.EntityRef{repo},
			Metadata: map[string]any{"repository": l.repo.ID, "repositoryKey": l.repo.Key, "path": file, "branch": l.repo.Branch,
				"commit": l.repo.Commit, "language": code.FenceLanguage(file)},
		}, nil
	}
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

// Generated reports whether a source file says in its first lines that a
// tool wrote it.
func Generated(b []byte) bool {
	head := string(b[:min(len(b), 2048)])
	lines := strings.SplitN(head, "\n", 6)
	for _, l := range lines[:min(len(lines), 5)] {
		if strings.Contains(l, "Code generated") || strings.Contains(l, "<auto-generated") {
			return true
		}
	}
	return false
}

// Skipped lists the source files that the skip globs removed from the last
// listings: one entry per repository and glob, with the number of files.
func (k *Connector) Skipped() []connectors.Skipped {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []connectors.Skipped
	for _, id := range k.repos {
		l, ok := k.listings[id]
		if !ok {
			continue
		}
		globs := make([]string, 0, len(l.skipped))
		for g := range l.skipped {
			globs = append(globs, g)
		}
		slices.Sort(globs)
		for _, g := range globs {
			out = append(out, connectors.Skipped{ID: fmt.Sprintf("%s:%s (%d files)", id, g, l.skipped[g]), Reason: "generated"})
		}
	}
	return out
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
