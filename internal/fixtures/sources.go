package fixtures

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed corpus
var corpus embed.FS

// SourceToken is the token that the fake source APIs accept.
const SourceToken = "acme-token"

// Canary is the marker that only restricted content contains.
const Canary = "CANARY-7Q4Z"

// Page is a Confluence page of the corpus.
type Page struct {
	ID      string
	Space   string
	Title   string
	Parent  string
	Labels  []string
	Author  string
	Created time.Time
	Updated time.Time
	Version int
	Body    string // storage format
}

// Comment is a Jira comment.
type Comment struct {
	Author  string
	Created time.Time
	Body    string
}

// Issue is a Jira issue of the corpus.
type Issue struct {
	Key         string
	Project     string
	Type        string
	Summary     string
	Status      string
	Assignee    string
	Reporter    string
	Priority    string
	Labels      []string
	Components  []string
	FixVersions []string
	Due         string
	Parent      string
	Epic        string
	Links       []string // "Blocks PAY-3"
	Created     time.Time
	Updated     time.Time
	Description string // wiki markup
	Comments    []Comment
}

// Fault is a scripted failure of the next requests to a path prefix.
type Fault struct {
	Status     int
	RetryAfter string
	Count      int
}

// Hit is one request that a fake source received.
type Hit struct {
	Method string
	Path   string
	At     time.Time
}

// Sources holds the fake Confluence and Jira APIs and their state. Tests
// change the state through its methods.
type Sources struct {
	mu       sync.Mutex
	users    map[string]string // name to display name
	spaces   [][2]string
	projects [][2]string
	pages    map[string]*Page
	issues   map[string]*Issue
	faults   map[string]*Fault
	hits     []Hit
	tokens   map[string]bool
	now      func() time.Time
}

// NewSources loads the Acme corpus.
func NewSources() *Sources {
	s := &Sources{users: map[string]string{}, pages: map[string]*Page{}, issues: map[string]*Issue{},
		faults: map[string]*Fault{}, tokens: map[string]bool{SourceToken: true}, now: time.Now}
	for _, l := range lines("corpus/users.txt") {
		name, display, _ := strings.Cut(l, " ")
		s.users[name] = display
	}
	for _, l := range lines("corpus/confluence/spaces.txt") {
		k, n, _ := strings.Cut(l, " ")
		s.spaces = append(s.spaces, [2]string{k, n})
	}
	for _, l := range lines("corpus/jira/projects.txt") {
		k, n, _ := strings.Cut(l, " ")
		s.projects = append(s.projects, [2]string{k, n})
	}
	_ = fs.WalkDir(corpus, "corpus/confluence", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".html") {
			return err
		}
		h, body := document(p)
		pg := &Page{ID: strings.TrimSuffix(path.Base(p), ".html"), Space: path.Base(path.Dir(p)), Title: h["title"],
			Parent: h["parent"], Labels: list(h["labels"]), Author: h["author"], Created: when(h["created"]),
			Updated: when(h["updated"]), Version: 1, Body: body}
		s.pages[pg.ID] = pg
		return nil
	})
	_ = fs.WalkDir(corpus, "corpus/jira", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".txt") || path.Base(p) == "projects.txt" {
			return err
		}
		h, body := document(p)
		is := &Issue{Key: strings.TrimSuffix(path.Base(p), ".txt"), Project: path.Base(path.Dir(p)), Type: h["type"],
			Summary: h["summary"], Status: h["status"], Assignee: h["assignee"], Reporter: h["reporter"], Priority: h["priority"],
			Labels: list(h["labels"]), Components: list(h["components"]), FixVersions: list(h["fixVersions"]), Due: h["due"],
			Parent: h["parent"], Epic: h["epic"], Links: list(h["links"]), Created: when(h["created"]), Updated: when(h["updated"])}
		parts := strings.Split(body, "\n=== comment ")
		is.Description = strings.TrimSpace(parts[0])
		for _, c := range parts[1:] {
			head, text, _ := strings.Cut(c, "\n")
			author, at, _ := strings.Cut(head, " ")
			is.Comments = append(is.Comments, Comment{Author: author, Created: when(at), Body: strings.TrimSpace(text)})
		}
		s.issues[is.Key] = is
		return nil
	})
	return s
}

