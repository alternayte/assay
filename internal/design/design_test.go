package design_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

type cite struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Path  string `json:"path"`
	Start int    `json:"start"`
	End   int    `json:"end"`
	Code  bool   `json:"code"`
	Hash  string `json:"hash"`
	Stale bool   `json:"stale"`
}

type section struct {
	Slug         string   `json:"slug"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	State        string   `json:"state"`
	Markdown     string   `json:"markdown"`
	Diagram      string   `json:"diagram"`
	DiagramCites []string `json:"diagramCites"`
	Cites        []cite   `json:"cites"`
	Author       string   `json:"author"`
	Pinned       bool     `json:"pinned"`
	VersionID    int64    `json:"versionId"`
	RepoKey      string   `json:"repositoryKey"`
}

type page struct {
	Key      string    `json:"key"`
	Level    string    `json:"level"`
	Updating bool      `json:"updating"`
	Sections []section `json:"sections"`
	Run      *struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
		Tokens int    `json:"tokens"`
	} `json:"run"`
}

type world struct {
	env                  *testkit.Env
	src                  *fixtures.Sources
	owner, rev, member   *testkit.Client
	github, azdo, hidden int64
	host, base           string
	fake                 *fixtures.Models
}

// start syncs the Acme repositories with code, shared with Everyone, turns
// on the design and decide functions, and builds the design pages.
func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.rev, _ = env.Invite(w.owner, "reviewer@acme.test", "reviewer", "reviewer-password-1")
	w.member, _ = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	w.src, w.base = testkit.FakeSources(t)
	u, _ := url.Parse(w.base)
	w.host = u.Host
	var openai, decisions string
	w.fake, openai, decisions = testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "design", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	dep := testkit.CreateEndpoint(t, w.owner, "decide", "decisions", decisions, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "decide", testkit.Function{Endpoint: dep, Model: "fake-decide"})
	repos := []string{"acme/payments-api", "acme/reminder-service", "acme/checkout-web", "acme/fraud-scoring"}
	w.github = w.connect(t, "acme-github", "github", map[string]any{"repositories": repos}, true)
	w.azdo = w.connect(t, "acme-azdo", "azdo", map[string]any{"repositories": []string{"Platform/ledger-service"}}, true)
	w.hidden = w.connect(t, "acme-worker", "github", map[string]any{"repositories": []string{"acme/payments-worker"}}, false)
	if _, err := env.Server.Resolve().Profiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []int64{w.github, w.azdo, w.hidden} {
		w.build(t, c)
	}
	return w
}

func (w *world) connect(t *testing.T, name, kind string, scope map[string]any, share bool) int64 {
	t.Helper()
	path := "/" + kind
	if kind == "azdo" {
		path = "/azdo/" + fixtures.AzdoCollection
	}
	id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: name, Kind: kind, BaseURL: w.base, Scope: scope, Path: path})
	if share {
		testkit.ShareWithEveryone(t, w.env, w.owner, id)
	}
	w.sync(t, id)
	return id
}

func (w *world) sync(t *testing.T, id int64) {
	t.Helper()
	if r := w.env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync %d: %+v", id, r)
	}
}

func (w *world) build(t *testing.T, conn int64) {
	t.Helper()
	if err := w.env.Server.Design().Build(context.Background(), conn, "", "sync"); err != nil {
		t.Fatal(err)
	}
}

func repoKey(host, repo string) string { return "github:" + strings.ToLower(host) + ":" + repo }

func designPath(key string) string { return "/api/v1/entities/" + url.PathEscape(key) + "/design" }

func get(t *testing.T, c *testkit.Client, key string) page {
	t.Helper()
	var p page
	c.Must(http.StatusOK, http.MethodGet, designPath(key), nil).JSON(t, &p)
	return p
}

func find(p page, slug string) section {
	for _, s := range p.Sections {
		if s.Slug == slug {
			return s
		}
	}
	return section{}
}

var sentenceEnd = regexp.MustCompile(`[.!?](?:\s*\[[^\]]*\])*(?:\s+|$)`)

// cited checks that each sentence of a section has a marker with a cite.
func cited(t *testing.T, s section) {
	t.Helper()
	ids := map[string]bool{}
	for _, c := range s.Cites {
		ids[c.ID] = true
	}
	text := strings.TrimSpace(s.Markdown)
	for text != "" {
		loc := sentenceEnd.FindStringIndex(text)
		sent := text
		if loc != nil {
			sent, text = text[:loc[1]], text[loc[1]:]
		} else {
			text = ""
		}
		sent = strings.TrimSpace(sent)
		if sent == "" || sent == "No document records the reasons for this design." {
			continue
		}
		ms := regexp.MustCompile(`\[([SC]\d+)\]`).FindAllStringSubmatch(sent, -1)
		if len(ms) == 0 {
			t.Errorf("%s: sentence without a source: %q", s.Slug, sent)
		}
		for _, m := range ms {
			if !ids[m[1]] {
				t.Errorf("%s: marker %s has no cite", s.Slug, m[1])
			}
		}
	}
}

// TestSCN_DSG_003_BuildWritesCitedRepositoryPage builds the design pages
// of the Acme repositories and checks the repository page: an overview, an
// architecture diagram, one section per component and a data section, each
// sentence with a source, code cites with a line range, and the Markdown
// form with footnotes.
func TestSCN_DSG_003_BuildWritesCitedRepositoryPage(t *testing.T) {
	w := start(t)
	key := repoKey(w.host, "acme/payments-api")
	p := get(t, w.member, key)
	if p.Level != "repository" || p.Run == nil || p.Run.Status != "succeeded" {
		t.Fatalf("page %+v", p)
	}
	var slugs []string
	for _, s := range p.Sections {
		slugs = append(slugs, s.Slug)
		if s.State != "fresh" {
			t.Errorf("section %s is %s", s.Slug, s.State)
		}
		cited(t, s)
	}
	want := []string{"overview", "architecture", "component:.", "component:cmd/server", "component:internal/ledgerclient",
		"component:internal/payments", "component:internal/store", "data"}
	if !slices.Equal(slugs, want) {
		t.Fatalf("sections %q, want %q", slugs, want)
	}
	arch := find(p, "architecture")
	if !strings.HasPrefix(arch.Diagram, "flowchart LR") || len(arch.DiagramCites) == 0 {
		t.Fatalf("architecture diagram %q cites %v", arch.Diagram, arch.DiagramCites)
	}
	comp := find(p, "component:internal/payments")
	var code *cite
	for i := range comp.Cites {
		if comp.Cites[i].Code {
			code = &comp.Cites[i]
		}
	}
	if code == nil || code.Start == 0 || !strings.Contains(code.URL, fmt.Sprintf("#L%d-L%d", code.Start, code.End)) || !strings.Contains(code.URL, "/blob/") {
		t.Fatalf("code cite %+v", code)
	}
	over := find(p, "overview")
	if !slices.ContainsFunc(over.Cites, func(c cite) bool {
		return !c.Code && strings.HasPrefix(c.Path, "README") || strings.HasPrefix(c.Path, "docs/")
	}) {
		t.Errorf("the overview cites no document: %+v", over.Cites)
	}
	md := string(w.member.Must(http.StatusOK, http.MethodGet, designPath(key)+".md", nil).Body)
	if !strings.Contains(md, "## Architecture") || !strings.Contains(md, "```mermaid") || !strings.Contains(md, "[^1]:") ||
		!strings.Contains(md, "lines ") {
		t.Fatalf("markdown:\n%s", md)
	}
	alias := string(w.member.Must(http.StatusOK, http.MethodGet, "/e/"+url.PathEscape(key)+"/design.md", nil).Body)
	if alias != md {
		t.Fatal("/e/{key}/design.md differs from the API form")
	}
	// A knowledge page of a team and an entity without code have no design page.
	w.member.Must(http.StatusNotFound, http.MethodGet, designPath("github:"+strings.ToLower(w.host)+":team:acme/payments-team"), nil)
}

