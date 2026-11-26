package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/tree"
	"github.com/alternayte/assay/internal/content"
)

// Connector reads the files of the selected repositories and, inside the
// history window, their merged pull requests and their issues.
type Connector struct {
	*tree.Connector
	h     *Host
	repos []string
	hist  connectors.History
}

// perPage is the page size of GitHub listings, the largest that GitHub
// takes.
const perPage = 100

type userJSON struct {
	Login string `json:"login"`
}

type pullJSON struct {
	Number         int        `json:"number"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	HTMLURL        string     `json:"html_url"`
	User           userJSON   `json:"user"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	MergedAt       *time.Time `json:"merged_at"`
	MergeCommitSHA string     `json:"merge_commit_sha"`
	Base           struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type issueJSON struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	HTMLURL   string    `json:"html_url"`
	User      userJSON  `json:"user"`
	Comments  int       `json:"comments"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Labels    []struct {
		Name string `json:"name"`
	} `json:"labels"`
	// PullRequest is set when the issue is a pull request: the issues
	// endpoint also returns pull requests.
	PullRequest *struct{} `json:"pull_request"`
	Repository  *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type commentJSON struct {
	User      userJSON  `json:"user"`
	Body      string    `json:"body"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	// SubmittedAt is the time of a review.
	SubmittedAt time.Time `json:"submitted_at"`
}

func (c commentJSON) at() time.Time {
	if c.CreatedAt.IsZero() {
		return c.SubmittedAt
	}
	return c.CreatedAt
}

// pages reads a listing page by page until a page has fewer than perPage
// entries or fn returns false.
func pages[T any](ctx context.Context, c *connectors.Client, path string, q url.Values, fn func(T) bool) error {
	for page := 1; ; page++ {
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		pq.Set("per_page", strconv.Itoa(perPage))
		pq.Set("page", strconv.Itoa(page))
		var batch []T
		if err := c.GetJSON(ctx, path, pq, &batch); err != nil {
			return err
		}
		for _, x := range batch {
			if !fn(x) {
				return nil
			}
		}
		if len(batch) < perPage {
			return nil
		}
	}
}

