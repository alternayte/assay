package azdo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/connectors/tree"
	"github.com/alternayte/assay/internal/content"
)

// Connector reads the files of the selected repositories and their
// completed pull requests inside the history window, and the work items
// and the wiki pages of their projects. A project is a part of its own.
type Connector struct {
	*tree.Connector
	h        *Host
	repos    []string
	projects []string
	hist     connectors.History

	mu    gosync.Mutex
	wikis map[string]*wikiListing
}

// Page sizes. A work item request takes at most 200 IDs.
const (
	pullPage     = 100
	workItemPage = 200
	changesPage  = 2000
)

// Parts are the selected repositories and then their projects.
func (k *Connector) Parts(ctx context.Context) ([]connectors.Part, error) {
	out, err := k.Connector.Parts(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range k.projects {
		out = append(out, connectors.Part{Key: p})
	}
	return out, nil
}

func (k *Connector) isProject(part string) bool { return slices.Contains(k.projects, part) }

// Changed lists the files and the completed pull requests of a repository,
// or the wiki pages and the work items of a project.
func (k *Connector) Changed(ctx context.Context, part connectors.Part, since time.Time, fn func(connectors.Ref) error) error {
	if !k.isProject(part.Key) {
		if err := k.Connector.Changed(ctx, part, since, fn); err != nil {
			return err
		}
		return k.pulls(ctx, part.Key, k.hist.From(since), fn)
	}
	if err := k.wikiRefs(ctx, part.Key, fn); err != nil {
		return err
	}
	return k.workItems(ctx, part.Key, k.hist.From(since), true, fn)
}

// ListIDs lists the items of a part inside the window.
func (k *Connector) ListIDs(ctx context.Context, part connectors.Part, fn func(string) error) error {
	ids := func(r connectors.Ref) error { return fn(r.ID) }
	if !k.isProject(part.Key) {
		if err := k.Connector.ListIDs(ctx, part, fn); err != nil {
			return err
		}
		return k.pulls(ctx, part.Key, k.hist.Start(), ids)
	}
	if err := k.wikiRefs(ctx, part.Key, ids); err != nil {
		return err
	}
	return k.workItems(ctx, part.Key, k.hist.Start(), false, ids)
}

// Keep keeps the pull requests and work items that left the window, and the
// items of a kind that the connection does not read.
func (k *Connector) Keep(id string, updated time.Time) bool { return k.hist.Keep(id, updated) }

// Fetch reads a file, a pull request, a work item or a wiki page.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	scope, kind, num, ok := connectors.ParseHistoryID(id)
	if !ok {
		return k.Connector.Fetch(ctx, id)
	}
	switch {
	case kind == connectors.KindPullRequest && slices.Contains(k.repos, scope):
		n, err := strconv.Atoi(num)
		if err != nil {
			return nil, connectors.ErrNotFound
		}
		return k.pull(ctx, scope, n)
	case kind == connectors.KindWorkItem && k.isProject(scope):
		items, err := k.readWorkItems(ctx, scope, []string{num})
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, connectors.ErrNotFound
		}
		return items[0], nil
	case kind == connectors.KindWiki && k.isProject(scope):
		return k.wikiPage(ctx, scope, num)
	}
	return nil, connectors.ErrNotFound
}

func (k *Connector) web(p string, q url.Values) string { return k.h.c.URL(p, q) }

type identityJSON struct {
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
}

func (i identityJSON) name() string {
	if i.DisplayName != "" {
		return i.DisplayName
	}
	return i.UniqueName
}

type pullJSON struct {
	PullRequestID int          `json:"pullRequestId"`
	Status        string       `json:"status"`
	Title         string       `json:"title"`
	Description   string       `json:"description"`
	CreatedBy     identityJSON `json:"createdBy"`
	CreationDate  time.Time    `json:"creationDate"`
	ClosedDate    time.Time    `json:"closedDate"`
	TargetRefName string       `json:"targetRefName"`
	MergeCommit   struct {
		CommitID string `json:"commitId"`
	} `json:"lastMergeCommit"`
}