// TestSCN_DSG_004_ChangesRegenerateOnlyTheirSections changes a cited
// declaration and then adds a package. Only the sections that cite the
// changed passage regenerate, and the new package gets a section.
func TestSCN_DSG_004_ChangesRegenerateOnlyTheirSections(t *testing.T) {
	w := start(t)
	key := repoKey(w.host, "acme/payments-api")
	before := get(t, w.owner, key)
	comp := find(before, "component:internal/store")
	var target cite
	for _, c := range comp.Cites {
		if c.Code {
			target = c
			break
		}
	}
	if target.Path == "" {
		t.Fatalf("no code cite in %+v", comp)
	}
	file := w.src.RepoFile("github", "acme/payments-api", target.Path)
	lines := strings.Split(string(file), "\n")
	lines[target.End-1] += " // changed"
	w.src.PutFile("github", "acme/payments-api", target.Path, []byte(strings.Join(lines, "\n")))
	w.sync(t, w.github)
	w.build(t, w.github)
	after := get(t, w.owner, key)
	for _, s := range after.Sections {
		old := find(before, s.Slug)
		citesTarget := slices.ContainsFunc(old.Cites, func(c cite) bool { return c.Hash == target.Hash })
		if changed := s.VersionID != old.VersionID; changed != citesTarget {
			t.Errorf("section %s: new version %v, cites the changed passage %v", s.Slug, changed, citesTarget)
		}
		if s.State != "fresh" {
			t.Errorf("section %s is %s after the build", s.Slug, s.State)
		}
	}
	w.src.PutFile("github", "acme/payments-api", "internal/audit/audit.go", []byte("// Package audit writes the audit trail of payments.\npackage audit\n\n// Log writes one line.\nfunc Log(line string) {}\n"))
	w.sync(t, w.github)
	w.build(t, w.github)
	if s := find(get(t, w.owner, key), "component:internal/audit"); s.Title != "audit" || s.State != "fresh" {
		t.Fatalf("new package section %+v", s)
	}
}