// Changed lists the files of a repository, then its merged pull requests
// and its issues updated since, oldest first.
func (k *Connector) Changed(ctx context.Context, part connectors.Part, since time.Time, fn func(connectors.Ref) error) error {
	if err := k.Connector.Changed(ctx, part, since, fn); err != nil {
		return err
	}
	refs, err := k.history(ctx, part.Key, k.hist.From(since))
	if err != nil {
		return err
	}
	for _, r := range refs {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// ListIDs lists the files of a repository and its pull requests and issues
// inside the window.
func (k *Connector) ListIDs(ctx context.Context, part connectors.Part, fn func(string) error) error {
	if err := k.Connector.ListIDs(ctx, part, fn); err != nil {
		return err
	}
	refs, err := k.history(ctx, part.Key, k.hist.Start())
	if err != nil {
		return err
	}
	for _, r := range refs {
		if err := fn(r.ID); err != nil {
			return err
		}
	}
	return nil
}

// Keep keeps the pull requests and issues that left the window, and those
// of a kind that the connection does not read.
func (k *Connector) Keep(id string, updated time.Time) bool { return k.hist.Keep(id, updated) }

// history lists the merged pull requests and the issues of a repository
// updated at or after from, oldest first. A repository that GitHub no
// longer has gives none.
func (k *Connector) history(ctx context.Context, repo string, from time.Time) ([]connectors.Ref, error) {
	if !slices.Contains(k.repos, repo) {
		return nil, nil
	}
	var refs []connectors.Ref
	if k.hist.ReadsPullRequests() {
		// GitHub has no merged filter: list the closed pull requests, newest
		// update first, and keep the merged ones.
		q := url.Values{"state": {"closed"}, "sort": {"updated"}, "direction": {"desc"}}
		err := pages(ctx, k.h.c, "/repos/"+repo+"/pulls", q, func(p pullJSON) bool {
			if p.UpdatedAt.Before(from) {
				return false
			}
			if p.MergedAt != nil {
				refs = append(refs, connectors.Ref{ID: connectors.HistoryID(repo, connectors.KindPullRequest, strconv.Itoa(p.Number)), Updated: p.UpdatedAt})
			}
			return true
		})
		if errors.Is(err, connectors.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if k.hist.ReadsIssues() {
		q := url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"asc"}, "since": {from.UTC().Format(time.RFC3339)}}
		err := pages(ctx, k.h.c, "/repos/"+repo+"/issues", q, func(is issueJSON) bool {
			if is.PullRequest == nil {
				refs = append(refs, connectors.Ref{ID: connectors.HistoryID(repo, connectors.KindIssue, strconv.Itoa(is.Number)), Updated: is.UpdatedAt})
			}
			return true
		})
		if errors.Is(err, connectors.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	connectors.SortRefs(refs)
	return refs, nil
}

// Fetch reads a file, a pull request or an issue.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	repo, kind, num, ok := connectors.ParseHistoryID(id)
	if !ok {
		return k.Connector.Fetch(ctx, id)
	}
	n, err := strconv.Atoi(num)
	if err != nil || !slices.Contains(k.repos, repo) {
		return nil, connectors.ErrNotFound
	}
	switch kind {
	case connectors.KindPullRequest:
		return k.pull(ctx, repo, n)
	case connectors.KindIssue:
		return k.issue(ctx, repo, n)
	}
	return nil, connectors.ErrNotFound
}

func (k *Connector) changeKey(repo string, n int) string {
	return "github:" + k.h.web + ":" + strings.ToLower(repo) + "#pr/" + strconv.Itoa(n)
}

func (k *Connector) issueRef(repo string, n int, title string) *content.EntityRef {
	return &content.EntityRef{Type: "issue", Key: "github:" + k.h.web + ":" + strings.ToLower(repo) + "#issue/" + strconv.Itoa(n), Name: title,
		Aliases: []string{repo + "#" + strconv.Itoa(n)}}
}

// closing are the GitHub closing keywords with an issue reference.
var closing = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)?#([0-9]+)\b`)

// pull reads a merged pull request with its changed files, its reviews,
// review comments and thread comments, and the issues that it implements.
func (k *Connector) pull(ctx context.Context, repo string, n int) (*content.Item, error) {
	base := "/repos/" + repo + "/pulls/" + strconv.Itoa(n)
	var p pullJSON
	if err := k.h.c.GetJSON(ctx, base, nil, &p); err != nil {
		return nil, err
	}
	if p.MergedAt == nil {
		return nil, connectors.ErrNotFound
	}
	var files []string
	if err := pages(ctx, k.h.c, base+"/files", nil, func(f struct {
		Filename string `json:"filename"`
	}) bool {
		files = append(files, f.Filename)
		return true
	}); err != nil {
		return nil, err
	}
	var review []commentJSON
	collect := func(c commentJSON) bool {
		if strings.TrimSpace(c.Body) != "" {
			review = append(review, c)
		}
		return true
	}
	for _, path := range []string{base + "/reviews", base + "/comments", "/repos/" + repo + "/issues/" + strconv.Itoa(n) + "/comments"} {
		if err := pages(ctx, k.h.c, path, nil, collect); err != nil {
			return nil, err
		}
	}
	slices.SortStableFunc(review, func(a, b commentJSON) int { return a.at().Compare(b.at()) })
	var linked []issueJSON
	if err := pages(ctx, k.h.c, "/repos/"+repo+"/issues/"+strconv.Itoa(n)+"/timeline", nil, func(e struct {
		Event  string `json:"event"`
		Source struct {
			Issue *issueJSON `json:"issue"`
		} `json:"source"`
	}) bool {
		if e.Event == "cross-referenced" && e.Source.Issue != nil && e.Source.Issue.PullRequest == nil {
			linked = append(linked, *e.Source.Issue)
		}
		return true
	}); err != nil {
		return nil, err
	}

	subject := content.EntityRef{Type: "change", Key: k.changeKey(repo, n), Name: p.Title, Aliases: []string{repo + "#" + strconv.Itoa(n)}}
	b := &connectors.Body{}
	b.Field("Title", p.Title)
	b.Field("Author", p.User.Login)
	b.Field("Merged", p.MergedAt.UTC().Format("2006-01-02"))
	b.Field("Merge commit", p.MergeCommitSHA)
	var facts []content.Fact
	seen := map[string]bool{}
	implements := func(obj *content.EntityRef, quote string) {
		if seen[obj.Key] {
			return
		}
		seen[obj.Key] = true
		facts = append(facts, content.Fact{Subject: subject, Predicate: "implements", Object: obj, Quote: quote})
	}
	for _, is := range linked {
		r := repo
		if is.Repository != nil && is.Repository.FullName != "" {
			r = is.Repository.FullName
		}
		ref := "#" + strconv.Itoa(is.Number)
		if !strings.EqualFold(r, repo) {
			ref = r + ref
		}
		b.Field("Linked issue", ref+" "+is.Title)
		implements(k.issueRef(r, is.Number, is.Title), "Linked issue: "+ref)
	}
	if strings.TrimSpace(p.Body) != "" {
		b.Heading(2, "Description")
		b.Markdown(p.Body, 2)
	}
	if len(review) > 0 {
		b.Heading(2, "Review")
		for _, c := range review {
			head := c.User.Login
			if c.Path != "" {
				head += " on " + c.Path
			}
			b.Heading(3, head+", "+c.at().UTC().Format("2006-01-02 15:04"))
			b.Markdown(c.Body, 3)
		}
	}
	for _, m := range closing.FindAllStringSubmatch(p.Body, -1) {
		r := repo
		if m[1] != "" {
			r = m[1]
		}
		num, _ := strconv.Atoi(m[2])
		is, err := k.issueOf(ctx, r, num)
		if err != nil {
			return nil, err
		}
		if is != nil {
			implements(k.issueRef(r, num, is.Title), m[0])
		}
	}
	for _, obj := range k.hist.JiraKeys(p.Title + "\n" + p.Body) {
		implements(obj, obj.Aliases[0])
	}
	return &content.Item{
		ExternalID: connectors.HistoryID(repo, connectors.KindPullRequest, strconv.Itoa(n)), Kind: "pull_request",
		Title: fmt.Sprintf("#%d %s", n, p.Title), URL: p.HTMLURL, Author: p.User.Login, Created: p.CreatedAt, Updated: p.UpdatedAt,
		Body: b.String(), Entity: &subject, Facts: facts,
		Metadata: map[string]any{"repository": repo, "number": n, "mergedAt": p.MergedAt.UTC().Format(time.RFC3339), "mergeCommit": p.MergeCommitSHA,
			"baseBranch": p.Base.Ref, "changedPaths": orEmpty(files)},
	}, nil
}

// issueOf returns an issue, or nil when it is a pull request or does not
// exist.
func (k *Connector) issueOf(ctx context.Context, repo string, n int) (*issueJSON, error) {
	var is issueJSON
	err := k.h.c.GetJSON(ctx, "/repos/"+repo+"/issues/"+strconv.Itoa(n), nil, &is)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if is.PullRequest != nil {
		return nil, nil
	}
	return &is, nil
}

// issue reads an issue with its comments.
func (k *Connector) issue(ctx context.Context, repo string, n int) (*content.Item, error) {
	is, err := k.issueOf(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	if is == nil {
		return nil, connectors.ErrNotFound
	}
	var comments []commentJSON
	if is.Comments > 0 {
		if err := pages(ctx, k.h.c, "/repos/"+repo+"/issues/"+strconv.Itoa(n)+"/comments", nil, func(c commentJSON) bool {
			comments = append(comments, c)
			return true
		}); err != nil {
			return nil, err
		}
	}
	var labels []string
	for _, l := range is.Labels {
		labels = append(labels, l.Name)
	}
	subject := k.issueRef(repo, n, is.Title)
	b := &connectors.Body{}
	b.Field("State", is.State)
	b.Field("Author", is.User.Login)
	b.Field("Labels", strings.Join(labels, ", "))
	if strings.TrimSpace(is.Body) != "" {
		b.Heading(2, "Description")
		b.Markdown(is.Body, 2)
	}
	if len(comments) > 0 {
		b.Heading(2, "Comments")
		for _, c := range comments {
			b.Heading(3, c.User.Login+", "+c.CreatedAt.UTC().Format("2006-01-02 15:04"))
			b.Markdown(c.Body, 3)
		}
	}
	it := &content.Item{
		ExternalID: connectors.HistoryID(repo, connectors.KindIssue, strconv.Itoa(n)), Kind: "issue",
		Title: fmt.Sprintf("#%d %s", n, is.Title), URL: is.HTMLURL, Author: is.User.Login, Created: is.CreatedAt, Updated: is.UpdatedAt,
		Body: b.String(), Entity: subject,
		Metadata: map[string]any{"repository": repo, "number": n, "state": is.State, "labels": orEmpty(labels)},
	}
	if is.State != "" {
		it.Facts = []content.Fact{{Subject: *subject, Predicate: "status", Text: is.State, Quote: "State: " + is.State}}
	}
	return it, nil
}

// LastCommit returns the message of the last commit of the default branch
// that changed a file.
func (h *Host) LastCommit(ctx context.Context, r tree.Repo, file string) (string, error) {
	var cs []struct {
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	q := url.Values{"path": {file}, "sha": {r.Branch}, "per_page": {"1"}}
	if err := h.c.GetJSON(ctx, "/repos/"+r.ID+"/commits", q, &cs); err != nil {
		return "", err
	}
	if len(cs) == 0 {
		return "", nil
	}
	return cs[0].Commit.Message, nil
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
