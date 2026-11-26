package fixtures

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Note is a review, a review comment or a comment of the history corpus.
// Age is in days before the history base time.
type Note struct {
	Author string `json:"author"`
	Age    int    `json:"age"`
	Path   string `json:"path"`
	Body   string `json:"body"`
}

// PullRequest is a pull request of the history corpus. State is merged,
// closed or open on GitHub and completed, active or abandoned on Azure
// DevOps. Age is the days from its last update, its merge or its
// completion to the history base time.
type PullRequest struct {
	Number         int      `json:"number"`
	State          string   `json:"state"`
	Age            int      `json:"age"`
	Title          string   `json:"title"`
	Author         string   `json:"author"`
	Files          []string `json:"files"`
	Body           string   `json:"body"`
	Reviews        []Note   `json:"reviews"`
	ReviewComments []Note   `json:"reviewComments"`
	Comments       []Note   `json:"comments"`
	// CrossRefs are the issues whose text names the pull request. They
	// are cross-referenced events of its timeline.
	CrossRefs []int `json:"crossRefs"`
	// WorkItems are the work items that an Azure DevOps pull request links.
	WorkItems []int `json:"workItems"`
}

// GitIssue is a GitHub issue of the history corpus.
type GitIssue struct {
	Number   int      `json:"number"`
	State    string   `json:"state"`
	Age      int      `json:"age"`
	Title    string   `json:"title"`
	Author   string   `json:"author"`
	Labels   []string `json:"labels"`
	Body     string   `json:"body"`
	Comments []Note   `json:"comments"`
}

// RepoHistory is the history of one repository: its pull requests, its
// issues and the message of the last commit of each file. A file without
// a message has the message "Add <path>".
type RepoHistory struct {
	Pulls   []PullRequest     `json:"pulls"`
	Issues  []GitIssue        `json:"issues"`
	Commits map[string]string `json:"commits"`
}

// WorkItem is an Azure DevOps work item of the history corpus.
type WorkItem struct {
	ID          int    `json:"id"`
	Type        string `json:"type"`
	Age         int    `json:"age"`
	Title       string `json:"title"`
	State       string `json:"state"`
	AssignedTo  string `json:"assignedTo"`
	Description string `json:"description"`
}