// TestSCN_DSG_005_CorrectionsArePinned checks the correction flows: a
// reviewer edit is pinned and survives a code change, which opens a
// design_update card; a member edit and a problem report become cards.
func TestSCN_DSG_005_CorrectionsArePinned(t *testing.T) {
	w := start(t)
	key := repoKey(w.host, "acme/payments-api")
	slug := "component:internal/store"
	path := designPath(key) + "/sections/" + url.PathEscape(slug)
	sec := find(get(t, w.rev, key), slug)
	text := "The store keeps each payment in one row of the payments table. " + regexp.MustCompile(`\[S\d+\]`).FindString(sec.Markdown)
	var saved struct {
		Version  int64 `json:"version"`
		Proposal int64 `json:"proposal"`
	}
	w.rev.Must(http.StatusOK, http.MethodPut, path, map[string]any{"markdown": text}).JSON(t, &saved)
	if saved.Version == 0 {
		t.Fatalf("reviewer edit %+v", saved)
	}
	got := find(get(t, w.member, key), slug)
	if !got.Pinned || got.Markdown != text || len(got.Cites) != 1 {
		t.Fatalf("pinned section %+v", got)
	}
	// A code change under the section keeps the edit and opens a card.
	for _, c := range sec.Cites {
		if c.Code {
			file := string(w.src.RepoFile("github", "acme/payments-api", c.Path))
			w.src.PutFile("github", "acme/payments-api", c.Path, []byte(file+"\n// Close is not needed.\n"))
			break
		}
	}
	w.src.PutFile("github", "acme/payments-api", "internal/store/close.go", []byte("package store\n\n// Close closes the pool.\nfunc (s *Store) Close() error { return s.db.Close() }\n"))
	w.sync(t, w.github)
	w.build(t, w.github)
	if got := find(get(t, w.member, key), slug); !got.Pinned || got.Markdown != text {
		t.Fatalf("the build replaced the pinned section: %+v", got)
	}
	if n := w.env.Count("SELECT count(*) FROM proposals WHERE kind = 'design_update' AND status = 'open'"); n != 1 {
		t.Fatalf("%d design_update cards", n)
	}
	w.build(t, w.github)
	if n := w.env.Count("SELECT count(*) FROM proposals WHERE kind = 'design_update'"); n != 1 {
		t.Fatalf("a second build opened another card: %d", n)
	}
	// A member edit is a card. Accept pins it.
	saved.Version, saved.Proposal = 0, 0
	w.member.Must(http.StatusOK, http.MethodPut, designPath(key)+"/sections/overview", map[string]any{"markdown": "The Payments API takes card payments."}).JSON(t, &saved)
	if saved.Proposal == 0 || saved.Version != 0 {
		t.Fatalf("member edit %+v", saved)
	}
	w.rev.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", saved.Proposal), map[string]any{})
	if got := find(get(t, w.member, key), "overview"); !got.Pinned || got.Markdown != "The Payments API takes card payments." {
		t.Fatalf("accepted member edit %+v", got)
	}
	// A problem report is a card. Accept without text queues a regeneration
	// with the reason.
	var rep struct {
		Proposal int64 `json:"proposal"`
	}
	w.member.Must(http.StatusCreated, http.MethodPost, designPath(key)+"/sections/data/problems", map[string]any{"reason": "Name the payments table."}).JSON(t, &rep)
	w.rev.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", rep.Proposal), map[string]any{})
	if n := w.env.Count("SELECT count(*) FROM river_job WHERE kind = 'design_section'"); n != 1 {
		t.Fatalf("%d section jobs", n)
	}
	var sectionID int64
	if err := w.env.Server.Pool().QueryRow(context.Background(), "SELECT id FROM design_sections WHERE slug = 'data' AND entity_id = (SELECT id FROM entities WHERE key = $1)", key).Scan(&sectionID); err != nil {
		t.Fatal(err)
	}
	if err := w.env.Server.Design().Regenerate(context.Background(), sectionID, "Name the payments table."); err != nil {
		t.Fatal(err)
	}
	if n := w.env.Count("SELECT count(*) FROM design_versions WHERE section_id = $1 AND note LIKE 'Regenerated with a reviewer instruction%'", sectionID); n != 1 {
		t.Fatalf("%d regenerated versions", n)
	}
	w.member.Must(http.StatusUnprocessableEntity, http.MethodPut, path, map[string]any{"markdown": "Text.", "diagram": "pie title x"})
}

