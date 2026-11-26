package fixtures

import (
	"crypto/sha1" //nolint:gosec // Git commit IDs are SHA-1.
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// OmitNextPageStart makes the Bitbucket file list leave out nextPageStart
// on pages that are not the last, as Bitbucket before 7.14 does.
func (s *Sources) OmitNextPageStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.noNextPageStart = true
}

// fileCommit is the last commit that changed a file: it changes only when
// the file changes.
func (r *Repo) fileCommit(file string) string {
	h := sha1.New() //nolint:gosec // Git commit IDs are SHA-1.
	_, _ = fmt.Fprintf(h, "commit %s %s\n", file, BlobID(r.Files[file]))
	return hex.EncodeToString(h.Sum(nil))
}

// bitbucketPage writes one page of a Bitbucket paged list. limit is 25 by
// default and at most 1000.
func bitbucketPage[T any](w http.ResponseWriter, q url.Values, all []T, omitNext bool) {
	start, _ := strconv.Atoi(q.Get("start"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 25
	}
	limit = min(limit, 1000)
	vs := window(all, start, limit)
	last := start+len(vs) >= len(all)
	page := map[string]any{"size": len(vs), "limit": limit, "start": start, "isLastPage": last, "values": vs}
	if !last && !omitNext {
		page["nextPageStart"] = start + len(vs)
	}
	writeJSON(w, http.StatusOK, page)
}

// bitbucket serves the Bitbucket Data Center REST 1.0 API and the raw file
// route under /bitbucket. Every answer names the token user in
// X-AUSERNAME.
func (s *Sources) bitbucket(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/bitbucket")
	q := r.URL.Query()
	w.Header().Set("X-AUSERNAME", "assay-reader")
	s.mu.Lock()
	defer s.mu.Unlock()
	repoJSON := func(rp *Repo) map[string]any {
		key, slug, _ := strings.Cut(rp.ID, "/")
		return map[string]any{"slug": slug, "name": slug, "scmId": "git", "state": "AVAILABLE",
			"project": map[string]any{"key": key, "name": key}}
	}
	notFound := func(msg string) {
		writeJSON(w, http.StatusNotFound, map[string]any{"errors": []map[string]any{{"message": msg}}})
	}
	switch p {
	case "/rest/api/1.0/repos":
		if q.Get("permission") != "REPO_READ" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"errors": []map[string]any{{"message": "the fixture needs permission=REPO_READ"}}})
			return
		}
		var out []map[string]any
		for _, rp := range s.reposOf("bitbucket") {
			out = append(out, repoJSON(rp))
		}
		bitbucketPage(w, q, out, false)
		return
	case "/rest/api/1.0/users/assay-reader":
		writeJSON(w, http.StatusOK, map[string]any{"name": "assay-reader", "slug": "assay-reader", "displayName": "Assay Reader"})
		return
	}
	api := strings.HasPrefix(p, "/rest/api/1.0/projects/")
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(p, "/rest/api/1.0"), "/projects/"), "/", 4)
	if len(parts) < 3 || parts[1] != "repos" || !strings.HasPrefix(p, "/projects/") && !api {
		http.NotFound(w, r)
		return
	}
	key, _ := url.PathUnescape(parts[0])
	slug, _ := url.PathUnescape(parts[2])
	rp, ok := s.repos["bitbucket:"+key+"/"+slug]
	if !ok {
		notFound("Repository " + key + "/" + slug + " does not exist.")
		return
	}
	sub := ""
	if len(parts) == 4 {
		sub = parts[3]
	}
	head := rp.commit()
	atHead := func() bool {
		at := q.Get("at")
		return at == head || at == rp.Branch || at == "refs/heads/"+rp.Branch
	}
	op, rest, _ := strings.Cut(sub, "/")
	file, err := url.PathUnescape(rest)
	if err != nil {
		notFound("The path is not valid.")
		return
	}
	switch {
	case api && sub == "":
		writeJSON(w, http.StatusOK, repoJSON(rp))
	case api && sub == "default-branch":
		if len(rp.Files) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "refs/heads/" + rp.Branch, "displayId": rp.Branch, "type": "BRANCH",
			"latestCommit": head, "latestChangeset": head, "isDefault": true})
	case api && sub == "files":
		if !atHead() {
			notFound("Commit " + q.Get("at") + " does not exist.")
			return
		}
		bitbucketPage(w, q, rp.paths(), s.noNextPageStart)
	case api && op == "last-modified":
		if !atHead() {
			notFound("Commit " + q.Get("at") + " does not exist.")
			return
		}
		dir := file
		if dir == "" {
			dir = "."
		}
		files := map[string]any{}
		for _, f := range rp.paths() {
			if path.Dir(f) == dir {
				c := rp.fileCommit(f)
				files[path.Base(f)] = map[string]any{"id": c, "displayId": c[:11]}
			}
		}
		if len(files) == 0 {
			notFound("The path " + dir + " does not exist.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"files": files, "latestCommit": map[string]any{"id": head, "displayId": head[:11]}})
	case !api && op == "raw":
		b, ok := rp.Files[file]
		if !ok || (q.Get("at") != rp.fileCommit(file) && !atHead()) {
			notFound("The path " + file + " does not exist at " + q.Get("at") + ".")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}
