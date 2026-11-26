package fixtures

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The Acme Cloud site. The fake Jira Cloud and Confluence Cloud APIs answer
// on the site under /cloud, and through the API host of scoped tokens
// under /atlassian/ex/jira/<CloudID> and /atlassian/ex/confluence/<CloudID>.
const (
	// CloudID is the cloud ID of the Acme site.
	CloudID = "a1c3e000-0000-4000-8000-00000000ac3e"
	// CloudSitePath is the path of the site on the fixture server.
	CloudSitePath = "/cloud"
	// CloudAPIPath is the path of the API host of scoped tokens on the
	// fixture server: it stands for https://api.atlassian.com.
	CloudAPIPath = "/atlassian"
)

// Email is the email of a corpus user.
func Email(name string) string { return name + "@acme.test" }

// AccountID is the Cloud account ID of a corpus user.
func AccountID(name string) string { return "712020:" + name }

// CloudPageID is the Cloud ID of a page. A migration to Cloud gives every
// page a new ID.
func CloudPageID(id string) string { return "33" + id }

func pageID(id string, cloud bool) string {
	if cloud {
		return CloudPageID(id)
	}
	return id
}

// HideEmail makes the Cloud answers leave out the email of a user, as the
// privacy setting of an Atlassian account does. ShowEmail undoes it.
func (s *Sources) HideEmail(name string) { s.setHidden(name, true) }

// ShowEmail shows the email of a user again.
func (s *Sources) ShowEmail(name string) { s.setHidden(name, false) }

func (s *Sources) setHidden(name string, hidden bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hidden[name] = hidden
}

// jiraPerson is a Jira Cloud user. The lock is held.
func (s *Sources) jiraPerson(name string) map[string]any {
	if name == "" {
		return nil
	}
	p := map[string]any{"accountId": AccountID(name), "accountType": "atlassian", "displayName": s.users[name], "active": true}
	if !s.hidden[name] {
		p["emailAddress"] = Email(name)
	}
	return p
}

// confluencePerson is a Confluence Cloud user. The lock is held.
func (s *Sources) confluencePerson(name string) map[string]any {
	if name == "" {
		return nil
	}
	return map[string]any{"type": "known", "accountId": AccountID(name), "accountType": "atlassian", "displayName": s.users[name]}
}

func (s *Sources) userByAccount(id string) (string, bool) {
	for name := range s.users {
		if AccountID(name) == id {
			return name, true
		}
	}
	return "", false
}

// cloudWebUI is the web link of a Cloud page, relative to /wiki.
func cloudWebUI(pg *Page) string {
	return "/spaces/" + pg.Space + "/pages/" + CloudPageID(pg.ID) + "/" + strings.ReplaceAll(url.PathEscape(pg.Title), "%20", "+")
}

// recordedADF is the recorded Cloud answer for the description and comments
// of a corpus issue, with the wiki markup they stand for.
type recordedADF struct {
	Description json.RawMessage   `json:"description"`
	Comments    []json.RawMessage `json:"comments"`
	wiki        string
	comments    []string
}

// loadADF reads the recorded ADF of the corpus issues. It runs after the
// issues are loaded.
func (s *Sources) loadADF() {
	b, err := corpus.ReadFile("corpus/jira-cloud/adf.json")
	if err != nil {
		panic(err)
	}
	var recs map[string]*recordedADF
	if err := json.Unmarshal(b, &recs); err != nil {
		panic(err)
	}
	for key, rec := range recs {
		is, ok := s.issues[key]
		if !ok {
			panic("fixtures: recorded ADF for an unknown issue " + key)
		}
		rec.wiki = is.Description
		for _, c := range is.Comments {
			rec.comments = append(rec.comments, c.Body)
		}
		s.adf[key] = rec
	}
}

// descriptionADF is the ADF description of an issue: the recorded answer
// while the description is the corpus one, else plain paragraphs. The lock
// is held.
func (s *Sources) descriptionADF(is *Issue) any {
	if rec, ok := s.adf[is.Key]; ok && rec.wiki == is.Description && len(rec.Description) > 0 {
		return rec.Description
	}
	return plainADF(is.Description)
}

// commentADF is the ADF body of comment i of an issue. The lock is held.
func (s *Sources) commentADF(key string, i int, body string) any {
	if rec, ok := s.adf[key]; ok && i < len(rec.Comments) && i < len(rec.comments) && rec.comments[i] == body {
		return rec.Comments[i]
	}
	return plainADF(body)
}

// plainADF is a document of plain paragraphs, one per block of lines, or
// nil for an empty text.
func plainADF(text string) any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var paras []map[string]any
	for _, block := range strings.Split(strings.TrimSpace(text), "\n\n") {
		var inline []map[string]any
		for i, line := range strings.Split(strings.TrimSpace(block), "\n") {
			if i > 0 {
				inline = append(inline, map[string]any{"type": "hardBreak"})
			}
			inline = append(inline, map[string]any{"type": "text", "text": line})
		}
		paras = append(paras, map[string]any{"type": "paragraph", "content": inline})
	}
	return map[string]any{"type": "doc", "version": 1, "content": paras}
}

