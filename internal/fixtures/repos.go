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

// Repo is a Git repository of the corpus on the fake GitHub or Azure
// DevOps Server.
type Repo struct {
	Host   string // github or azdo
	ID     string // owner/name or project/name
	Branch string
	Files  map[string][]byte
}

// AzdoCollection is the collection of the fake Azure DevOps Server.
const AzdoCollection = "AcmeCollection"

// loadRepos reads corpus/<host>/<owner>/<name>/. A file name that ends in
// .fixture loses the suffix, so that go.mod and Go files in the corpus do
// not change the Go module.
func (s *Sources) loadRepos() {
	for _, host := range []string{"github", "azdo"} {
		root := "corpus/" + host
		_ = fs.WalkDir(corpus, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			parts := strings.SplitN(strings.TrimPrefix(p, root+"/"), "/", 3)
			if len(parts) < 3 {
				return nil
			}
			id := parts[0] + "/" + parts[1]
			r, ok := s.repos[host+":"+id]
			if !ok {
				r = &Repo{Host: host, ID: id, Branch: "main", Files: map[string][]byte{}}
				s.repos[host+":"+id] = r
			}
			b, err := corpus.ReadFile(p)
			if err != nil {
				return err
			}
			r.Files[strings.TrimSuffix(parts[2], ".fixture")] = b
			return nil
		})
	}
}

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
	switch {
	case len(parts) == 2:
		writeJSON(w, http.StatusOK, repoJSON(rp))
	case len(parts) == 5 && parts[2] == "git" && parts[3] == "trees":
		if parts[4] != rp.Branch {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
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
	if strings.Contains(p, "/_apis/git/") && q.Get("api-version") == "" {
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
	project, rest, ok := strings.Cut(strings.TrimPrefix(p, "/"), "/_apis/git/repositories/")
	if !ok {
		http.NotFound(w, r)
		return
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
