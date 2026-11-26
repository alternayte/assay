package connectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alternayte/assay/internal/content"
)

// DefaultHistoryMonths is the history window of a repository connection
// without the option. Older pull requests seldom explain the current code.
const DefaultHistoryMonths = 24

// HistoryOptions are the history options of a GitHub or Azure DevOps
// connection. A missing switch is on.
type HistoryOptions struct {
	HistoryMonths *int  `json:"historyMonths,omitempty" minimum:"0" doc:"The months of pull requests, issues and work items that sync reads. 0 turns history off. The default is 24."`
	PullRequests  *bool `json:"pullRequests,omitempty" doc:"Read merged pull requests. The default is on."`
	Issues        *bool `json:"issues,omitempty" doc:"Read the issues of GitHub repositories and the work items of Azure DevOps projects. The default is on."`
	Wiki          *bool `json:"wiki,omitempty" doc:"Read the wiki pages of Azure DevOps projects. The default is on."`
}

// JiraProject is one project key of a Jira connection. A pull request that
// names an issue key of it implements that issue.
type JiraProject struct {
	Host string
	Key  string
}

// History is what a repository connection reads besides its files: merged
// pull requests, issues or work items, wiki pages and the last commit of
// each changed file.
type History struct {
	// Months is the window. 0 turns off pull requests, issues, work items
	// and the last commits.
	Months       int
	PullRequests bool
	Issues       bool
	Wiki         bool
	// InitiativeTypes are the work item types that become initiatives.
	InitiativeTypes []string
	// Jira are the projects of the Jira connections.
	Jira []JiraProject
	Now  func() time.Time
}

// ParseHistory reads the history options of a connection.
func ParseHistory(options json.RawMessage) (History, error) {
	var o HistoryOptions
	if len(options) > 0 {
		if err := json.Unmarshal(options, &o); err != nil {
			return History{}, fmt.Errorf("connectors: the options are not valid: %w", err)
		}
	}
	h := History{Months: DefaultHistoryMonths, PullRequests: true, Issues: true, Wiki: true, Now: time.Now}
	if o.HistoryMonths != nil {
		if *o.HistoryMonths < 0 {
			return History{}, errors.New("connectors: historyMonths must be 0 or more")
		}
		h.Months = *o.HistoryMonths
	}
	for _, s := range []struct {
		v   *bool
		out *bool
	}{{o.PullRequests, &h.PullRequests}, {o.Issues, &h.Issues}, {o.Wiki, &h.Wiki}} {
		if s.v != nil {
			*s.out = *s.v
		}
	}
	return h, nil
}

// Start is the start of the window.
func (h History) Start() time.Time {
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	return now().UTC().AddDate(0, -h.Months, 0)
}

// From is the start of a listing: since, or the start of the window when
// since is before it.
func (h History) From(since time.Time) time.Time {
	if s := h.Start(); since.Before(s) {
		return s
	}
	return since
}

// ReadsPullRequests reports whether sync reads pull requests.
func (h History) ReadsPullRequests() bool { return h.Months > 0 && h.PullRequests }

// ReadsIssues reports whether sync reads issues or work items.
func (h History) ReadsIssues() bool { return h.Months > 0 && h.Issues }

// ReadsLastChange reports whether sync reads the last commit of a changed
// file.
func (h History) ReadsLastChange() bool { return h.Months > 0 }

// History kinds of item IDs.
const (
	KindPullRequest = "pr"
	KindIssue       = "issue"
	KindWorkItem    = "wi"
	KindWiki        = "wiki"
)

// HistoryID is the item ID of a history item: <repo>#pr/<number>,
// <repo>#issue/<number>, <project>#wi/<id> or <project>#wiki/<page id>.
func HistoryID(scope, kind, id string) string { return scope + "#" + kind + "/" + id }

