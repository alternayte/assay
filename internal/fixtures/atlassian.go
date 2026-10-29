package fixtures

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func badQuery(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"message": msg})
}

func page(r *http.Request, def int) (start, limit int) {
	start, _ = strconv.Atoi(r.URL.Query().Get("start"))
	if v := r.URL.Query().Get("startAt"); v != "" {
		start, _ = strconv.Atoi(v)
	}
	limit = def
	for _, k := range []string{"limit", "maxResults"} {
		if v, err := strconv.Atoi(r.URL.Query().Get(k)); err == nil && v >= 0 {
			limit = v
		}
	}
	return max(start, 0), limit
}

func window[T any](all []T, start, limit int) []T {
	if start >= len(all) {
		return []T{}
	}
	return all[start:min(len(all), start+limit)]
}

func (s *Sources) person(name string) map[string]any {
	if name == "" {
		return nil
	}
	return map[string]any{"name": name, "username": name, "key": name, "displayName": s.users[name]}
}

const confluenceTime = "2006-01-02T15:04:05.000Z07:00"

var (
	cqlType     = regexp.MustCompile(`(?i)\btype\s*=\s*"?page"?`)
	cqlIn       = regexp.MustCompile(`(?i)\b(space|ancestor|label)\s+in\s*\(([^)]*)\)`)
	cqlEq       = regexp.MustCompile(`(?i)\b(space|ancestor|label)\s*=\s*("[^"]*"|[\w.-]+)`)
	cqlModified = regexp.MustCompile(`(?i)\blastmodified\s*>=\s*"([^"]+)"`)
	cqlOrder    = regexp.MustCompile(`(?i)\border\s+by\s+lastmodified(\s+(asc|desc))?\s*$`)
	cqlRest     = regexp.MustCompile(`(?i)\band\b|[()\s]`)
)