// WikiPage is a page of the project wiki of an Azure DevOps project.
type WikiPage struct {
	ID      int    `json:"id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ProjectHistory is the work items and the wiki of an Azure DevOps
// project. Generate adds that many generated work items after the listed
// ones.
type ProjectHistory struct {
	WorkItems []WorkItem `json:"workItems"`
	Generate  int        `json:"generate"`
	Wiki      []WikiPage `json:"wiki"`
}

// loadHistory reads corpus/history/<host>/<owner>/<name>.json for each
// repository and corpus/history/azdo/<project>.json for each project.
func (s *Sources) loadHistory() {
	s.history = map[string]*RepoHistory{}
	s.azdoProjects = map[string]*ProjectHistory{}
	_ = fs.WalkDir(corpus, "corpus/history", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		b, err := corpus.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(p, "corpus/history/"), ".json")
		host, id, _ := strings.Cut(rel, "/")
		if !strings.Contains(id, "/") {
			var ph ProjectHistory
			if err := json.Unmarshal(b, &ph); err != nil {
				panic(fmt.Sprintf("fixtures: %s: %v", p, err))
			}
			for i := range ph.Generate {
				n := 9 + i
				ph.WorkItems = append(ph.WorkItems, WorkItem{ID: n, Type: []string{"Task", "Bug", "User Story"}[i%3], Age: 1 + i%300,
					Title: workItemName(n), State: []string{"New", "Active", "Closed"}[i%3], AssignedTo: "sam.acme",
					Description: fmt.Sprintf("<p>Check the ledger balance of account group %d.</p>", i%40)})
			}
			s.azdoProjects[id] = &ph
			return nil
		}
		var rh RepoHistory
		if err := json.Unmarshal(b, &rh); err != nil {
			panic(fmt.Sprintf("fixtures: %s: %v", p, err))
		}
		s.history[host+":"+id] = &rh
		return nil
	})
}

// workItemName is the title of a generated work item: a made-up word that
// shares no trigrams with the other titles, so that resolution does not
// propose to merge the generated work items.
func workItemName(n int) string {
	const consonants, vowels = "bdfgklmnprstvz", "aeiou"
	x := uint32(n)*2654435761 + 97 //nolint:gosec // n is a small positive work item ID.
	b := make([]byte, 9)
	for i := range b {
		x = x*1664525 + 1013904223
		if i%2 == 0 {
			b[i] = consonants[(x>>16)%uint32(len(consonants))]
		} else {
			b[i] = vowels[(x>>16)%uint32(len(vowels))]
		}
	}
	b[0] -= 'a' - 'A'
	return string(b)
}

// historyAt is the time of an age in days before the history base time.
func (s *Sources) historyAt(age int) time.Time {
	return s.historyBase.Add(-time.Duration(age) * 24 * time.Hour)
}

// HistoryItems is the number of history items that a repository connection
// with the default options stores for a repository: the merged or
// completed pull requests and the GitHub issues inside 24 months.
func (s *Sources) HistoryItems(host, repo string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.history[host+":"+repo]
	if h == nil {
		return 0
	}
	start := s.historyBase.AddDate(0, -24, 0)
	n := 0
	for _, p := range h.Pulls {
		if (p.State == "merged" || p.State == "completed") && !s.historyAt(p.Age).Before(start) {
			n++
		}
	}
	if host == "github" {
		for _, is := range h.Issues {
			if !s.historyAt(is.Age).Before(start) {
				n++
			}
		}
	}
	return n
}

// ProjectItems is the number of work items and wiki pages of an Azure
// DevOps project inside 24 months.
func (s *Sources) ProjectItems(project string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	ph := s.azdoProjects[project]
	if ph == nil {
		return 0
	}
	start := s.historyBase.AddDate(0, -24, 0)
	n := len(ph.Wiki)
	for _, w := range ph.WorkItems {
		if !s.historyAt(w.Age).Before(start) {
			n++
		}
	}
	return n
}

// PutPull adds or changes a pull request of a repository.
func (s *Sources) PutPull(host, repo string, p PullRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.history[host+":"+repo]
	if h == nil {
		h = &RepoHistory{}
		s.history[host+":"+repo] = h
	}
	h.Pulls = slices.DeleteFunc(h.Pulls, func(x PullRequest) bool { return x.Number == p.Number })
	h.Pulls = append(h.Pulls, p)
}

// DeletePull removes a pull request, so that the API answers 404 for it.
func (s *Sources) DeletePull(host, repo string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.history[host+":"+repo]; h != nil {
		h.Pulls = slices.DeleteFunc(h.Pulls, func(x PullRequest) bool { return x.Number == n })
	}
}

func (s *Sources) pullOf(h *RepoHistory, n int) (PullRequest, bool) {
	if h == nil {
		return PullRequest{}, false
	}
	for _, p := range h.Pulls {
		if p.Number == n {
			return p, true
		}
	}
	return PullRequest{}, false
}

func (h *RepoHistory) issue(n int) (GitIssue, bool) {
	if h == nil {
		return GitIssue{}, false
	}
	for _, is := range h.Issues {
		if is.Number == n {
			return is, true
		}
	}
	return GitIssue{}, false
}

func (h *RepoHistory) commit(file string) string {
	if h != nil {
		if m, ok := h.Commits[file]; ok {
			return m
		}
	}
	return "Add " + file
}

func ghPage[T any](r *http.Request, all []T) []T {
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if per <= 0 {
		per = 30
	}
	pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
	return window(all, (max(pg, 1)-1)*per, per)
}

func ghTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// githubHistory serves the pulls, issues and commits routes of a GitHub
// repository. It reports false for another route.
func (s *Sources) githubHistory(w http.ResponseWriter, r *http.Request, rp *Repo, rest []string, web string) bool {
	h := s.history["github:"+rp.ID]
	html := origin(r) + web + "/" + rp.ID
	user := func(login string) map[string]any { return map[string]any{"login": login} }
	pullJSON := func(p PullRequest) map[string]any {
		at := s.historyAt(p.Age)
		out := map[string]any{"number": p.Number, "title": p.Title, "body": p.Body, "state": "closed", "html_url": fmt.Sprintf("%s/pull/%d", html, p.Number),
			"user": user(p.Author), "created_at": ghTime(at.Add(-48 * time.Hour)), "updated_at": ghTime(at), "merged_at": nil,
			"merge_commit_sha": nil, "draft": false, "base": map[string]any{"ref": rp.Branch}}
		switch p.State {
		case "merged":
			out["merged_at"] = ghTime(at)
			out["merge_commit_sha"] = BlobID([]byte(fmt.Sprintf("%s#%d", rp.ID, p.Number)))
		case "open":
			out["state"] = "open"
		}
		return out
	}
	issueJSON := func(is GitIssue) map[string]any {
		labels := []map[string]any{}
		for _, l := range is.Labels {
			labels = append(labels, map[string]any{"name": l})
		}
		return map[string]any{"number": is.Number, "title": is.Title, "body": is.Body, "state": is.State, "user": user(is.Author),
			"html_url": fmt.Sprintf("%s/issues/%d", html, is.Number), "comments": len(is.Comments), "labels": labels,
			"created_at": ghTime(s.historyAt(is.Age + 5)), "updated_at": ghTime(s.historyAt(is.Age)),
			"repository": map[string]any{"full_name": rp.ID}}
	}
	// The issues endpoint returns pull requests too, with pull_request set.
	asIssue := func(p PullRequest) map[string]any {
		at := s.historyAt(p.Age)
		return map[string]any{"number": p.Number, "title": p.Title, "body": p.Body, "state": "closed", "user": user(p.Author),
			"html_url": fmt.Sprintf("%s/pull/%d", html, p.Number), "comments": len(p.Comments), "labels": []any{},
			"created_at": ghTime(at.Add(-48 * time.Hour)), "updated_at": ghTime(at), "pull_request": map[string]any{"url": ""}}
	}
	note := func(n Note) map[string]any {
		return map[string]any{"user": user(n.Author), "body": n.Body, "path": n.Path, "created_at": ghTime(s.historyAt(n.Age)),
			"submitted_at": ghTime(s.historyAt(n.Age))}
	}
	notes := func(ns []Note) []map[string]any {
		out := []map[string]any{}
		for _, n := range ns {
			out = append(out, note(n))
		}
		return out
	}
	q := r.URL.Query()
	switch {
	case len(rest) == 1 && rest[0] == "commits":
		if q.Get("sha") != rp.Branch || rp.Files[q.Get("path")] == nil {
			writeJSON(w, http.StatusOK, []any{})
			return true
		}
		writeJSON(w, http.StatusOK, []map[string]any{{"sha": rp.commit(), "commit": map[string]any{"message": h.commit(q.Get("path"))}}})
	case len(rest) == 1 && rest[0] == "pulls":
		var ps []PullRequest
		if h != nil {
			for _, p := range h.Pulls {
				if q.Get("state") == "closed" && p.State == "open" {
					continue
				}
				ps = append(ps, p)
			}
		}
		desc := q.Get("direction") != "asc"
		slices.SortFunc(ps, func(a, b PullRequest) int {
			if desc {
				return a.Age - b.Age
			}
			return b.Age - a.Age
		})
		out := []map[string]any{}
		for _, p := range ghPage(r, ps) {
			out = append(out, pullJSON(p))
		}
		writeJSON(w, http.StatusOK, out)
	case len(rest) >= 2 && rest[0] == "pulls":
		n, _ := strconv.Atoi(rest[1])
		p, ok := s.pullOf(h, n)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
			return true
		}
		switch {
		case len(rest) == 2:
			writeJSON(w, http.StatusOK, pullJSON(p))
		case rest[2] == "files":
			files := []map[string]any{}
			for _, f := range p.Files {
				files = append(files, map[string]any{"filename": f, "status": "modified"})
			}
			writeJSON(w, http.StatusOK, ghPage(r, files))
		case rest[2] == "comments":
			writeJSON(w, http.StatusOK, ghPage(r, notes(p.ReviewComments)))
		case rest[2] == "reviews":
			writeJSON(w, http.StatusOK, ghPage(r, notes(p.Reviews)))
		default:
			http.NotFound(w, r)
		}
	case len(rest) == 1 && rest[0] == "issues":
		since, _ := time.Parse(time.RFC3339, q.Get("since"))
		var all []map[string]any
		var ages []int
		if h != nil {
			for _, is := range h.Issues {
				if !s.historyAt(is.Age).Before(since) {
					all, ages = append(all, issueJSON(is)), append(ages, is.Age)
				}
			}
			for _, p := range h.Pulls {
				if !s.historyAt(p.Age).Before(since) {
					all, ages = append(all, asIssue(p)), append(ages, p.Age)
				}
			}
		}
		idx := make([]int, len(all))
		for i := range idx {
			idx[i] = i
		}
		slices.SortStableFunc(idx, func(a, b int) int { return ages[b] - ages[a] })
		sorted := []map[string]any{}
		for _, i := range idx {
			sorted = append(sorted, all[i])
		}
		writeJSON(w, http.StatusOK, ghPage(r, sorted))
	case len(rest) >= 2 && rest[0] == "issues":
		n, _ := strconv.Atoi(rest[1])
		is, isIssue := h.issue(n)
		p, isPull := s.pullOf(h, n)
		if !isIssue && !isPull {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
			return true
		}
		switch {
		case len(rest) == 2 && isIssue:
			writeJSON(w, http.StatusOK, issueJSON(is))
		case len(rest) == 2:
			writeJSON(w, http.StatusOK, asIssue(p))
		case rest[2] == "comments" && isIssue:
			writeJSON(w, http.StatusOK, ghPage(r, notes(is.Comments)))
		case rest[2] == "comments":
			writeJSON(w, http.StatusOK, ghPage(r, notes(p.Comments)))
		case rest[2] == "timeline":
			events := []map[string]any{}
			if isPull {
				for _, ref := range p.CrossRefs {
					if src, ok := h.issue(ref); ok {
						events = append(events, map[string]any{"event": "cross-referenced", "source": map[string]any{"type": "issue", "issue": issueJSON(src)}})
					}
				}
			}
			writeJSON(w, http.StatusOK, ghPage(r, events))
		default:
			http.NotFound(w, r)
		}
	default:
		return false
	}
	return true
}

// azdoTime formats a time the way Azure DevOps does.
func azdoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Sources) identity(name string) map[string]any {
	return map[string]any{"displayName": s.users[name], "uniqueName": name + "@acme.test"}
}

// azdoPulls serves the pull request routes of an Azure DevOps repository.
// It reports false for another route.
func (s *Sources) azdoPulls(w http.ResponseWriter, r *http.Request, rp *Repo, rest []string) bool {
	if len(rest) == 0 || !strings.EqualFold(rest[0], "pullrequests") {
		return false
	}
	h := s.history["azdo:"+rp.ID]
	q := r.URL.Query()
	pullJSON := func(p PullRequest) map[string]any {
		at := s.historyAt(p.Age)
		out := map[string]any{"pullRequestId": p.Number, "status": p.State, "title": p.Title, "description": p.Body,
			"createdBy": s.identity(p.Author), "creationDate": azdoTime(at.Add(-48 * time.Hour)), "targetRefName": "refs/heads/" + rp.Branch}
		if p.State == "completed" {
			out["closedDate"] = azdoTime(at)
			out["lastMergeCommit"] = map[string]any{"commitId": BlobID([]byte(fmt.Sprintf("%s!%d", rp.ID, p.Number)))}
		}
		return out
	}
	if len(rest) == 1 {
		minTime, _ := time.Parse(time.RFC3339, q.Get("searchCriteria.minTime"))
		var ps []PullRequest
		if h != nil {
			for _, p := range h.Pulls {
				if q.Get("searchCriteria.status") != "" && p.State != q.Get("searchCriteria.status") {
					continue
				}
				if q.Get("searchCriteria.queryTimeRangeType") == "closed" && s.historyAt(p.Age).Before(minTime) {
					continue
				}
				ps = append(ps, p)
			}
		}
		slices.SortFunc(ps, func(a, b PullRequest) int { return b.Number - a.Number })
		top, _ := strconv.Atoi(q.Get("$top"))
		if top <= 0 {
			top = 101
		}
		skip, _ := strconv.Atoi(q.Get("$skip"))
		out := []map[string]any{}
		for _, p := range window(ps, skip, top) {
			out = append(out, pullJSON(p))
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "value": out})
		return true
	}
	n, _ := strconv.Atoi(rest[1])
	p, ok := s.pullOf(h, n)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "The pull request does not exist."})
		return true
	}
	switch {
	case len(rest) == 2:
		writeJSON(w, http.StatusOK, pullJSON(p))
	case rest[2] == "threads":
		threads := []map[string]any{{"id": 1, "isDeleted": false, "comments": []map[string]any{{"author": s.identity(p.Author),
			"content": "Policy check passed.", "publishedDate": azdoTime(s.historyAt(p.Age)), "commentType": "system"}}}}
		for i, c := range append(append([]Note{}, p.ReviewComments...), p.Comments...) {
			t := map[string]any{"id": i + 2, "isDeleted": false, "comments": []map[string]any{{"author": s.identity(c.Author), "content": c.Body,
				"publishedDate": azdoTime(s.historyAt(c.Age)), "commentType": "text"}}}
			if c.Path != "" {
				t["threadContext"] = map[string]any{"filePath": "/" + c.Path}
			}
			threads = append(threads, t)
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(threads), "value": threads})
	case rest[2] == "workitems":
		out := []map[string]any{}
		for _, id := range p.WorkItems {
			out = append(out, map[string]any{"id": strconv.Itoa(id), "url": origin(r) + "/azdo/" + AzdoCollection + "/_apis/wit/workItems/" + strconv.Itoa(id)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "value": out})
	case rest[2] == "iterations" && len(rest) == 3:
		writeJSON(w, http.StatusOK, map[string]any{"count": 2, "value": []map[string]any{{"id": 1}, {"id": 2}}})
	case rest[2] == "iterations" && len(rest) == 5 && rest[4] == "changes":
		if rest[3] != "2" || q.Get("$compareTo") != "0" {
			writeJSON(w, http.StatusOK, map[string]any{"changeEntries": []any{}})
			return true
		}
		out := []map[string]any{}
		for _, f := range p.Files {
			out = append(out, map[string]any{"changeType": "edit", "item": map[string]any{"path": "/" + f, "gitObjectType": "blob"}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"changeEntries": out})
	default:
		http.NotFound(w, r)
	}
	return true
}

// wikiRepoID is the repository ID of the project wiki of a project.
func wikiRepoID(project string) string { return BlobID([]byte(project + ".wiki"))[:32] }

func wikiItemPath(p string) string { return strings.ReplaceAll(p, " ", "-") + ".md" }

// azdoProject serves the work item and wiki routes of an Azure DevOps
// project, and the Git items of its wiki repository. It reports false for
// another route.
func (s *Sources) azdoProject(w http.ResponseWriter, r *http.Request, project, rest string) bool {
	ph := s.azdoProjects[project]
	q := r.URL.Query()
	wiQuery := func(wi WorkItem) map[string]any {
		at := s.historyAt(wi.Age)
		return map[string]any{"System.Id": wi.ID, "System.Title": wi.Title, "System.WorkItemType": wi.Type, "System.State": wi.State,
			"System.TeamProject": project, "System.AreaPath": project + `\Ledger`, "System.IterationPath": project + `\Sprint 12`,
			"System.AssignedTo": s.identity(wi.AssignedTo), "System.CreatedBy": s.identity(wi.AssignedTo),
			"System.CreatedDate": azdoTime(at.Add(-72 * time.Hour)), "System.ChangedDate": azdoTime(at), "System.Description": wi.Description}
	}
	pick := func(fields map[string]any, names string) map[string]any {
		if names == "" {
			return fields
		}
		out := map[string]any{}
		for _, n := range strings.Split(names, ",") {
			if v, ok := fields[strings.TrimSpace(n)]; ok {
				out[strings.TrimSpace(n)] = v
			}
		}
		return out
	}
	switch {
	case rest == "wit/reporting/workitemrevisions":
		if ph == nil {
			writeJSON(w, http.StatusOK, map[string]any{"values": []any{}, "isLastBatch": true})
			return true
		}
		if q.Get("includeLatestOnly") != "true" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "The fixture serves only the latest revisions."})
			return true
		}
		start, _ := time.Parse(time.RFC3339, q.Get("startDateTime"))
		var ws []WorkItem
		for _, wi := range ph.WorkItems {
			if !s.historyAt(wi.Age).Before(start) {
				ws = append(ws, wi)
			}
		}
		slices.SortStableFunc(ws, func(a, b WorkItem) int { return b.Age - a.Age })
		size, _ := strconv.Atoi(q.Get("$maxPageSize"))
		if size <= 0 || size > 200 {
			size = 200
		}
		from, _ := strconv.Atoi(q.Get("continuationToken"))
		page := window(ws, from, size)
		vals := []map[string]any{}
		for _, wi := range page {
			vals = append(vals, map[string]any{"id": wi.ID, "rev": 3, "fields": pick(wiQuery(wi), q.Get("fields"))})
		}
		writeJSON(w, http.StatusOK, map[string]any{"values": vals, "continuationToken": strconv.Itoa(from + len(page)),
			"isLastBatch": from+len(page) >= len(ws)})
	case rest == "wit/workitems" || strings.HasPrefix(rest, "wit/workitems/") || strings.HasPrefix(rest, "wit/workItems/"):
		ids := strings.Split(q.Get("ids"), ",")
		if r := strings.TrimPrefix(strings.TrimPrefix(rest, "wit/workitems"), "wit/workItems"); r != "" {
			ids = []string{strings.TrimPrefix(r, "/")}
		}
		if len(ids) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "The request takes at most 200 IDs."})
			return true
		}
		vals := []any{}
		for _, id := range ids {
			n, _ := strconv.Atoi(id)
			var found map[string]any
			if ph != nil {
				for _, wi := range ph.WorkItems {
					if wi.ID == n {
						found = map[string]any{"id": wi.ID, "rev": 3, "fields": pick(wiQuery(wi), q.Get("fields")),
							"_links": map[string]any{"html": map[string]any{"href": fmt.Sprintf("%s/azdo/%s/%s/_workitems/edit/%d", origin(r), AzdoCollection, project, wi.ID)}}}
					}
				}
			}
			if found != nil {
				vals = append(vals, found)
			} else if q.Get("errorPolicy") == "omit" {
				vals = append(vals, nil)
			} else {
				writeJSON(w, http.StatusNotFound, map[string]any{"message": "The work item does not exist."})
				return true
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(vals), "value": vals})
	case rest == "wiki/wikis":
		out := []map[string]any{}
		if ph != nil && len(ph.Wiki) > 0 {
			out = append(out, map[string]any{"id": wikiRepoID(project), "name": project + ".wiki", "type": "projectWiki",
				"repositoryId": wikiRepoID(project), "mappedPath": "/", "versions": []map[string]any{{"version": "wikiMaster"}}})
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "value": out})
	case strings.HasPrefix(rest, "wiki/wikis/"+wikiRepoID(project)+"/pages") && ph != nil:
		id := strings.TrimPrefix(strings.TrimPrefix(rest, "wiki/wikis/"+wikiRepoID(project)+"/pages"), "/")
		if id == "" {
			if q.Get("recursionLevel") != "full" {
				writeJSON(w, http.StatusOK, map[string]any{"id": 0, "path": "/", "subPages": []any{}})
				return true
			}
			writeJSON(w, http.StatusOK, s.wikiTree(ph, "/"))
			return true
		}
		n, _ := strconv.Atoi(id)
		for _, pg := range ph.Wiki {
			if pg.ID == n {
				out := map[string]any{"id": pg.ID, "path": pg.Path, "gitItemPath": wikiItemPath(pg.Path),
					"remoteUrl": fmt.Sprintf("%s/azdo/%s/%s/_wiki/wikis/%s.wiki/%d", origin(r), AzdoCollection, project, project, pg.ID)}
				if q.Get("includeContent") == "true" {
					out["content"] = pg.Content
				}
				writeJSON(w, http.StatusOK, out)
				return true
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "The page does not exist."})
	case strings.HasPrefix(rest, "git/repositories/"+wikiRepoID(project)+"/items") && ph != nil:
		out := []map[string]any{}
		for _, pg := range ph.Wiki {
			out = append(out, map[string]any{"objectId": BlobID([]byte(pg.Content)), "gitObjectType": "blob", "path": wikiItemPath(pg.Path)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "value": out})
	default:
		return false
	}
	return true
}

// wikiTree is the page tree below a path, with the pages as sub-pages of
// their parent paths.
func (s *Sources) wikiTree(ph *ProjectHistory, at string) map[string]any {
	node := map[string]any{"id": 0, "path": at, "gitItemPath": "", "subPages": []any{}}
	for _, pg := range ph.Wiki {
		if pg.Path == at {
			node["id"], node["gitItemPath"] = pg.ID, wikiItemPath(pg.Path)
		}
	}
	var subs []any
	for _, pg := range ph.Wiki {
		if path.Dir(pg.Path) == at && pg.Path != at {
			subs = append(subs, s.wikiTree(ph, pg.Path))
		}
	}
	if subs != nil {
		node["subPages"] = subs
	}
	return node
}

// PutWikiPage adds or changes a wiki page of a project.
func (s *Sources) PutWikiPage(project string, p WikiPage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ph := s.azdoProjects[project]
	ph.Wiki = slices.DeleteFunc(ph.Wiki, func(x WikiPage) bool { return x.ID == p.ID })
	ph.Wiki = append(ph.Wiki, p)
}