// TestSCN_DSG_006_BudgetStopsTheBuild sets a small token budget: the build
// stops, keeps what it wrote, and the run shows the reason.
func TestSCN_DSG_006_BudgetStopsTheBuild(t *testing.T) {
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.src, w.base = testkit.FakeSources(t)
	u, _ := url.Parse(w.base)
	w.host = u.Host
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "design", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	if _, err := env.Server.Pool().Exec(context.Background(),
		`UPDATE settings SET value = '{"tokenBudget": 1000, "maxSections": 12, "sectionWords": 200}' WHERE key = 'design'`); err != nil {
		t.Fatal(err)
	}
	id := w.connect(t, "acme-github", "github", map[string]any{"repositories": []string{"acme/payments-api"}}, true)
	w.build(t, id)
	p := get(t, w.owner, repoKey(w.host, "acme/payments-api"))
	if p.Run == nil || p.Run.Status != "stopped" || !strings.Contains(p.Run.Reason, "token budget of 1000") {
		t.Fatalf("run %+v", p.Run)
	}
	missing := 0
	for _, s := range p.Sections {
		if s.State == "missing" {
			missing++
		}
	}
	if missing == 0 {
		t.Fatal("the build wrote all sections within the budget")
	}
}

// TestSCN_DSG_007_SystemPagesFollowTheVisibleSet groups repositories with
// repo_system cards and checks the system page of payments: a member sees
// the shared repositories, and never the repository of the owner's
// connection, in a section, a node or a cite.
func TestSCN_DSG_007_SystemPagesFollowTheVisibleSet(t *testing.T) {
	w := start(t)
	var cards struct {
		Cards []struct {
			ID      int64 `json:"id"`
			Payload struct {
				Repository string `json:"repository"`
				Option     string `json:"option"`
				Candidate  string `json:"candidate"`
			} `json:"payload"`
		} `json:"cards"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/cards?kind=repo_system", nil).JSON(t, &cards)
	system := "backstage:system:default/payments"
	accepted := map[string]bool{}
	for _, c := range cards.Cards {
		if c.Payload.Candidate == system {
			w.owner.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", c.ID), map[string]any{})
			accepted[c.Payload.Repository] = true
		}
	}
	worker := repoKey(w.host, "acme/payments-worker")
	checkout := repoKey(w.host, "acme/checkout-web")
	if !accepted[worker] || !accepted[checkout] {
		t.Fatalf("repo_system cards %+v", cards.Cards)
	}
	w.member.Must(http.StatusOK, http.MethodPost, designPath(system)+"/compile", nil)
	mine := get(t, w.member, system)
	if mine.Level != "system" {
		t.Fatalf("page %+v", mine)
	}
	repos := map[string]bool{}
	for _, s := range mine.Sections {
		if s.RepoKey != "" {
			repos[s.RepoKey] = true
		}
		if strings.Contains(s.Markdown+s.Diagram, "payments-worker") || strings.Contains(s.Markdown+s.Diagram, "settle") {
			t.Errorf("section %s shows the hidden repository: %q %q", s.Slug, s.Markdown, s.Diagram)
		}
		for _, c := range s.Cites {
			if strings.Contains(c.URL, "payments-worker") {
				t.Errorf("section %s cites the hidden repository: %+v", s.Slug, c)
			}
		}
	}
	if !repos[repoKey(w.host, "acme/payments-api")] || !repos[checkout] || repos[worker] {
		t.Fatalf("member repositories %v", repos)
	}
	if over := find(mine, "overview"); over.State != "fresh" || over.Markdown == "" {
		t.Fatalf("overview %+v", over)
	}
	w.owner.Must(http.StatusOK, http.MethodPost, designPath(system)+"/compile", nil)
	if theirs := get(t, w.owner, system); !slices.ContainsFunc(theirs.Sections, func(s section) bool { return s.RepoKey == worker }) {
		t.Fatalf("the owner does not see the worker: %+v", theirs.Sections)
	}
	w.member.Must(http.StatusNotFound, http.MethodGet, designPath(worker), nil)
}

// TestSCN_DSG_008_SearchAndAskReadCode checks that search shows source
// code only with the code filter, and that a question about how something
// works cites code with its line range.
func TestSCN_DSG_008_SearchAndAskReadCode(t *testing.T) {
	w := start(t)
	type results struct {
		Results []struct {
			Kind  string `json:"kind"`
			Title string `json:"title"`
		} `json:"results"`
	}
	var prose, code results
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/search?q=capture", nil).JSON(t, &prose)
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/search?q=capture&code=true", nil).JSON(t, &code)
	for _, r := range prose.Results {
		if r.Kind == "code" {
			t.Errorf("search without the code filter shows %s", r.Title)
		}
	}
	if len(code.Results) == 0 {
		t.Fatal("search with the code filter finds no code")
	}
	for _, r := range code.Results {
		if r.Kind != "code" {
			t.Errorf("search with the code filter shows %s of kind %s", r.Title, r.Kind)
		}
	}
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "answer", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	ask := func(q string) string {
		r := w.member.Do(http.MethodPost, "/api/v1/ask", map[string]string{"question": q})
		if r.Status != http.StatusOK {
			t.Fatalf("ask: %d %s", r.Status, r.Body)
		}
		return string(r.Body)
	}
	how := ask("How does the payments service capture a payment?")
	if !strings.Contains(how, `"kind":"code"`) || !regexp.MustCompile(`service\.go#L\d+-L\d+`).MatchString(how) {
		t.Fatalf("the answer to a how question cites no code with lines:\n%s", how)
	}
	if who := ask("Who owns the Payments API?"); strings.Contains(who, `"kind":"code"`) {
		t.Fatalf("the answer to a who question cites code:\n%s", who)
	}
}