// pulls lists the completed pull requests of a repository closed at or
// after from, oldest first. A repository that the server no longer has
// gives none.
func (k *Connector) pulls(ctx context.Context, repo string, from time.Time, fn func(connectors.Ref) error) error {
	if !k.hist.ReadsPullRequests() || !slices.Contains(k.repos, repo) {
		return nil
	}
	p, err := repoPath(repo)
	if err != nil {
		return err
	}
	var refs []connectors.Ref
	for skip := 0; ; skip += pullPage {
		var page struct {
			Value []pullJSON `json:"value"`
		}
		q := k.h.query(url.Values{"searchCriteria.status": {"completed"}, "searchCriteria.queryTimeRangeType": {"closed"},
			"searchCriteria.minTime": {from.UTC().Format(time.RFC3339)}, "$top": {strconv.Itoa(pullPage)}, "$skip": {strconv.Itoa(skip)}})
		err := k.h.c.GetJSON(ctx, p+"/pullrequests", q, &page)
		if errors.Is(err, connectors.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, pr := range page.Value {
			if pr.Status == "completed" && !pr.ClosedDate.Before(from) {
				refs = append(refs, connectors.Ref{ID: connectors.HistoryID(repo, connectors.KindPullRequest, strconv.Itoa(pr.PullRequestID)),
					Updated: pr.ClosedDate})
			}
		}
		if len(page.Value) < pullPage {
			break
		}
	}
	connectors.SortRefs(refs)
	for _, r := range refs {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// pull reads a completed pull request with its threads, its changed files
// and its work items. The threads endpoint is not paged: one request reads
// all.
func (k *Connector) pull(ctx context.Context, repo string, n int) (*content.Item, error) {
	p, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	base := p + "/pullRequests/" + strconv.Itoa(n)
	var pr pullJSON
	if err := k.h.c.GetJSON(ctx, base, k.h.query(nil), &pr); err != nil {
		return nil, err
	}
	if pr.Status != "completed" {
		return nil, connectors.ErrNotFound
	}
	var threads struct {
		Value []struct {
			IsDeleted bool `json:"isDeleted"`
			Context   *struct {
				FilePath string `json:"filePath"`
			} `json:"threadContext"`
			Comments []struct {
				Author        identityJSON `json:"author"`
				Content       string       `json:"content"`
				PublishedDate time.Time    `json:"publishedDate"`
				CommentType   string       `json:"commentType"`
				IsDeleted     bool         `json:"isDeleted"`
			} `json:"comments"`
		} `json:"value"`
	}
	if err := k.h.c.GetJSON(ctx, base+"/threads", k.h.query(nil), &threads); err != nil {
		return nil, err
	}
	type comment struct {
		head string
		at   time.Time
		body string
	}
	var review []comment
	for _, t := range threads.Value {
		if t.IsDeleted {
			continue
		}
		file := ""
		if t.Context != nil {
			file = strings.TrimPrefix(t.Context.FilePath, "/")
		}
		for _, c := range t.Comments {
			if c.IsDeleted || c.CommentType == "system" || strings.TrimSpace(c.Content) == "" {
				continue
			}
			head := c.Author.name()
			if file != "" {
				head += " on " + file
			}
			review = append(review, comment{head: head, at: c.PublishedDate, body: c.Content})
		}
	}
	slices.SortStableFunc(review, func(a, b comment) int { return a.at.Compare(b.at) })
	files, err := k.changedFiles(ctx, base)
	if err != nil {
		return nil, err
	}
	var links struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if err := k.h.c.GetJSON(ctx, base+"/workitems", k.h.query(nil), &links); err != nil {
		return nil, err
	}
	project, name, _ := strings.Cut(repo, "/")
	var ids []string
	for _, l := range links.Value {
		ids = append(ids, l.ID)
	}
	linked, err := k.workItemsByID(ctx, project, ids, url.Values{"fields": {"System.Title,System.WorkItemType,System.TeamProject"}})
	if err != nil {
		return nil, err
	}

	subject := content.EntityRef{Type: "change", Key: "azdo:" + k.h.c.Host() + ":" + strings.ToLower(repo) + "#pr/" + strconv.Itoa(n), Name: pr.Title,
		Aliases: []string{repo + "!" + strconv.Itoa(n)}}
	b := &connectors.Body{}
	b.Field("Title", pr.Title)
	b.Field("Author", pr.CreatedBy.name())
	b.Field("Completed", pr.ClosedDate.UTC().Format("2006-01-02"))
	b.Field("Merge commit", pr.MergeCommit.CommitID)
	var facts []content.Fact
	seen := map[string]bool{}
	implements := func(obj *content.EntityRef, quote string) {
		if !seen[obj.Key] {
			seen[obj.Key] = true
			facts = append(facts, content.Fact{Subject: subject, Predicate: "implements", Object: obj, Quote: quote})
		}
	}
	for _, w := range linked {
		b.Field("Work item", strconv.Itoa(w.ID)+" "+w.str("System.Title"))
		implements(k.workItemRef(w, project), "Work item: "+strconv.Itoa(w.ID))
	}
	if strings.TrimSpace(pr.Description) != "" {
		b.Heading(2, "Description")
		b.Markdown(pr.Description, 2)
	}
	if len(review) > 0 {
		b.Heading(2, "Review")
		for _, c := range review {
			b.Heading(3, c.head+", "+c.at.UTC().Format("2006-01-02 15:04"))
			b.Markdown(c.body, 3)
		}
	}
	for _, obj := range k.hist.JiraKeys(pr.Title + "\n" + pr.Description) {
		implements(obj, obj.Aliases[0])
	}
	return &content.Item{
		ExternalID: connectors.HistoryID(repo, connectors.KindPullRequest, strconv.Itoa(n)), Kind: "pull_request",
		Title: fmt.Sprintf("!%d %s", n, pr.Title), URL: k.web("/"+project+"/_git/"+name+"/pullrequest/"+strconv.Itoa(n), nil),
		Author: pr.CreatedBy.name(), Created: pr.CreationDate, Updated: pr.ClosedDate, Body: b.String(), Entity: &subject, Facts: facts,
		Metadata: map[string]any{"repository": repo, "number": n, "completedAt": pr.ClosedDate.UTC().Format(time.RFC3339),
			"mergeCommit": pr.MergeCommit.CommitID, "targetBranch": strings.TrimPrefix(pr.TargetRefName, "refs/heads/"), "changedPaths": orEmpty(files)},
	}, nil
}

// changedFiles lists the paths that the last iteration of a pull request
// changed against its target.
func (k *Connector) changedFiles(ctx context.Context, base string) ([]string, error) {
	var its struct {
		Value []struct {
			ID int `json:"id"`
		} `json:"value"`
	}
	if err := k.h.c.GetJSON(ctx, base+"/iterations", k.h.query(nil), &its); err != nil {
		return nil, err
	}
	last := 0
	for _, it := range its.Value {
		last = max(last, it.ID)
	}
	if last == 0 {
		return nil, nil
	}
	var out []string
	for skip := 0; ; {
		var ch struct {
			ChangeEntries []struct {
				Item struct {
					Path          string `json:"path"`
					GitObjectType string `json:"gitObjectType"`
				} `json:"item"`
			} `json:"changeEntries"`
			NextSkip int `json:"nextSkip"`
		}
		q := k.h.query(url.Values{"$compareTo": {"0"}, "$top": {strconv.Itoa(changesPage)}, "$skip": {strconv.Itoa(skip)}})
		if err := k.h.c.GetJSON(ctx, base+"/iterations/"+strconv.Itoa(last)+"/changes", q, &ch); err != nil {
			return nil, err
		}
		for _, e := range ch.ChangeEntries {
			if e.Item.GitObjectType != "tree" && e.Item.Path != "" {
				out = append(out, strings.TrimPrefix(e.Item.Path, "/"))
			}
		}
		if ch.NextSkip <= skip {
			return out, nil
		}
		skip = ch.NextSkip
	}
}

// workItemJSON is a work item with its fields.
type workItemJSON struct {
	ID     int            `json:"id"`
	Fields map[string]any `json:"fields"`
	Links  struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"_links"`
}

func (w workItemJSON) str(field string) string {
	switch v := w.Fields[field].(type) {
	case string:
		return v
	case map[string]any:
		if s, _ := v["displayName"].(string); s != "" {
			return s
		}
		s, _ := v["uniqueName"].(string)
		return s
	}
	return ""
}

func (w workItemJSON) time(field string) time.Time {
	t, _ := time.Parse(time.RFC3339, w.str(field))
	return t
}

// workItemRef is the entity of a work item. project is the project of a
// work item without the System.TeamProject field.
func (k *Connector) workItemRef(w workItemJSON, project string) *content.EntityRef {
	typ := "issue"
	if slices.Contains(k.hist.InitiativeTypes, w.str("System.WorkItemType")) {
		typ = "initiative"
	}
	if p := w.str("System.TeamProject"); p != "" {
		project = p
	}
	return &content.EntityRef{Type: typ, Key: "azdo:" + k.h.c.Host() + ":" + strings.ToLower(project) + "#wi/" + strconv.Itoa(w.ID),
		Name: w.str("System.Title")}
}

// workItemsByID reads work items in batches of 200 IDs. A work item that is
// gone or that the token cannot read is left out.
func (k *Connector) workItemsByID(ctx context.Context, project string, ids []string, extra url.Values) ([]workItemJSON, error) {
	var out []workItemJSON
	for chunk := range slices.Chunk(ids, workItemPage) {
		var res struct {
			Value []*workItemJSON `json:"value"`
		}
		q := k.h.query(url.Values{"ids": {strings.Join(chunk, ",")}, "errorPolicy": {"omit"}})
		for key, v := range extra {
			q[key] = v
		}
		if err := k.h.c.GetJSON(ctx, "/"+url.PathEscape(project)+"/_apis/wit/workitems", q, &res); err != nil {
			return nil, err
		}
		for _, w := range res.Value {
			if w != nil {
				out = append(out, *w)
			}
		}
	}
	return out, nil
}

// workItems lists the work items of a project changed at or after from,
// oldest first, through the reporting revisions with only the latest
// revision of each work item. With read set, it reads each page of up to
// 200 work items in one request and the refs carry the items.
func (k *Connector) workItems(ctx context.Context, project string, from time.Time, read bool, fn func(connectors.Ref) error) error {
	if !k.hist.ReadsIssues() {
		return nil
	}
	token := ""
	for {
		var page struct {
			Values []struct {
				ID     int            `json:"id"`
				Fields map[string]any `json:"fields"`
			} `json:"values"`
			ContinuationToken string `json:"continuationToken"`
			IsLastBatch       bool   `json:"isLastBatch"`
		}
		q := k.h.query(url.Values{"includeLatestOnly": {"true"}, "startDateTime": {from.UTC().Format(time.RFC3339)},
			"fields": {"System.Id,System.ChangedDate"}, "$maxPageSize": {strconv.Itoa(workItemPage)}})
		if token != "" {
			q.Set("continuationToken", token)
		}
		err := k.h.c.GetJSON(ctx, "/"+url.PathEscape(project)+"/_apis/wit/reporting/workitemrevisions", q, &page)
		if errors.Is(err, connectors.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		refs := make([]connectors.Ref, 0, len(page.Values))
		ids := make([]string, 0, len(page.Values))
		for _, v := range page.Values {
			w := workItemJSON{ID: v.ID, Fields: v.Fields}
			refs = append(refs, connectors.Ref{ID: connectors.HistoryID(project, connectors.KindWorkItem, strconv.Itoa(v.ID)),
				Updated: w.time("System.ChangedDate")})
			ids = append(ids, strconv.Itoa(v.ID))
		}
		if read && len(ids) > 0 {
			items, err := k.readWorkItems(ctx, project, ids)
			if err != nil {
				return err
			}
			byID := map[string]*content.Item{}
			for _, it := range items {
				byID[it.ExternalID] = it
			}
			// A work item that is gone between the listing and the read
			// has no item. Sync fetches it and finds it gone.
			for i := range refs {
				refs[i].Item = byID[refs[i].ID]
			}
		}
		connectors.SortRefs(refs)
		for _, r := range refs {
			if err := fn(r); err != nil {
				return err
			}
		}
		if page.IsLastBatch || page.ContinuationToken == "" || page.ContinuationToken == token || len(page.Values) == 0 {
			return nil
		}
		token = page.ContinuationToken
	}
}

// readWorkItems reads work items with their fields.
func (k *Connector) readWorkItems(ctx context.Context, project string, ids []string) ([]*content.Item, error) {
	ws, err := k.workItemsByID(ctx, project, ids, nil)
	if err != nil {
		return nil, err
	}
	out := make([]*content.Item, 0, len(ws))
	for _, w := range ws {
		out = append(out, k.workItem(project, w))
	}
	return out, nil
}

// workItem builds the item of a work item.
func (k *Connector) workItem(project string, w workItemJSON) *content.Item {
	subject := k.workItemRef(w, project)
	b := &connectors.Body{}
	b.Field("Type", w.str("System.WorkItemType"))
	b.Field("State", w.str("System.State"))
	b.Field("Assigned to", w.str("System.AssignedTo"))
	b.Field("Area", w.str("System.AreaPath"))
	b.Field("Iteration", w.str("System.IterationPath"))
	b.Field("Tags", strings.ReplaceAll(w.str("System.Tags"), ";", ","))
	for _, f := range []struct{ field, heading string }{{"System.Description", "Description"},
		{"Microsoft.VSTS.Common.AcceptanceCriteria", "Acceptance criteria"}, {"Microsoft.VSTS.TCM.ReproSteps", "Repro steps"}} {
		h := strings.TrimSpace(w.str(f.field))
		if h == "" {
			continue
		}
		md, err := confluence.ToMarkdown(h)
		if err != nil || strings.TrimSpace(md) == "" {
			continue
		}
		b.Heading(2, f.heading)
		b.Markdown(md, 2)
	}
	u := w.Links.HTML.Href
	if u == "" {
		u = k.web("/"+project+"/_workitems/edit/"+strconv.Itoa(w.ID), nil)
	}
	it := &content.Item{
		ExternalID: connectors.HistoryID(project, connectors.KindWorkItem, strconv.Itoa(w.ID)), Kind: "work_item",
		Title: fmt.Sprintf("#%d %s", w.ID, w.str("System.Title")), URL: u, Author: w.str("System.CreatedBy"),
		Created: w.time("System.CreatedDate"), Updated: w.time("System.ChangedDate"), Body: b.String(), Entity: subject,
		Metadata: map[string]any{"project": project, "id": w.ID, "type": w.str("System.WorkItemType"), "state": w.str("System.State"),
			"assignedTo": w.str("System.AssignedTo")},
	}
	if s := w.str("System.State"); s != "" {
		it.Facts = []content.Fact{{Subject: *subject, Predicate: "status", Text: s, Quote: "State: " + s}}
	}
	return it
}

// wikiPageRef is one page of a project wiki.
type wikiPageRef struct {
	wiki, path, version string
}

// wikiListing is the pages of the project wikis of one project by page ID.
type wikiListing struct {
	pages map[string]wikiPageRef
	order []string
}

// wikiRefs lists the pages of the project wikis of a project with the blob
// ID of each page file as the version, so that sync reads only the pages
// that changed.
func (k *Connector) wikiRefs(ctx context.Context, project string, fn func(connectors.Ref) error) error {
	if !k.hist.Wiki {
		return nil
	}
	l, err := k.listWiki(ctx, project)
	if err != nil {
		return err
	}
	for _, id := range l.order {
		if err := fn(connectors.Ref{ID: connectors.HistoryID(project, connectors.KindWiki, id), Version: l.pages[id].version}); err != nil {
			return err
		}
	}
	return nil
}

// listWiki reads the page trees of the project wikis of a project. A code
// wiki is the files of a repository, which the repository parts read.
func (k *Connector) listWiki(ctx context.Context, project string) (*wikiListing, error) {
	pp := "/" + url.PathEscape(project) + "/_apis/wiki/wikis"
	var wikis struct {
		Value []struct {
			ID           string `json:"id"`
			Type         string `json:"type"`
			RepositoryID string `json:"repositoryId"`
			Versions     []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"value"`
	}
	l := &wikiListing{pages: map[string]wikiPageRef{}}
	err := k.h.c.GetJSON(ctx, pp, k.h.query(nil), &wikis)
	if errors.Is(err, connectors.ErrNotFound) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	for _, w := range wikis.Value {
		if w.Type != "projectWiki" {
			continue
		}
		branch := "wikiMaster"
		if len(w.Versions) > 0 && w.Versions[0].Version != "" {
			branch = w.Versions[0].Version
		}
		var files struct {
			Value []struct {
				ObjectID string `json:"objectId"`
				Path     string `json:"path"`
			} `json:"value"`
		}
		q := k.h.query(url.Values{"recursionLevel": {"Full"}, "versionDescriptor.version": {branch}, "versionDescriptor.versionType": {"branch"}})
		if err := k.h.c.GetJSON(ctx, "/"+url.PathEscape(project)+"/_apis/git/repositories/"+url.PathEscape(w.RepositoryID)+"/items", q, &files); err != nil {
			return nil, err
		}
		blobs := map[string]string{}
		for _, f := range files.Value {
			blobs[f.Path] = f.ObjectID
		}
		var root wikiTreeJSON
		q = k.h.query(url.Values{"path": {"/"}, "recursionLevel": {"full"}})
		if err := k.h.c.GetJSON(ctx, pp+"/"+url.PathEscape(w.ID)+"/pages", q, &root); err != nil {
			return nil, err
		}
		var walk func(p wikiTreeJSON)
		walk = func(p wikiTreeJSON) {
			if p.Path != "/" && p.ID != 0 {
				id := strconv.Itoa(p.ID)
				if _, dup := l.pages[id]; !dup {
					l.pages[id] = wikiPageRef{wiki: w.ID, path: p.Path, version: blobs[p.GitItemPath]}
					l.order = append(l.order, id)
				}
			}
			for _, s := range p.SubPages {
				walk(s)
			}
		}
		walk(root)
	}
	k.mu.Lock()
	k.wikis[project] = l
	k.mu.Unlock()
	return l, nil
}

type wikiTreeJSON struct {
	ID          int            `json:"id"`
	Path        string         `json:"path"`
	GitItemPath string         `json:"gitItemPath"`
	SubPages    []wikiTreeJSON `json:"subPages"`
}

// wikiPage reads one wiki page with its Markdown.
func (k *Connector) wikiPage(ctx context.Context, project, id string) (*content.Item, error) {
	k.mu.Lock()
	l := k.wikis[project]
	k.mu.Unlock()
	if l == nil || l.pages[id].wiki == "" {
		var err error
		if l, err = k.listWiki(ctx, project); err != nil {
			return nil, err
		}
	}
	ref, ok := l.pages[id]
	if !ok {
		return nil, connectors.ErrNotFound
	}
	var p struct {
		Path      string `json:"path"`
		Content   string `json:"content"`
		RemoteURL string `json:"remoteUrl"`
	}
	q := k.h.query(url.Values{"includeContent": {"true"}})
	if err := k.h.c.GetJSON(ctx, "/"+url.PathEscape(project)+"/_apis/wiki/wikis/"+url.PathEscape(ref.wiki)+"/pages/"+id, q, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		p.Path = ref.path
	}
	u := p.RemoteURL
	if u == "" {
		u = k.web("/"+project+"/_wiki/wikis/"+ref.wiki+"/"+id, nil)
	}
	return &content.Item{
		ExternalID: connectors.HistoryID(project, connectors.KindWiki, id), Kind: "page", Title: path.Base(p.Path), URL: u,
		Body: strings.TrimSpace(strings.ReplaceAll(p.Content, "\r\n", "\n")), Revision: ref.version,
		Metadata: map[string]any{"project": project, "wiki": ref.wiki, "path": p.Path},
	}, nil
}

// LastCommit returns the message of the last commit of the default branch
// that changed a file.
func (h *Host) LastCommit(ctx context.Context, r tree.Repo, file string) (string, error) {
	p, err := repoPath(r.ID)
	if err != nil {
		return "", err
	}
	var cs struct {
		Value []struct {
			Comment string `json:"comment"`
		} `json:"value"`
	}
	q := h.query(url.Values{"searchCriteria.itemPath": {"/" + file}, "searchCriteria.itemVersion.version": {r.Branch},
		"searchCriteria.itemVersion.versionType": {"branch"}, "$top": {"1"}})
	if err := h.c.GetJSON(ctx, p+"/commits", q, &cs); err != nil {
		return "", err
	}
	if len(cs.Value) == 0 {
		return "", nil
	}
	return cs.Value[0].Comment, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
