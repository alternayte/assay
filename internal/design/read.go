package design

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/pages"
	"github.com/alternayte/assay/internal/settings"
	"github.com/alternayte/assay/internal/visibility"
)

// ErrNotFound means that the entity has no design page for the reader: it
// is hidden, missing, or has no indexed code that the reader can see.
var ErrNotFound = errors.New("design: no design page")

// Section states.
const (
	StateFresh   = "fresh"
	StateStale   = "stale"
	StateMissing = "missing"
)

// DesignCite is one marker of a section as the reader sees it.
type DesignCite struct {
	ID string `json:"id" doc:"The marker, such as S1 or C2."`
	Cite
	Stale bool `json:"stale" doc:"The passage is no longer in the source."`
}

// DesignPageSection is one section as the reader sees it.
type DesignPageSection struct {
	Slug          string          `json:"slug"`
	Kind          string          `json:"kind" enum:"overview,architecture,component,data,repository"`
	Title         string          `json:"title"`
	State         string          `json:"state" enum:"fresh,stale,missing"`
	Markdown      string          `json:"markdown" doc:"Markers such as [S1] cite the sources."`
	Diagram       string          `json:"diagram" doc:"A Mermaid flowchart, or empty."`
	DiagramCites  []string        `json:"diagramCites" doc:"The markers of the diagram nodes and edges."`
	Cites         []DesignCite      `json:"cites"`
	Author        string          `json:"author" enum:"model,user,"`
	Pinned        bool            `json:"pinned" doc:"A person corrected the section. Regeneration does not replace it."`
	Editable      bool            `json:"editable" doc:"People can edit and report the section."`
	VersionID     int64           `json:"versionId,omitempty"`
	EditedAt      *time.Time      `json:"editedAt,omitempty"`
	Model         string          `json:"model,omitempty"`
	Lint          []pages.Failure `json:"lint"`
	ComponentPath string          `json:"componentPath,omitempty"`
	RepositoryKey string          `json:"repositoryKey,omitempty" doc:"The repository of a section of a system page."`
}