// cloud serves the Acme Cloud site and the API host of scoped tokens. The
// tenant info of the site needs no token.
func (s *Sources) cloud(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, CloudSitePath+"/wiki/"):
		s.confluenceCloud(w, r, strings.TrimPrefix(p, CloudSitePath+"/wiki"))
	case strings.HasPrefix(p, CloudSitePath+"/"):
		s.jiraCloud(w, r, strings.TrimPrefix(p, CloudSitePath))
	case strings.HasPrefix(p, CloudAPIPath+"/ex/jira/"+CloudID+"/"):
		s.jiraCloud(w, r, strings.TrimPrefix(p, CloudAPIPath+"/ex/jira/"+CloudID))
	case strings.HasPrefix(p, CloudAPIPath+"/ex/confluence/"+CloudID+"/wiki/"):
		s.confluenceCloud(w, r, strings.TrimPrefix(p, CloudAPIPath+"/ex/confluence/"+CloudID+"/wiki"))
	default:
		http.NotFound(w, r)
	}
}

// tenantInfo answers the cloud ID of the site.
func tenantInfo(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{"cloudId": CloudID})
}

// jiraCloud serves Jira Cloud REST v3. The search with startAt is gone, as
// on Cloud: only search/jql answers, paged by nextPageToken without a
// total.
func (s *Sources) jiraCloud(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case p == "/rest/api/3/myself":
		writeJSON(w, http.StatusOK, map[string]any{"accountId": AccountID("assay.reader"), "accountType": "atlassian",
			"displayName": "Assay Reader", "emailAddress": Email("assay.reader")})
	case p == "/rest/api/3/project/search":
		start, limit := page(r, 50)
		vals := []map[string]any{}
		for i, pr := range window(s.projects, start, limit) {
			vals = append(vals, map[string]any{"id": strconv.Itoa(10000 + start + i), "key": pr[0], "name": pr[1]})
		}
		writeJSON(w, http.StatusOK, map[string]any{"startAt": start, "maxResults": limit, "total": len(s.projects),
			"isLast": start+len(vals) >= len(s.projects), "values": vals})
	case p == "/rest/api/3/search/jql":
		all, ok := s.matchIssues(r.URL.Query().Get("jql"))
		if !ok {
			badQuery(w, "Error in the JQL Query")
			return
		}
		_, limit := page(r, 50)
		limit = min(max(limit, 1), 100)
		start := 0
		if t := r.URL.Query().Get("nextPageToken"); t != "" {
			n, err := strconv.Atoi(strings.TrimPrefix(t, "acme-page-"))
			if err != nil || !strings.HasPrefix(t, "acme-page-") {
				badQuery(w, "The next page token is not valid.")
				return
			}
			start = n
		}
		res := []map[string]any{}
		for _, is := range window(all, start, limit) {
			res = append(res, map[string]any{"id": is.Key, "key": is.Key, "fields": map[string]any{"updated": is.Updated.Format(jiraTime)}})
		}
		out := map[string]any{"issues": res, "isLast": start+len(res) >= len(all)}
		if start+len(res) < len(all) {
			out["nextPageToken"] = "acme-page-" + strconv.Itoa(start+len(res))
		}
		writeJSON(w, http.StatusOK, out)
	case p == "/rest/api/3/search" || p == "/rest/api/2/search":
		writeJSON(w, http.StatusGone, map[string]any{"errorMessages": []string{
			"The requested API has been removed. Please migrate to the /rest/api/3/search/jql API."}})
	case strings.HasPrefix(p, "/rest/api/3/issue/") && strings.HasSuffix(p, "/comment"):
		s.comments(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "/rest/api/3/issue/"), "/comment"), true)
	case strings.HasPrefix(p, "/rest/api/3/issue/") && strings.HasSuffix(p, "/changelog"):
		s.changelog(w, r, strings.TrimSuffix(strings.TrimPrefix(p, "/rest/api/3/issue/"), "/changelog"))
	case strings.HasPrefix(p, "/rest/api/3/issue/"):
		s.issue(w, r, strings.TrimPrefix(p, "/rest/api/3/issue/"), true)
	default:
		http.NotFound(w, r)
	}
}

// spaceID is the Cloud ID of a space. The lock is held.
func (s *Sources) spaceID(key string) string {
	for i, sp := range s.spaces {
		if sp[0] == key {
			return strconv.Itoa(65536 + i)
		}
	}
	return ""
}

// cloudPage returns the page of a Cloud page ID. The lock is held.
func (s *Sources) cloudPage(id string) (*Page, bool) {
	if !strings.HasPrefix(id, "33") {
		return nil, false
	}
	pg, ok := s.pages[strings.TrimPrefix(id, "33")]
	return pg, ok
}