// ParseHistoryID splits a history item ID. ok is false for a file ID.
func ParseHistoryID(id string) (scope, kind, num string, ok bool) {
	scope, rest, found := strings.Cut(id, "#")
	if !found || strings.Contains(scope, ":") {
		return "", "", "", false
	}
	kind, num, found = strings.Cut(rest, "/")
	if !found || num == "" || !slices.Contains([]string{KindPullRequest, KindIssue, KindWorkItem, KindWiki}, kind) {
		return "", "", "", false
	}
	return scope, kind, num, true
}

// Keeper is a connector whose reconcile listing leaves out items that
// still exist in the source, such as pull requests older than the window.
// Reconcile keeps a stored item that the listing does not have when Keep
// reports true.
type Keeper interface {
	Keep(id string, updated time.Time) bool
}

// Keep reports whether reconcile keeps a history item that the listing
// does not have: its kind is off, or it left the window. A wiki page has
// no window.
func (h History) Keep(id string, updated time.Time) bool {
	_, kind, _, ok := ParseHistoryID(id)
	if !ok {
		return false
	}
	switch kind {
	case KindWiki:
		return !h.Wiki
	case KindPullRequest:
		if !h.ReadsPullRequests() {
			return true
		}
	default:
		if !h.ReadsIssues() {
			return true
		}
	}
	return updated.Before(h.Start())
}

// JiraKeys returns the Jira issues that a text names, as entity references
// that keep the type of the stored issue.
func (h History) JiraKeys(text string) []*content.EntityRef {
	if len(h.Jira) == 0 {
		return nil
	}
	var out []*content.EntityRef
	seen := map[string]bool{}
	for _, m := range issueKeyRe.FindAllString(text, -1) {
		project, _, _ := strings.Cut(m, "-")
		for _, p := range h.Jira {
			if p.Key != project || seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, &content.EntityRef{Type: "issue", Key: "jira:" + p.Host + ":" + m, Aliases: []string{m}, KeepType: true})
			break
		}
	}
	return out
}

var issueKeyRe = regexp.MustCompile(`\b[A-Z][A-Z0-9_]+-[1-9][0-9]*\b`)

// SortRefs sorts refs by update time, oldest first, so that the cursor of a
// part that stops is never past an item that sync did not store.
func SortRefs(refs []Ref) {
	slices.SortStableFunc(refs, func(a, b Ref) int { return a.Updated.Compare(b.Updated) })
}

// Body collects the field paragraphs and sections of a history item. Each
// field is its own paragraph, so that each field is one passage and keeps
// its hash when another field changes.
type Body struct{ parts []string }

// Field adds one field. An empty value adds nothing. The value is not
// escaped: the normalized passage text keeps escapes, and the quotes of
// facts match against it.
func (b *Body) Field(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.parts = append(b.parts, "**"+label+":** "+strings.Join(strings.Fields(value), " "))
}

// Heading adds a heading of a level.
func (b *Body) Heading(level int, text string) {
	b.parts = append(b.parts, strings.Repeat("#", level)+" "+strings.Join(strings.Fields(text), " "))
}

// Markdown adds a Markdown text. Its headings move below level, so that a
// comment stays inside its section.
func (b *Body) Markdown(md string, level int) {
	md = strings.TrimSpace(strings.ReplaceAll(md, "\r\n", "\n"))
	if md == "" {
		return
	}
	lines := strings.Split(md, "\n")
	fence := false
	for i, l := range lines {
		t := strings.TrimLeft(l, " ")
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
		}
		if fence || !strings.HasPrefix(t, "#") {
			continue
		}
		n := len(t) - len(strings.TrimLeft(t, "#"))
		if n <= 6 && (len(t) == n || t[n] == ' ') {
			lines[i] = strings.Repeat("#", min(6, n+level)) + t[n:]
		}
	}
	b.parts = append(b.parts, strings.Join(lines, "\n"))
}

// String is the Markdown body.
func (b *Body) String() string { return strings.Join(b.parts, "\n\n") }