func lines(p string) []string {
	b, err := corpus.ReadFile(p)
	if err != nil {
		panic(err)
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// document reads "key: value" header lines, a "---" line and a body.
func document(p string) (map[string]string, string) {
	b, err := corpus.ReadFile(p)
	if err != nil {
		panic(err)
	}
	head, body, _ := strings.Cut(string(b), "\n---\n")
	h := map[string]string{}
	for _, l := range strings.Split(head, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok {
			h[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return h, body
}

func list(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func when(v string) time.Time {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		panic(fmt.Sprintf("fixtures: bad time %q", v))
	}
	return t
}

// ServeHTTP serves the Confluence API under /confluence and the Jira API
// under /jira.
func (s *Sources) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.hits = append(s.hits, Hit{Method: r.Method, Path: r.URL.Path, At: time.Now()})
	var fault *Fault
	for prefix, f := range s.faults {
		if strings.HasPrefix(r.URL.Path, prefix) && f.Count > 0 {
			f.Count--
			fault = &Fault{Status: f.Status, RetryAfter: f.RetryAfter}
			break
		}
	}
	authorized := s.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	s.mu.Unlock()
	if r.URL.Path == "/manifest.json" && r.Method == http.MethodGet {
		s.serveManifest(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, `{"message":"read-only fixture"}`, http.StatusMethodNotAllowed)
		return
	}
	if fault != nil {
		if fault.RetryAfter != "" {
			w.Header().Set("Retry-After", fault.RetryAfter)
		}
		http.Error(w, `{"message":"scripted failure"}`, fault.Status)
		return
	}
	if !authorized {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Client must be authenticated to access this resource."}`))
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/confluence/"):
		s.confluence(w, r)
	case strings.HasPrefix(r.URL.Path, "/jira/"):
		s.jira(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Fail makes the next count requests under prefix fail with status.
func (s *Sources) Fail(prefix string, status, count int, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[prefix] = &Fault{Status: status, RetryAfter: retryAfter, Count: count}
}

// RevokeToken makes the source refuse the token, as when its user loses
// access. AllowToken undoes it.
func (s *Sources) RevokeToken() { s.setToken(false) }

// AllowToken accepts the token again.
func (s *Sources) AllowToken() { s.setToken(true) }

func (s *Sources) setToken(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[SourceToken] = ok
}

// Hits returns the requests so far.
func (s *Sources) Hits() []Hit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.hits)
}

// PutPage adds or changes a page. A change raises the version and the
// update time.
func (s *Sources) PutPage(p Page) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.pages[p.ID]; ok {
		p.Version = old.Version + 1
		if p.Created.IsZero() {
			p.Created = old.Created
		}
	} else if p.Version == 0 {
		p.Version = 1
	}
	if p.Updated.IsZero() {
		p.Updated = s.now().UTC().Truncate(time.Second)
	}
	if p.Created.IsZero() {
		p.Created = p.Updated
	}
	s.pages[p.ID] = &p
}

// Page returns a copy of a page.
func (s *Sources) Page(id string) (Page, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pages[id]
	if !ok {
		return Page{}, false
	}
	return *p, true
}

// DeletePage removes a page.
func (s *Sources) DeletePage(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pages, id)
}

// PutIssue adds or changes an issue. A change raises the update time.
func (s *Sources) PutIssue(is Issue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.issues[is.Key]; ok && is.Created.IsZero() {
		is.Created = old.Created
	}
	if is.Updated.IsZero() {
		is.Updated = s.now().UTC().Truncate(time.Second)
	}
	if is.Created.IsZero() {
		is.Created = is.Updated
	}
	if is.Project == "" {
		is.Project, _, _ = strings.Cut(is.Key, "-")
	}
	s.issues[is.Key] = &is
}

// Issue returns a copy of an issue.
func (s *Sources) Issue(key string) (Issue, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	is, ok := s.issues[key]
	if !ok {
		return Issue{}, false
	}
	return *is, true
}

// Bulk adds n generated pages to a new space.
func (s *Sources) Bulk(space string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spaces = append(s.spaces, [2]string{space, "Acme bulk " + space})
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		id := fmt.Sprintf("%s-%05d", strings.ToLower(space), i)
		s.pages[id] = &Page{ID: id, Space: space, Title: fmt.Sprintf("Service note %d", i), Author: "sam.acme",
			Created: base, Updated: base.Add(time.Duration(i) * time.Second), Version: 1,
			Body: fmt.Sprintf("<h1>Summary</h1><p>Service %d sends events to the ledger.</p><h2>Owners</h2><ul><li>Team %d owns it.</li></ul>"+
				"<table><tbody><tr><th>Key</th><th>Value</th></tr><tr><td>tier</td><td>%d</td></tr></tbody></table>", i, i%40, i%3)}
	}
}

// Count returns the number of pages in a space, or of issues in a project.
func (s *Sources) Count(spaceOrProject string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.pages {
		if p.Space == spaceOrProject {
			n++
		}
	}
	for _, is := range s.issues {
		if is.Project == spaceOrProject {
			n++
		}
	}
	return n
}
