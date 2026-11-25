package fixtures

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// gitlabPerPage returns the per_page of a GitLab list: 20 by default and
// at most 100.
func gitlabPerPage(q url.Values) int {
	per, _ := strconv.Atoi(q.Get("per_page"))
	if per <= 0 {
		return 20
	}
	return min(per, 100)
}

// gitlabNext sets the Link header of a keyset page: the request URL with
// the next key.
func gitlabNext(w http.ResponseWriter, r *http.Request, key, value string) {
	q := r.URL.Query()
	q.Set(key, value)
	next := origin(r) + r.URL.EscapedPath() + "?" + q.Encode()
	w.Header().Set("Link", "<"+next+`>; rel="next"`)
}

// gitlab serves the GitLab REST v4 API under /gitlab/api/v4. A project ID
// is the URL-encoded path with namespace, one path segment, as GitLab
// takes it.
func (s *Sources) gitlab(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.EscapedPath(), "/gitlab/api/v4")
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	projects := s.reposOf("gitlab")
	projectJSON := func(rp *Repo) map[string]any {
		i := slices.Index(projects, rp)
		path := rp.ID[strings.LastIndex(rp.ID, "/")+1:]
		var branch any = rp.Branch
		if len(rp.Files) == 0 {
			branch = nil
		}
		return map[string]any{"id": i + 1, "name": strings.ReplaceAll(path, "-", " "), "path": path, "path_with_namespace": rp.ID,
			"default_branch": branch, "web_url": origin(r) + "/gitlab/" + rp.ID}
	}
	switch p {
	case "/user":
		writeJSON(w, http.StatusOK, map[string]any{"id": 7, "username": "assay-reader", "name": "Assay Reader"})
		return
	case "/projects":
		if q.Get("pagination") != "keyset" || q.Get("order_by") != "id" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "the fixture serves only keyset paging by id"})
			return
		}
		after, _ := strconv.Atoi(q.Get("id_after"))
		per := gitlabPerPage(q)
		out := []map[string]any{}
		for i := after; i < len(projects) && len(out) < per; i++ {
			out = append(out, projectJSON(projects[i]))
		}
		if after+len(out) < len(projects) {
			gitlabNext(w, r, "id_after", strconv.Itoa(after+len(out)))
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	rest, ok := strings.CutPrefix(p, "/projects/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	enc, sub, _ := strings.Cut(rest, "/")
	id, err := url.PathUnescape(enc)
	rp, found := s.repos["gitlab:"+id]
	if err != nil || !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Project Not Found"})
		return
	}
	switch {
	case sub == "":
		writeJSON(w, http.StatusOK, projectJSON(rp))
	case sub == "repository/tree":
		if q.Get("ref") != rp.Branch || q.Get("recursive") != "true" || len(rp.Files) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Tree Not Found"})
			return
		}
		if q.Get("pagination") != "keyset" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "the fixture serves only keyset paging"})
			return
		}
		var entries []map[string]any
		for _, d := range rp.dirs() {
			entries = append(entries, map[string]any{"id": BlobID([]byte(d)), "name": d[strings.LastIndex(d, "/")+1:], "type": "tree", "path": d,
				"mode": "040000"})
		}
		for _, f := range rp.paths() {
			entries = append(entries, map[string]any{"id": BlobID(rp.Files[f]), "name": f[strings.LastIndex(f, "/")+1:], "type": "blob", "path": f,
				"mode": "100644"})
		}
		slices.SortFunc(entries, func(a, b map[string]any) int { return strings.Compare(a["path"].(string), b["path"].(string)) })
		start := 0
		if tok := q.Get("page_token"); tok != "" {
			start = slices.IndexFunc(entries, func(e map[string]any) bool { return e["path"] == tok }) + 1
		}
		page := window(entries, start, gitlabPerPage(q))
		if start+len(page) < len(entries) {
			gitlabNext(w, r, "page_token", page[len(page)-1]["path"].(string))
		}
		writeJSON(w, http.StatusOK, page)
	case strings.HasPrefix(sub, "repository/blobs/") && strings.HasSuffix(sub, "/raw"):
		b, ok := rp.blob(strings.TrimSuffix(strings.TrimPrefix(sub, "repository/blobs/"), "/raw"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Blob Not Found"})
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
	case strings.HasPrefix(sub, "repository/branches/"):
		name, _ := url.PathUnescape(strings.TrimPrefix(sub, "repository/branches/"))
		if name != rp.Branch || len(rp.Files) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "404 Branch Not Found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": rp.Branch, "default": true, "commit": map[string]any{"id": rp.commit()}})
	default:
		http.NotFound(w, r)
	}
}
