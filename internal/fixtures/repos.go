package fixtures

import (
	"crypto/sha1" //nolint:gosec // Git blob IDs are SHA-1.
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Repo is a Git repository of the corpus on the fake GitHub, Azure DevOps
// Server, GitLab or Bitbucket Data Center.
type Repo struct {
	Host   string // github, azdo, gitlab or bitbucket
	ID     string // owner/name, project/name, group/subgroup/project or KEY/slug
	Branch string
	Files  map[string][]byte
}

// AzdoCollection is the collection of the fake Azure DevOps Server.
const AzdoCollection = "AcmeCollection"

// GitLabRepo is the project of the fake GitLab, a project in a subgroup.
const GitLabRepo = "acme/payments/billing-api"

// BitbucketRepo is the repository of the fake Bitbucket Data Center.
const BitbucketRepo = "PLAT/billing-gateway"

// GitLabPadding and BitbucketPadding are the generated files that no glob
// selects. They make the GitLab tree longer than one page of 100 and the
// Bitbucket file list longer than one page of 500.
const (
	GitLabPadding    = 120
	BitbucketPadding = 520
)

// repoDepth is the number of path segments of a repository ID per host.
var repoDepth = map[string]int{"github": 2, "azdo": 2, "gitlab": 3, "bitbucket": 2}

// loadRepos reads corpus/<host>/<repository ID>/. A file name that ends in
// .fixture loses the suffix, so that go.mod and Go files in the corpus do
// not change the Go module.
func (s *Sources) loadRepos() {
	for _, host := range []string{"github", "azdo", "gitlab", "bitbucket"} {
		root := "corpus/" + host
		depth := repoDepth[host]
		_ = fs.WalkDir(corpus, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			parts := strings.SplitN(strings.TrimPrefix(p, root+"/"), "/", depth+1)
			if len(parts) < depth+1 {
				return nil
			}
			id := strings.Join(parts[:depth], "/")
			r, ok := s.repos[host+":"+id]
			if !ok {
				r = &Repo{Host: host, ID: id, Branch: "main", Files: map[string][]byte{}}
				s.repos[host+":"+id] = r
			}
			b, err := corpus.ReadFile(p)
			if err != nil {
				return err
			}
			r.Files[strings.TrimSuffix(parts[depth], ".fixture")] = b
			return nil
		})
	}
	s.repos["github:"+EmptyRepo] = &Repo{Host: "github", ID: EmptyRepo, Branch: "main", Files: map[string][]byte{}}
	pad(s.repos["gitlab:"+GitLabRepo], GitLabPadding)
	pad(s.repos["bitbucket:"+BitbucketRepo], BitbucketPadding)
}

// pad adds n icons under assets/. Their paths sort before catalog-info.yaml
// and docs/, so that the files of the globs come after the first page.
func pad(r *Repo, n int) {
	for i := range n {
		r.Files[fmt.Sprintf("assets/icon-%04d.svg", i)] = fmt.Appendf(nil, "<svg xmlns=\"http://www.w3.org/2000/svg\"><title>Acme icon %d</title></svg>\n", i)
	}
}

// EmptyRepo is a GitHub repository without commits. GitHub answers 409 for
// its tree and 404 for its default branch.
const EmptyRepo = "acme/sandbox"

// BlobID is the Git blob ID of a file.
func BlobID(b []byte) string {
	h := sha1.New() //nolint:gosec // Git blob IDs are SHA-1.
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// PutFile adds or changes a file of a repository.
func (s *Sources) PutFile(host, repo, file string, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[host+":"+repo].Files[file] = b
}

// DeleteFile removes a file of a repository.
func (s *Sources) DeleteFile(host, repo, file string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.repos[host+":"+repo].Files, file)
}

// DeleteRepo removes a repository, so that the API answers 404 for it.
func (s *Sources) DeleteRepo(host, repo string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.repos, host+":"+repo)
}