// DesignRunView is the newest build of a repository page.
type DesignRunView struct {
	Status     string     `json:"status" enum:"running,succeeded,stopped,failed"`
	Trigger    string     `json:"trigger"`
	Tokens     int32      `json:"tokens"`
	Digests    int32      `json:"digests"`
	Sections   int32      `json:"sections"`
	Reason     string     `json:"reason" doc:"Why a build stopped or failed."`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// DesignPage is a design page as one reader sees it.
type DesignPage struct {
	Key           string        `json:"key"`
	Type          string        `json:"type"`
	Name          string        `json:"name"`
	Level         string        `json:"level" enum:"repository,system" doc:"repository: one repository. system: a system or a service of several repositories."`
	Updating      bool          `json:"updating" doc:"A build runs. The page shows the last good version."`
	Run           *DesignRunView      `json:"run,omitempty"`
	VisibilityKey string        `json:"visibilityKey"`
	Sections      []DesignPageSection `json:"sections"`

	entityID    int64
	connections []int64
}

// Get returns the design page of a visible entity: a repository with
// indexed code, a system, or a service with two or more repositories.
func (s *Service) Get(ctx context.Context, vis visibility.Set, key string) (*DesignPage, error) {
	d, err := s.Entities.Get(ctx, vis, key)
	if errors.Is(err, entities.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	switch d.Type {
	case "repository":
		return s.repositoryPage(ctx, vis, d)
	case "system", "service":
		return s.systemPage(ctx, vis, d)
	}
	return nil, ErrNotFound
}

// repoConnection is the visible connection that holds the design page of
// a repository.
func (s *Service) repoConnection(ctx context.Context, vis visibility.Set, ids []int64) (map[int64]int64, error) {
	rows, err := New(s.Pool).RepositoryLinks(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]int64{}
	for _, r := range rows {
		if !slices.Contains(vis, r.ConnectionID) {
			continue
		}
		if cur, ok := out[r.EntityID]; !ok || r.ConnectionID < cur {
			out[r.EntityID] = r.ConnectionID
		}
	}
	return out, nil
}

func (s *Service) repositoryPage(ctx context.Context, vis visibility.Set, d *entities.Detail) (*DesignPage, error) {
	conns, err := s.repoConnection(ctx, vis, d.Members)
	if err != nil {
		return nil, err
	}
	conn, ok := conns[d.ID]
	if !ok {
		for _, m := range d.Members {
			if c, found := conns[m]; found {
				conn, ok = c, true
				break
			}
		}
	}
	if !ok {
		return nil, ErrNotFound
	}
	q := New(s.Pool)
	p := &DesignPage{Key: d.Key, Type: d.Type, Name: d.Name, Level: "repository", Sections: []DesignPageSection{}, entityID: d.ID, connections: []int64{conn}}
	vkey := pages.VisibilityKey(p.connections)
	p.VisibilityKey = hex.EncodeToString(vkey)
	if run, err := q.LastRun(ctx, d.ID); err == nil {
		p.Updating = run.Status == "running"
		p.Run = &DesignRunView{Status: run.Status, Trigger: run.Trigger, Tokens: run.Tokens, Digests: run.Digests, Sections: run.Sections, Reason: run.Reason,
			StartedAt: run.StartedAt, FinishedAt: run.FinishedAt}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	secs, err := q.Sections(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	vs, err := s.versions(ctx, secs)
	if err != nil {
		return nil, err
	}
	for _, sec := range secs {
		ps := DesignPageSection{Slug: sec.Slug, Kind: sec.Kind, Title: sec.Title, State: StateMissing, Editable: true, Lint: []pages.Failure{},
			ComponentPath: sec.ComponentPath, Cites: []DesignCite{}, DiagramCites: []string{}}
		if v := pick(vs[sec.ID], vis, vkey); v != nil {
			if err := s.fill(ctx, &ps, v); err != nil {
				return nil, err
			}
			if v.Author == "model" && !slices.Equal(v.InputHash, sec.InputHash) {
				ps.State = StateStale
			}
		}
		p.Sections = append(p.Sections, ps)
	}
	return p, nil
}

// versions loads the versions of sections, newest first.
func (s *Service) versions(ctx context.Context, secs []DesignSection) (map[int64][]DesignVersion, error) {
	ids := make([]int64, len(secs))
	for i, sec := range secs {
		ids[i] = sec.ID
	}
	rows, err := New(s.Pool).Versions(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64][]DesignVersion{}
	for _, v := range rows {
		out[v.SectionID] = append(out[v.SectionID], v)
	}
	return out, nil
}

// pick chooses the version that a reader sees: the newest user version
// whose connections are all visible, else the newest model version of the
// visibility key.
func pick(vs []DesignVersion, vis visibility.Set, vkey []byte) *DesignVersion {
	for i := range vs {
		v := &vs[i]
		if v.Author == "user" && subset(v.Connections, vis) {
			return v
		}
	}
	for i := range vs {
		v := &vs[i]
		if v.Author == "model" && slices.Equal(v.VisibilityKey, vkey) {
			return v
		}
	}
	return nil
}

func subset(a []int64, vis visibility.Set) bool {
	for _, x := range a {
		if !slices.Contains(vis, x) {
			return false
		}
	}
	return true
}

// fill sets the text, the diagram and the cites of a section from a
// version, with the state of each cited passage.
func (s *Service) fill(ctx context.Context, ps *DesignPageSection, v *DesignVersion) error {
	var set citeSet
	_ = json.Unmarshal(v.Cites, &set)
	gone, err := s.goneCites(ctx, v.Connections, set.Markers)
	if err != nil {
		return err
	}
	ps.State, ps.Markdown, ps.Diagram, ps.DiagramCites = StateFresh, v.Markdown, v.Diagram, orEmpty(set.Diagram)
	ps.Author, ps.Pinned, ps.VersionID, ps.EditedAt, ps.Model = v.Author, v.Author == "user", v.ID, &v.CreatedAt, v.Model
	_ = json.Unmarshal(v.Lint, &ps.Lint)
	ps.Lint = orEmpty(ps.Lint)
	ids := make([]string, 0, len(set.Markers))
	for id := range set.Markers {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, markerOrder)
	for _, id := range ids {
		ps.Cites = append(ps.Cites, DesignCite{ID: id, Cite: set.Markers[id], Stale: gone[id]})
	}
	if len(gone) > 0 {
		ps.State = StateStale
	}
	return nil
}

// markerOrder sorts S2 before S10 and passages before claims.
func markerOrder(a, b string) int {
	if a[0] != b[0] {
		return strings.Compare(b[:1], a[:1])
	}
	var x, y int
	_, _ = fmt.Sscanf(a[1:], "%d", &x)
	_, _ = fmt.Sscanf(b[1:], "%d", &y)
	return x - y
}

// system is the repositories and edges of a system or service for a
// reader.
type system struct {
	repos  []claims.Claim // implements or part_of claims with a repository subject
	edges  []claims.Claim
	repoID map[string]int64
}

// members finds the visible repositories of a system or a service and
// the claims between its parts.
func (s *Service) members(ctx context.Context, vis visibility.Set, d *entities.Detail) (*system, error) {
	sys := &system{repoID: map[string]int64{}}
	current := func(c claims.Claim) bool { return c.Status == claims.Supported || c.Status == claims.Conflicting }
	var services []int64
	parts := []int64{}
	for _, c := range d.Incoming {
		if !current(c) {
			continue
		}
		switch {
		case c.Predicate == "implements" && c.SubjectType == "repository":
			sys.repos = append(sys.repos, c)
		case c.Predicate == "part_of" && c.SubjectType == "repository":
			sys.repos = append(sys.repos, c)
		case c.Predicate == "part_of" && c.SubjectType == "service":
			services = append(services, c.SubjectID)
			parts = append(parts, c.SubjectID)
		}
	}
	if len(services) > 0 {
		cl, err := claims.Load(ctx, s.Pool, vis, claims.Query{Objects: services})
		if err != nil {
			return nil, err
		}
		for _, c := range cl {
			if current(c) && c.Predicate == "implements" && c.SubjectType == "repository" {
				sys.repos = append(sys.repos, c)
			}
		}
	}
	for _, c := range sys.repos {
		sys.repoID[c.SubjectKey] = c.SubjectID
		parts = append(parts, c.SubjectID)
	}
	if len(parts) > 0 {
		cl, err := claims.Load(ctx, s.Pool, vis, claims.Query{Subjects: parts})
		if err != nil {
			return nil, err
		}
		for _, c := range cl {
			if current(c) && c.ObjectKey != "" && slices.Contains([]string{"depends_on", "consumes_api", "provides_api"}, c.Predicate) {
				sys.edges = append(sys.edges, c)
			}
		}
	}
	return sys, nil
}

func (s *Service) systemPage(ctx context.Context, vis visibility.Set, d *entities.Detail) (*DesignPage, error) {
	sys, err := s.members(ctx, vis, d)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(sys.repoID))
	for _, id := range sys.repoID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	repoConns, err := s.repoConnection(ctx, vis, ids)
	if err != nil {
		return nil, err
	}
	if len(repoConns) == 0 || (d.Type == "service" && len(sys.repoID) < 2) {
		return nil, ErrNotFound
	}
	in, err := s.systemInput(ctx, vis, d, sys, repoConns)
	if err != nil {
		return nil, err
	}
	p := &DesignPage{Key: d.Key, Type: d.Type, Name: d.Name, Level: "system", Sections: []DesignPageSection{}, entityID: d.ID, connections: in.conns}
	vkey := pages.VisibilityKey(in.conns)
	p.VisibilityKey = hex.EncodeToString(vkey)
	secs, err := s.ensureSystemSections(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	vs, err := s.versions(ctx, secs)
	if err != nil {
		return nil, err
	}
	for _, sec := range secs {
		ps := DesignPageSection{Slug: sec.Slug, Kind: sec.Kind, Title: sec.Title, State: StateMissing, Editable: true, Lint: []pages.Failure{},
			Cites: []DesignCite{}, DiagramCites: []string{}}
		v := pick(vs[sec.ID], vis, vkey)
		switch {
		case v != nil:
			if err := s.fill(ctx, &ps, v); err != nil {
				return nil, err
			}
			if v.Author == "model" && !slices.Equal(v.InputHash, in.overview.hash) {
				ps.State = StateStale
			}
		case sec.Kind == KindArchitecture:
			s.fillComputed(&ps, "", in.diagram, in.diagramCites, in.claimCites)
		}
		p.Sections = append(p.Sections, ps)
	}
	for _, r := range in.repos {
		ps := DesignPageSection{Slug: "repository:" + r.key, Kind: KindRepository, Title: r.name, State: StateMissing, Lint: []pages.Failure{},
			Cites: []DesignCite{}, DiagramCites: []string{}, RepositoryKey: r.key}
		if r.overview != nil {
			s.fillComputed(&ps, r.lead, "", nil, r.cites)
		}
		p.Sections = append(p.Sections, ps)
	}
	return p, nil
}

// fillComputed sets a section that the page computes from other pages and
// claims. It has no version.
func (s *Service) fillComputed(ps *DesignPageSection, md, diagram string, diagramCites []string, cs map[string]Cite) {
	ps.State, ps.Markdown, ps.Diagram, ps.DiagramCites = StateFresh, md, diagram, orEmpty(diagramCites)
	ids := make([]string, 0, len(cs))
	for id := range cs {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, markerOrder)
	for _, id := range ids {
		ps.Cites = append(ps.Cites, DesignCite{ID: id, Cite: cs[id]})
	}
}

// ensureSystemSections creates the overview and architecture sections of
// a system page.
func (s *Service) ensureSystemSections(ctx context.Context, entityID int64) ([]DesignSection, error) {
	q := New(s.Pool)
	for _, w := range []struct {
		slug, kind, title string
		ord               int32
	}{{"overview", KindOverview, "Overview", 0}, {"architecture", KindArchitecture, "Architecture", 1}} {
		if _, err := q.UpsertSection(ctx, UpsertSectionParams{EntityID: entityID, Slug: w.slug, Kind: w.kind, Title: w.title, Ordinal: w.ord}); err != nil {
			return nil, err
		}
	}
	return q.Sections(ctx, entityID)
}

// systemRepo is one repository of a system page.
type systemRepo struct {
	key, name string
	overview  *DesignPageSection
	lead      string
	cites     map[string]Cite
}

// systemInputs is what a system page computes for a reader.
type systemInputs struct {
	conns        []int64
	repos        []systemRepo
	diagram      string
	diagramCites []string
	claimCites   map[string]Cite
	overview     *sectionInput
}

// systemInput reads the visible repository overviews and builds the
// diagram from the claims. Each node and edge of the diagram cites its
// claim.
func (s *Service) systemInput(ctx context.Context, vis visibility.Set, d *entities.Detail, sys *system, repoConns map[int64]int64) (*systemInputs, error) {
	out := &systemInputs{claimCites: map[string]Cite{}}
	conns := map[int64]bool{}
	keys := make([]string, 0, len(sys.repoID))
	for k := range sys.repoID {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	in := &sectionInput{cites: map[string]Cite{}, kind: KindOverview}
	var b strings.Builder
	b.WriteString("<context>\n")
	n := 0
	var overviewIDs []int64
	for _, key := range keys {
		id := sys.repoID[key]
		conn, ok := repoConns[id]
		if !ok {
			continue
		}
		conns[conn] = true
		rd, err := s.Entities.Get(ctx, vis, key)
		if err != nil {
			continue
		}
		rp, err := s.repositoryPage(ctx, vis, rd)
		if err != nil {
			continue
		}
		r := systemRepo{key: key, name: rd.Name, cites: map[string]Cite{}}
		for i := range rp.Sections {
			if rp.Sections[i].Slug == "overview" && rp.Sections[i].Markdown != "" {
				r.overview = &rp.Sections[i]
			}
		}
		if r.overview != nil {
			overviewIDs = append(overviewIDs, r.overview.VersionID)
			sents := slices.DeleteFunc(splitSentences(r.overview.Markdown), func(x string) bool { return x == NoReason })
			r.lead = strings.Join(sents[:min(2, len(sents))], " ")
			byID := map[string]Cite{}
			for _, c := range r.overview.Cites {
				byID[c.ID] = c.Cite
			}
			for _, m := range citedMarkers(r.lead) {
				r.cites[m] = byID[m]
			}
			text := markerOf.ReplaceAllStringFunc(r.overview.Markdown, func(m string) string {
				c, ok := byID[strings.Trim(m, "[]")]
				if !ok {
					return ""
				}
				n++
				nid := fmt.Sprintf("S%d", n)
				in.cites[nid] = c
				return "[" + nid + "]"
			})
			fmt.Fprintf(&b, "<component name=%q>\n%s\n</component>\n", rd.Name, data(text))
		}
		out.repos = append(out.repos, r)
	}
	var nodes []diagramNode
	var edges []diagramEdge
	seen := map[string]bool{}
	node := func(key, name string, cite string) {
		if !seen[key] {
			seen[key] = true
			nodes = append(nodes, diagramNode{ID: key, Label: name, Cites: []string{cite}})
		}
	}
	c := 0
	var claimKeys []string
	addClaim := func(x claims.Claim) string {
		c++
		id := fmt.Sprintf("C%d", c)
		text := x.SubjectName + " " + strings.ReplaceAll(x.Predicate, "_", " ") + " " + x.Object()
		cite := Cite{Kind: "claim", ClaimID: x.ID, Text: text}
		out.claimCites[id] = cite
		in.cites[id] = cite
		claimKeys = append(claimKeys, fmt.Sprintf("%d:%s", x.ID, x.Status))
		for _, e := range x.Evidence {
			conns[e.ConnectionID] = true
		}
		fmt.Fprintf(&b, "<claim id=%q>%s</claim>\n", id, data(text))
		return id
	}
	for _, x := range sys.repos {
		if _, ok := repoConns[x.SubjectID]; !ok {
			continue
		}
		id := addClaim(x)
		node(x.SubjectKey, x.SubjectName, id)
		if x.Predicate == "implements" {
			node(x.ObjectKey, x.ObjectName, id)
			edges = append(edges, diagramEdge{From: x.SubjectKey, To: x.ObjectKey, Label: "implements", Cites: []string{id}})
		}
	}
	for _, x := range sys.edges {
		if !seen[x.SubjectKey] {
			continue
		}
		id := addClaim(x)
		node(x.ObjectKey, x.ObjectName, id)
		edges = append(edges, diagramEdge{From: x.SubjectKey, To: x.ObjectKey, Label: strings.ReplaceAll(x.Predicate, "_", " "), Cites: []string{id}})
	}
	b.WriteString("</context>")
	known := map[string]bool{}
	for k := range out.claimCites {
		known[k] = true
	}
	out.diagram, out.diagramCites = mermaid(nodes, edges, known)
	for k := range conns {
		out.conns = append(out.conns, k)
	}
	slices.Sort(out.conns)
	in.prompt = b.String()
	in.hash = hashOf(PromptVersion, "system", d.Key, overviewIDs, claimKeys)
	out.overview = in
	return out, nil
}

// CompileSystem writes the overview of a system page for the reader's
// visibility key when it is missing or stale.
func (s *Service) CompileSystem(ctx context.Context, vis visibility.Set, key string, force bool) (*DesignPage, error) {
	d, err := s.Entities.Get(ctx, vis, key)
	if errors.Is(err, entities.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if d.Type != "system" && d.Type != "service" {
		return nil, ErrNotFound
	}
	p, err := s.systemPage(ctx, vis, d)
	if err != nil {
		return nil, err
	}
	var sec *DesignSection
	secs, err := New(s.Pool).Sections(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	for i := range secs {
		if secs[i].Slug == "overview" {
			sec = &secs[i]
		}
	}
	if sec == nil || (!force && p.Sections[0].State == StateFresh) {
		return p, nil
	}
	sys, err := s.members(ctx, vis, d)
	if err != nil {
		return nil, err
	}
	repoIDs := make([]int64, 0, len(sys.repoID))
	for _, id := range sys.repoID {
		repoIDs = append(repoIDs, id)
	}
	repoConns, err := s.repoConnection(ctx, vis, repoIDs)
	if err != nil {
		return nil, err
	}
	in, err := s.systemInput(ctx, vis, d, sys, repoConns)
	if err != nil {
		return nil, err
	}
	rules, err := settings.Read[Rules](ctx, s.Pool, Setting)
	if err != nil {
		return nil, err
	}
	v, err := s.generate(ctx, *sec, in.overview, tasks[KindSystemOverview], "", in.conns, &budget{limit: rules.TokenBudget})
	if err != nil {
		return nil, err
	}
	v.VisibilityKey, v.Connections = pages.VisibilityKey(in.conns), orEmpty(in.conns)
	if _, err := New(s.Pool).InsertVersion(ctx, *v); err != nil {
		return nil, err
	}
	return s.systemPage(ctx, vis, d)
}