// cursorPage answers one page of a REST v2 listing. The cursor is the
// offset; the next link carries it, also in the Link header.
func cursorPage[T any](w http.ResponseWriter, r *http.Request, path string, all []T) {
	limit := 25
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = min(v, 250)
	}
	start := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(c, "acme-cursor-"))
		if err != nil {
			badQuery(w, "The cursor is not valid.")
			return
		}
		start = n
	}
	res := window(all, start, limit)
	links := map[string]any{"base": "http://ignored.acme.test/wiki"}
	if start+len(res) < len(all) {
		q := url.Values{"cursor": {"acme-cursor-" + strconv.Itoa(start+len(res))}, "limit": {strconv.Itoa(limit)}}
		next := "/wiki/api/v2" + path + "?" + q.Encode()
		links["next"] = next
		w.Header().Set("Link", "<"+next+`>; rel="next"`)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res, "_links": links})
}

// confluenceCloud serves Confluence Cloud below /wiki: the CQL search and
// users of REST v1, and the spaces and pages of REST v2.
func (s *Sources) confluenceCloud(w http.ResponseWriter, r *http.Request, p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case p == "/rest/api/user/current":
		writeJSON(w, http.StatusOK, map[string]any{"type": "known", "accountId": AccountID("assay.reader"), "accountType": "atlassian",
			"displayName": "Assay Reader", "email": Email("assay.reader")})
	case p == "/rest/api/user":
		name, ok := s.userByAccount(r.URL.Query().Get("accountId"))
		if !ok {
			http.Error(w, `{"statusCode":404,"message":"No user found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, s.confluencePerson(name))
	case p == "/rest/api/content/search":
		s.searchPages(w, r, true)
	case strings.HasPrefix(p, "/download/attachments/"):
		s.download(w, r, p, true)
	case strings.HasPrefix(p, "/api/v2/attachments/"):
		a, ok := s.attachmentByID(strings.TrimPrefix(p, "/api/v2/attachments/"), true)
		if !ok {
			http.Error(w, `{"errors":[{"status":404,"code":"NOT_FOUND","title":"Not Found"}]}`, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, attachmentV2(a))
	case p == "/api/v2/spaces":
		res := []map[string]any{}
		for _, sp := range s.spaces {
			res = append(res, map[string]any{"id": s.spaceID(sp[0]), "key": sp[0], "name": sp[1], "type": "global", "status": "current"})
		}
		cursorPage(w, r, "/spaces", res)
	case strings.HasPrefix(p, "/api/v2/spaces/"):
		id := strings.TrimPrefix(p, "/api/v2/spaces/")
		for _, sp := range s.spaces {
			if s.spaceID(sp[0]) == id {
				writeJSON(w, http.StatusOK, map[string]any{"id": id, "key": sp[0], "name": sp[1], "type": "global", "status": "current"})
				return
			}
		}
		http.Error(w, `{"errors":[{"status":404,"code":"NOT_FOUND","title":"Not Found"}]}`, http.StatusNotFound)
	case strings.HasPrefix(p, "/api/v2/pages/"):
		rest := strings.TrimPrefix(p, "/api/v2/pages/")
		id, sub, _ := strings.Cut(rest, "/")
		pg, ok := s.cloudPage(id)
		if !ok {
			http.Error(w, `{"errors":[{"status":404,"code":"NOT_FOUND","title":"Not Found"}]}`, http.StatusNotFound)
			return
		}
		switch sub {
		case "":
			writeJSON(w, http.StatusOK, s.pageV2(pg, r.URL.Query().Get("body-format") == "storage"))
		case "labels":
			res := []map[string]any{}
			for i, l := range pg.Labels {
				res = append(res, map[string]any{"id": strconv.Itoa(900000 + i), "name": l, "prefix": "global"})
			}
			cursorPage(w, r, "/pages/"+id+"/labels", res)
		case "ancestors":
			res := []map[string]any{}
			for _, a := range s.ancestors(pg) {
				res = append(res, map[string]any{"id": CloudPageID(a.ID), "type": "page"})
			}
			cursorPage(w, r, "/pages/"+id+"/ancestors", res)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

// pageV2 is a page of REST v2, with its storage body when asked. The lock
// is held.
func (s *Sources) pageV2(pg *Page, body bool) map[string]any {
	parent := ""
	if a := s.ancestors(pg); len(a) > 0 {
		parent = CloudPageID(a[len(a)-1].ID)
	}
	out := map[string]any{
		"id": CloudPageID(pg.ID), "status": "current", "title": pg.Title, "spaceId": s.spaceID(pg.Space),
		"parentId": parent, "parentType": "page", "authorId": AccountID(pg.Author), "ownerId": AccountID(pg.Author),
		"createdAt": pg.Created.Format(confluenceTime),
		"version":   map[string]any{"number": pg.Version, "createdAt": pg.Updated.Format(confluenceTime), "authorId": AccountID(pg.Author)},
		"body":      map[string]any{},
		"_links":    map[string]any{"webui": cloudWebUI(pg), "base": "http://ignored.acme.test/wiki"},
	}
	if body {
		out["body"] = map[string]any{"storage": map[string]any{"value": pg.Body, "representation": "storage"}}
	}
	return out
}