// RepoFiles returns the paths of the files of a repository, sorted.
func (s *Sources) RepoFiles(host, repo string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for p := range s.repos[host+":"+repo].Files {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func (s *Sources) reposOf(host string) []*Repo {
	var out []*Repo
	for _, r := range s.repos {
		if r.Host == host {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b *Repo) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (r *Repo) blob(id string) ([]byte, bool) {
	for _, b := range r.Files {
		if BlobID(b) == id {
			return b, true
		}
	}
	return nil, false
}

// dirs returns the directories of the files, without the root.
func (r *Repo) dirs() []string {
	seen := map[string]bool{}
	for p := range r.Files {
		for d := path.Dir(p); d != "." && !seen[d]; d = path.Dir(d) {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}

// commit is the commit ID of the branch head: it changes when a file
// changes.
func (r *Repo) commit() string {
	h := sha1.New() //nolint:gosec // Git commit IDs are SHA-1.
	for _, p := range r.paths() {
		_, _ = fmt.Fprintf(h, "%s %s\n", p, BlobID(r.Files[p]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *Repo) paths() []string {
	out := make([]string, 0, len(r.Files))
	for p := range r.Files {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// github serves the GitHub REST API. GitHub has it at the root of the API
// host, Enterprise Server below /api/v3. The fixture serves the first under
// /github and the second under /ghes/api/v3.
func (s *Sources) github(w http.ResponseWriter, r *http.Request, prefix, web string) {
	p := strings.TrimPrefix(r.URL.Path, prefix)
	s.mu.Lock()
	defer s.mu.Unlock()
	repoJSON := func(rp *Repo) map[string]any {
		_, name, _ := strings.Cut(rp.ID, "/")
		return map[string]any{"full_name": rp.ID, "name": name, "default_branch": rp.Branch,
			"html_url": origin(r) + web + "/" + rp.ID, "private": false}
	}
	switch p {
	case "/user":
		writeJSON(w, http.StatusOK, map[string]any{"login": "assay-reader", "name": "Assay Reader"})
		return
	case "/user/repos":
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		if per <= 0 {
			per = 30
		}
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		out := []map[string]any{}
		for _, rp := range window(s.reposOf("github"), (max(pg, 1)-1)*per, per) {
			out = append(out, repoJSON(rp))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	rest, ok := strings.CutPrefix(p, "/repos/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	parts := strings.SplitN(rest, "/", 5)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	rp, ok := s.repos["github:"+parts[0]+"/"+parts[1]]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		return
	}
	if len(parts) > 2 && s.githubHistory(w, r, rp, strings.Split(strings.Join(parts[2:], "/"), "/"), web) {
		return
	}
	switch {
	case len(parts) == 2:
		writeJSON(w, http.StatusOK, repoJSON(rp))
	case len(parts) == 4 && parts[2] == "branches":
		if parts[3] != rp.Branch || len(rp.Files) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Branch not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": rp.Branch, "commit": map[string]any{"sha": rp.commit()}})
	case len(parts) == 5 && parts[2] == "git" && parts[3] == "trees":
		if parts[4] != rp.Branch {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
			return
		}
		if len(rp.Files) == 0 {
			writeJSON(w, http.StatusConflict, map[string]any{"message": "Git Repository is empty."})
			return
		}
		entries := []map[string]any{}
		for _, d := range rp.dirs() {
			entries = append(entries, map[string]any{"path": d, "mode": "040000", "type": "tree", "sha": BlobID([]byte(d))})
		}
		for _, f := range rp.paths() {
			b := rp.Files[f]
			entries = append(entries, map[string]any{"path": f, "mode": "100644", "type": "blob", "sha": BlobID(b), "size": len(b)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"sha": BlobID([]byte(rp.ID)), "tree": entries, "truncated": false})
	case len(parts) == 5 && parts[2] == "git" && parts[3] == "blobs":
		b, ok := rp.blob(parts[4])
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
			return
		}
		enc := base64.StdEncoding.EncodeToString(b)
		var lines []string
		for len(enc) > 60 {
			lines, enc = append(lines, enc[:60]), enc[60:]
		}
		lines = append(lines, enc)
		writeJSON(w, http.StatusOK, map[string]any{"sha": parts[4], "size": len(b), "encoding": "base64", "content": strings.Join(lines, "\n") + "\n"})
	default:
		http.NotFound(w, r)
	}
}

// azdo serves the Azure DevOps Server Git API of one collection under
// /azdo/<collection>.
func (s *Sources) azdo(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/azdo/"+AzdoCollection)
	q := r.URL.Query()
	if (strings.Contains(p, "/_apis/git/") || strings.Contains(p, "/_apis/wit/") || strings.Contains(p, "/_apis/wiki/")) && q.Get("api-version") == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "No api-version was supplied for the request."})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repoJSON := func(rp *Repo) map[string]any {
		project, name, _ := strings.Cut(rp.ID, "/")
		return map[string]any{"id": BlobID([]byte(rp.ID))[:32], "name": name, "project": map[string]any{"name": project},
			"defaultBranch": "refs/heads/" + rp.Branch, "webUrl": origin(r) + "/azdo/" + AzdoCollection + "/" + project + "/_git/" + name}
	}
	switch p {
	case "/_apis/connectionData":
		writeJSON(w, http.StatusOK, map[string]any{"authenticatedUser": map[string]any{"providerDisplayName": "Assay Reader"}})
		return
	case "/_apis/git/repositories":
		out := []map[string]any{}
		for _, rp := range s.reposOf("azdo") {
			out = append(out, repoJSON(rp))
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "value": out})
		return
	}
	if project, rest, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/_apis/"); ok && s.azdoProject(w, r, project, rest) {
		return
	}
	project, rest, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/_apis/git/repositories/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if all := strings.Split(rest, "/"); len(all) > 1 {
		if rp, ok := s.repos["azdo:"+project+"/"+all[0]]; ok && s.azdoPulls(w, r, rp, all[1:]) {
			return
		}
	}
	parts := strings.SplitN(rest, "/", 3)
	rp, ok := s.repos["azdo:"+project+"/"+parts[0]]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "The repository does not exist."})
		return
	}
	switch {
	case len(parts) == 1:
		writeJSON(w, http.StatusOK, repoJSON(rp))
	case parts[1] == "items":
		if q.Get("versionDescriptor.version") != rp.Branch || q.Get("recursionLevel") != "Full" {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "The branch does not exist."})
			return
		}
		out := []map[string]any{{"objectId": BlobID([]byte("/")), "gitObjectType": "tree", "path": "/", "isFolder": true}}
		for _, d := range rp.dirs() {
			out = append(out, map[string]any{"objectId": BlobID([]byte(d)), "gitObjectType": "tree", "path": "/" + d, "isFolder": true})
		}
		for _, f := range rp.paths() {
			out = append(out, map[string]any{"objectId": BlobID(rp.Files[f]), "gitObjectType": "blob", "path": "/" + f})
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "value": out})
	case parts[1] == "commits":
		if q.Get("searchCriteria.itemVersion.version") != rp.Branch {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "The branch does not exist."})
			return
		}
		if file := strings.TrimPrefix(q.Get("searchCriteria.itemPath"), "/"); file != "" {
			if rp.Files[file] == nil {
				writeJSON(w, http.StatusOK, map[string]any{"count": 0, "value": []any{}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"count": 1, "value": []map[string]any{{"commitId": rp.commit(),
				"comment": s.history["azdo:"+rp.ID].commit(file)}}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": 1, "value": []map[string]any{{"commitId": rp.commit()}}})
	case parts[1] == "blobs" && len(parts) == 3:
		b, ok := rp.blob(parts[2])
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "The blob does not exist."})
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

// RepoFile returns the content of a file of a repository.
func (s *Sources) RepoFile(host, repo, file string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repos[host+":"+repo].Files[file]
}