func values(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.Trim(strings.TrimSpace(v), `"`); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// cqlFilter parses the CQL subset that the connector sends.
func (s *Sources) cqlFilter(cql string) (func(*Page) bool, bool) {
	sets := map[string][]string{}
	var since time.Time
	rest := cql
	rest = cqlOrder.ReplaceAllString(rest, "")
	rest = cqlType.ReplaceAllString(rest, "")
	for _, m := range cqlIn.FindAllStringSubmatch(rest, -1) {
		sets[strings.ToLower(m[1])] = append(sets[strings.ToLower(m[1])], values(m[2])...)
	}
	rest = cqlIn.ReplaceAllString(rest, "")
	for _, m := range cqlEq.FindAllStringSubmatch(rest, -1) {
		sets[strings.ToLower(m[1])] = append(sets[strings.ToLower(m[1])], values(m[2])...)
	}
	rest = cqlEq.ReplaceAllString(rest, "")
	if m := cqlModified.FindStringSubmatch(rest); m != nil {
		t, err := time.Parse("2006/01/02 15:04", m[1])
		if err != nil {
			return nil, false
		}
		since = t
	}
	rest = cqlModified.ReplaceAllString(rest, "")
	if cqlRest.ReplaceAllString(rest, "") != "" {
		return nil, false
	}
	return func(p *Page) bool {
		if v := sets["space"]; v != nil && !slices.Contains(v, p.Space) {
			return false
		}
		if v := sets["label"]; v != nil && !slices.ContainsFunc(p.Labels, func(l string) bool { return slices.Contains(v, l) }) {
			return false
		}
		if v := sets["ancestor"]; v != nil && !slices.ContainsFunc(s.ancestors(p), func(a *Page) bool { return slices.Contains(v, a.ID) }) {
			return false
		}
		return !p.Updated.Truncate(time.Minute).Before(since)
	}, true
}

// ancestors returns the parent chain of a page, root first. The lock is held.
func (s *Sources) ancestors(p *Page) []*Page {
	var out []*Page
	for id := p.Parent; id != ""; {
		a, ok := s.pages[id]
		if !ok || len(out) > 20 {
			break
		}
		out = append([]*Page{a}, out...)
		id = a.Parent
	}
	return out
}

func (s *Sources) confluence(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/confluence")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case p == "/rest/api/user/current":
		writeJSON(w, http.StatusOK, map[string]any{"type": "known", "username": "assay.reader", "displayName": "Assay Reader"})
	case p == "/rest/api/space":
		start, limit := page(r, 25)
		res := []map[string]any{}
		for _, sp := range window(s.spaces, start, limit) {
			res = append(res, map[string]any{"key": sp[0], "name": sp[1], "type": "global"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": res, "start": start, "limit": limit, "size": len(res)})
	case p == "/rest/api/content/search":
		keep, ok := s.cqlFilter(r.URL.Query().Get("cql"))
		if !ok {
			badQuery(w, "Could not parse cql")
			return
		}
		var all []*Page
		for _, pg := range s.pages {
			if keep(pg) {
				all = append(all, pg)
			}
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].Updated.Equal(all[j].Updated) {
				return all[i].Updated.Before(all[j].Updated)
			}
			return all[i].ID < all[j].ID
		})
		start, limit := page(r, 25)
		res := []map[string]any{}
		for _, pg := range window(all, start, limit) {
			res = append(res, map[string]any{"id": pg.ID, "type": "page", "status": "current", "title": pg.Title,
				"version": map[string]any{"number": pg.Version, "when": pg.Updated.Format(confluenceTime)}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": res, "start": start, "limit": limit, "size": len(res), "totalSize": len(all)})
	case strings.HasPrefix(p, "/rest/api/content/"):
		id := strings.TrimPrefix(p, "/rest/api/content/")
		pg, ok := s.pages[id]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"statusCode":404,"message":"No content found with id"}`))
			return
		}
		anc := []map[string]any{}
		for _, a := range s.ancestors(pg) {
			anc = append(anc, map[string]any{"id": a.ID, "type": "page", "title": a.Title})
		}
		labels := []map[string]any{}
		for _, l := range pg.Labels {
			labels = append(labels, map[string]any{"prefix": "global", "name": l})
		}
		spaceName := ""
		for _, sp := range s.spaces {
			if sp[0] == pg.Space {
				spaceName = sp[1]
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": pg.ID, "type": "page", "status": "current", "title": pg.Title,
			"space":     map[string]any{"key": pg.Space, "name": spaceName},
			"history":   map[string]any{"createdBy": s.person(pg.Author), "createdDate": pg.Created.Format(confluenceTime)},
			"version":   map[string]any{"number": pg.Version, "when": pg.Updated.Format(confluenceTime), "by": s.person(pg.Author)},
			"ancestors": anc,
			"body":      map[string]any{"storage": map[string]any{"value": pg.Body, "representation": "storage"}},
			"metadata":  map[string]any{"labels": map[string]any{"results": labels, "size": len(labels)}},
			"_links":    map[string]any{"webui": "/pages/viewpage.action?pageId=" + pg.ID, "base": "http://ignored.acme.test/confluence"},
		})
	default:
		http.NotFound(w, r)
	}
}

const jiraTime = "2006-01-02T15:04:05.000-0700"

// epicField is the custom field of the Epic Link.
const epicField = "customfield_10100"

var (
	jqlProjectIn = regexp.MustCompile(`(?i)\bproject\s+in\s*\(([^)]*)\)`)
	jqlProjectEq = regexp.MustCompile(`(?i)\bproject\s*=\s*("[^"]*"|[\w-]+)`)
	jqlUpdated   = regexp.MustCompile(`(?i)\bupdated\s*>=\s*"([^"]+)"`)
	jqlOrder     = regexp.MustCompile(`(?i)\border\s+by\s+(updated|key)(\s+(asc|desc))?\s*$`)
)

func (s *Sources) jqlFilter(jql string) (func(*Issue) bool, bool) {
	var projects []string
	var since time.Time
	rest := jqlOrder.ReplaceAllString(jql, "")
	for _, m := range jqlProjectIn.FindAllStringSubmatch(rest, -1) {
		projects = append(projects, values(m[1])...)
	}
	rest = jqlProjectIn.ReplaceAllString(rest, "")
	for _, m := range jqlProjectEq.FindAllStringSubmatch(rest, -1) {
		projects = append(projects, values(m[1])...)
	}
	rest = jqlProjectEq.ReplaceAllString(rest, "")
	if m := jqlUpdated.FindStringSubmatch(rest); m != nil {
		t, err := time.Parse("2006/01/02 15:04", m[1])
		if err != nil {
			return nil, false
		}
		since = t
	}
	rest = jqlUpdated.ReplaceAllString(rest, "")
	if cqlRest.ReplaceAllString(rest, "") != "" {
		return nil, false
	}
	return func(is *Issue) bool {
		if projects != nil && !slices.Contains(projects, is.Project) {
			return false
		}
		return !is.Updated.Truncate(time.Minute).Before(since)
	}, true
}

func (s *Sources) issueRef(key string) map[string]any {
	is, ok := s.issues[key]
	if !ok {
		return map[string]any{"key": key}
	}
	return map[string]any{"key": key, "fields": map[string]any{"summary": is.Summary, "issuetype": map[string]any{"name": is.Type},
		"status": map[string]any{"name": is.Status}}}
}

func named(vs []string) []map[string]any {
	out := []map[string]any{}
	for _, v := range vs {
		out = append(out, map[string]any{"name": v})
	}
	return out
}

func (s *Sources) jira(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/jira")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case p == "/rest/api/2/myself":
		writeJSON(w, http.StatusOK, map[string]any{"name": "assay.reader", "key": "assay.reader", "displayName": "Assay Reader"})
	case p == "/rest/api/2/project":
		out := []map[string]any{}
		for i, pr := range s.projects {
			out = append(out, map[string]any{"id": strconv.Itoa(10000 + i), "key": pr[0], "name": pr[1]})
		}
		writeJSON(w, http.StatusOK, out)
	case p == "/rest/api/2/search":
		keep, ok := s.jqlFilter(r.URL.Query().Get("jql"))
		if !ok {
			badQuery(w, "Error in the JQL Query")
			return
		}
		var all []*Issue
		for _, is := range s.issues {
			if keep(is) {
				all = append(all, is)
			}
		}
		sort.Slice(all, func(i, j int) bool {
			if !all[i].Updated.Equal(all[j].Updated) {
				return all[i].Updated.Before(all[j].Updated)
			}
			return all[i].Key < all[j].Key
		})
		start, limit := page(r, 50)
		res := []map[string]any{}
		for _, is := range window(all, start, limit) {
			res = append(res, map[string]any{"id": is.Key, "key": is.Key, "fields": map[string]any{"updated": is.Updated.Format(jiraTime)}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"startAt": start, "maxResults": limit, "total": len(all), "issues": res})
	case strings.HasPrefix(p, "/rest/api/2/issue/") && strings.HasSuffix(p, "/comment"):
		key := strings.TrimSuffix(strings.TrimPrefix(p, "/rest/api/2/issue/"), "/comment")
		is, ok := s.issues[key]
		if !ok {
			http.Error(w, `{"errorMessages":["Issue Does Not Exist"]}`, http.StatusNotFound)
			return
		}
		cs := slices.Clone(is.Comments)
		if r.URL.Query().Get("orderBy") == "-created" {
			slices.Reverse(cs)
		}
		start, limit := page(r, 50)
		out := []map[string]any{}
		for _, c := range window(cs, start, limit) {
			out = append(out, map[string]any{"author": s.person(c.Author), "created": c.Created.Format(jiraTime), "body": c.Body})
		}
		writeJSON(w, http.StatusOK, map[string]any{"startAt": start, "maxResults": limit, "total": len(cs), "comments": out})
	case strings.HasPrefix(p, "/rest/api/2/issue/"):
		key := strings.TrimPrefix(p, "/rest/api/2/issue/")
		is, ok := s.issues[key]
		if !ok {
			http.Error(w, `{"errorMessages":["Issue Does Not Exist"]}`, http.StatusNotFound)
			return
		}
		links := []map[string]any{}
		for _, l := range is.Links {
			typ, target, _ := strings.Cut(l, " ")
			links = append(links, map[string]any{"type": map[string]any{"name": typ, "outward": strings.ToLower(typ), "inward": "is " + strings.ToLower(typ) + " by"},
				"outwardIssue": s.issueRef(target)})
		}
		for _, other := range s.issues {
			for _, l := range other.Links {
				typ, target, _ := strings.Cut(l, " ")
				if target == key {
					links = append(links, map[string]any{"type": map[string]any{"name": typ, "outward": strings.ToLower(typ), "inward": "is " + strings.ToLower(typ) + " by"},
						"inwardIssue": s.issueRef(other.Key)})
				}
			}
		}
		fields := map[string]any{
			"summary": is.Summary, "description": is.Description,
			"issuetype":  map[string]any{"name": is.Type},
			"status":     map[string]any{"name": is.Status},
			"priority":   map[string]any{"name": is.Priority},
			"assignee":   s.person(is.Assignee),
			"reporter":   s.person(is.Reporter),
			"labels":     append([]string{}, is.Labels...),
			"components": named(is.Components), "fixVersions": named(is.FixVersions),
			"duedate": nil, "issuelinks": links,
			"created": is.Created.Format(jiraTime), "updated": is.Updated.Format(jiraTime),
			epicField: nil,
		}
		if is.Due != "" {
			fields["duedate"] = is.Due
		}
		if is.Parent != "" {
			fields["parent"] = s.issueRef(is.Parent)
		}
		if is.Epic != "" {
			fields[epicField] = is.Epic
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": is.Key, "key": is.Key, "fields": fields,
			"names": map[string]any{"summary": "Summary", "status": "Status", epicField: "Epic Link"}})
	default:
		http.NotFound(w, r)
	}
}
