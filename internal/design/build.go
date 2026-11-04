package design

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/pages"
	"github.com/alternayte/assay/internal/settings"
	"github.com/alternayte/assay/internal/vocab"
)

// BuildArgs is the job that builds the design pages of the repositories of
// a connection after a sync. Repo limits it to one repository.
type BuildArgs struct {
	ConnectionID int64  `json:"connection_id"`
	Repo         string `json:"repo,omitempty"`
	Trigger      string `json:"trigger"`
}

// Kind is the River job kind.
func (BuildArgs) Kind() string { return "design_build" }

// Queue is the River queue of design jobs.
const Queue = "design"

// InsertOpts retries a failed build once. Builds are not unique: a build
// whose inputs did not change calls no model.
func (BuildArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: Queue, MaxAttempts: 2} }

// BuildWorker works design builds.
type BuildWorker struct {
	river.WorkerDefaults[BuildArgs]
	S *Service
}

// Timeout bounds one build.
func (w *BuildWorker) Timeout(*river.Job[BuildArgs]) time.Duration { return 3 * time.Hour }

// Work builds the design pages of the job.
func (w *BuildWorker) Work(ctx context.Context, job *river.Job[BuildArgs]) error {
	return w.S.Build(ctx, job.Args.ConnectionID, job.Args.Repo, job.Args.Trigger)
}

// QueueBuild queues the build of the repositories of a connection in tx.
// Sync calls it when a run of a connection with code globs succeeds.
func (s *Service) QueueBuild(ctx context.Context, tx pgx.Tx, connectionID int64) error {
	_, err := s.River.InsertTx(ctx, tx, BuildArgs{ConnectionID: connectionID, Trigger: "sync"}, nil)
	return err
}

// Limits of the model inputs, in tokens.
const (
	digestInputTokens  = 6000
	digestOutputTokens = 2500
	sectionDigestWords = 220
	sectionOutTokens   = 1500
	maxDocPassages     = 12
)

// repoInput is what a build knows about one repository.
type repoInput struct {
	connectionID int64
	kind         string
	entityID     int64
	key, name    string
	repo         string
	items        []RepositoryItemsRow
	passages     map[int64][]ItemPassagesRow
	// specAPIs are the spec apis of the repository by protocol. Inferred
	// operations attach to them.
	specAPIs map[string]content.EntityRef
}

// budget counts the tokens of one build.
type budget struct {
	limit, used int
}

func (b *budget) add(r *models.ChatResult, prompt int) {
	if r.TokensIn+r.TokensOut > 0 {
		b.used += r.TokensIn + r.TokensOut
		return
	}
	b.used += prompt + models.EstimateTokens(r.Content)
}

func (b *budget) left() bool { return b.used < b.limit }

// errBudget stops a build at the token budget.
var errBudget = errors.New("design: the token budget is used up")

// Build builds the design pages of the repositories of a connection: the
// components and their digests, then the sections. Only the parts whose
// inputs changed call the model.
func (s *Service) Build(ctx context.Context, connectionID int64, onlyRepo, trigger string) error {
	q := New(s.Pool)
	if err := q.FailStaleRuns(ctx, FailStaleRunsParams{Now: ptr(s.now()), Before: s.now().Add(-4 * time.Hour)}); err != nil {
		return err
	}
	conn, err := q.CodeConnection(ctx, connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(conn.CodeGlobs) == 0 {
		return nil
	}
	vis := []int64{connectionID}
	items, err := q.RepositoryItems(ctx, RepositoryItemsParams{Visible: vis, ConnectionID: connectionID})
	if err != nil {
		return err
	}
	byRepo := map[string][]RepositoryItemsRow{}
	var repos []string
	for _, it := range items {
		if onlyRepo != "" && it.Repo != onlyRepo {
			continue
		}
		if _, ok := byRepo[it.Repo]; !ok {
			repos = append(repos, it.Repo)
		}
		byRepo[it.Repo] = append(byRepo[it.Repo], it)
	}
	for _, repo := range repos {
		if !slices.ContainsFunc(byRepo[repo], func(it RepositoryItemsRow) bool { return it.Kind == "code" }) {
			continue
		}
		e, err := q.RepositoryEntity(ctx, RepositoryEntityParams{Visible: vis, AliasNorm: entities.NormalizeAlias(repo)})
		if errors.Is(err, pgx.ErrNoRows) {
			s.Log.Warn("design build skipped a repository without an entity", "connection", connectionID, "repository", repo)
			continue
		}
		if err != nil {
			return err
		}
		in := &repoInput{connectionID: connectionID, kind: conn.Kind, entityID: e.ID, key: e.Key, name: path.Base(repo), repo: repo, items: byRepo[repo]}
		if err := s.buildRepo(ctx, in, trigger); err != nil {
			return err
		}
	}
	return nil
}

// buildRepo builds the design page of one repository and records the run.
func (s *Service) buildRepo(ctx context.Context, in *repoInput, trigger string) error {
	q := New(s.Pool)
	runID, err := q.StartRun(ctx, StartRunParams{EntityID: in.entityID, ConnectionID: in.connectionID, Trigger: trigger, Now: s.now()})
	if err != nil {
		return err
	}
	rules, err := settings.Read[Rules](ctx, s.Pool, Setting)
	if err != nil {
		return err
	}
	b := &budget{limit: rules.TokenBudget}
	st := &runStats{}
	status, reason := "succeeded", ""
	err = s.buildSteps(ctx, in, rules, b, st)
	switch {
	case errors.Is(err, errBudget):
		status = "stopped"
		reason = fmt.Sprintf("The run reached the token budget of %d tokens. The other parts stay stale until the next run.", rules.TokenBudget)
	case errors.Is(err, models.ErrDisabled):
		status, reason = "stopped", "The design model is off. Turn on the design function on the Models page."
	case errors.Is(err, models.ErrBudgetExceeded):
		status, reason = "stopped", "The monthly budget of the design function is used up."
	case err != nil && ctx.Err() == nil:
		status, reason = "failed", "The build failed: "+err.Error()
		s.Log.Warn("design build failed", "entity", in.key, "error", err.Error())
	}
	finish := FinishRunParams{ID: runID, Status: status, Tokens: int32(min(b.used, 1<<30)), Digests: st.digests, Sections: st.sections, //nolint:gosec // Bounded.
		Reason: reason, Now: ptr(s.now())}
	if ferr := q.FinishRun(context.WithoutCancel(ctx), finish); ferr != nil {
		return ferr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.Resolve != nil {
		if err := s.Resolve.ProposeEdges(ctx, in.connectionID, in.key); err != nil {
			s.Log.Warn("design edges not proposed", "entity", in.key, "error", err.Error())
		}
		if err := s.Resolve.RepoSystem(ctx, in.key); err != nil {
			s.Log.Warn("repo_system not asked", "entity", in.key, "error", err.Error())
		}
	}
	return nil
}

type runStats struct {
	digests, sections int32
}

// buildSteps finds the components, writes the digests whose code changed
// and then the sections whose inputs changed.
func (s *Service) buildSteps(ctx context.Context, in *repoInput, rules Rules, b *budget, st *runStats) error {
	q := New(s.Pool)
	vis := []int64{in.connectionID}
	ids := make([]int64, len(in.items))
	files := make([]File, len(in.items))
	byPath := map[string]RepositoryItemsRow{}
	for i, it := range in.items {
		ids[i] = it.ID
		files[i] = File{Path: it.Path, Code: it.Kind == "code", Passages: int(it.Passages)}
		byPath[it.Path] = it
	}
	ps, err := q.ItemPassages(ctx, ItemPassagesParams{Items: ids, Visible: vis})
	if err != nil {
		return err
	}
	in.passages = map[int64][]ItemPassagesRow{}
	for _, p := range ps {
		in.passages[p.ItemID] = append(in.passages[p.ItemID], p)
	}
	if in.specAPIs, err = s.specAPIs(ctx, in); err != nil {
		return err
	}
	comps := Components(in.name, files, rules.MaxSections)
	old, err := q.Components(ctx, ComponentsParams{EntityID: in.entityID, ConnectionID: in.connectionID})
	if err != nil {
		return err
	}
	stored := map[string]DesignComponent{}
	for _, c := range old {
		stored[c.Path] = c
	}
	secs, err := s.ensureSections(ctx, in.entityID, comps)
	if err != nil {
		return err
	}
	catalog := catalogPaths(in.items)
	keep := make([]string, len(comps))
	for i, c := range comps {
		keep[i] = c.Path
		var hashes []string
		for _, f := range c.Files {
			for _, p := range in.passages[byPath[f].ID] {
				hashes = append(hashes, hex.EncodeToString(p.Hash))
			}
		}
		slices.Sort(hashes)
		set := hashOf(PromptVersion, hashes)
		row, err := q.UpsertComponent(ctx, UpsertComponentParams{EntityID: in.entityID, ConnectionID: in.connectionID, Repo: in.repo, Path: c.Path, Name: c.Name,
			Kind: c.Kind, Files: int32(len(c.Files)), Passages: int32(c.Passages), PassageSetHash: set, CatalogKey: catalog[c.Path], Now: s.now()}) //nolint:gosec // Bounded by the file limit.
		if err != nil {
			return err
		}
		prev, had := stored[c.Path]
		if had && slices.Equal(prev.PassageSetHash, set) && prev.DigestHash != nil {
			continue
		}
		if !b.left() {
			return errBudget
		}
		if err := s.digest(ctx, in, row, c, byPath, b); err != nil {
			return err
		}
		st.digests++
	}
	if err := q.DeleteComponents(ctx, DeleteComponentsParams{EntityID: in.entityID, ConnectionID: in.connectionID, Keep: keep}); err != nil {
		return err
	}
	for _, sec := range secs {
		if !b.left() {
			return errBudget
		}
		built, err := s.buildSection(ctx, in.connectionID, sec, "", b)
		if err != nil {
			return err
		}
		if built {
			st.sections++
		}
	}
	return nil
}

// catalogPaths maps the directory of each catalog-info.yaml to the key of
// its first Component.
func catalogPaths(items []RepositoryItemsRow) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		if path.Base(it.Path) == "catalog-info.yaml" {
			out[path.Dir(it.Path)] = it.ExternalID
		}
	}
	return out
}

// digest writes the internal summary of a component from its code, and the
// contracts that the code states as inferred facts of its passages.
func (s *Service) digest(ctx context.Context, in *repoInput, row DesignComponent, c Component, byPath map[string]RepositoryItemsRow, b *budget) error {
	cs := map[string]Cite{}
	byID := map[string]marked{}
	var items []int64
	var body strings.Builder
	fmt.Fprintf(&body, "<component path=%q kind=%q files=\"%d\">\n", c.Path, c.Kind, len(c.Files))
	tokens := 0
	n := 0
	for _, f := range c.Files {
		it := byPath[f]
		items = append(items, it.ID)
		for _, p := range in.passages[it.ID] {
			t := models.EstimateTokens(p.Text)
			if tokens+t > digestInputTokens {
				break
			}
			tokens += t
			n++
			id := fmt.Sprintf("S%d", n)
			cs[id] = passageCite(it, p, in.kind)
			byID[id] = marked{item: it.ID, row: p}
			fmt.Fprintf(&body, "<passage id=%q file=%q lines=\"%d-%d\">\n%s\n</passage>\n", id, f, deref(p.StartLine), deref(p.EndLine), data(p.Text))
		}
	}
	body.WriteString("</component>")
	if n == 0 {
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if err := content.WriteInferred(ctx, tx, in.connectionID, items, nil, s.now(), s.Log); err != nil {
				return err
			}
			return New(tx).SetDigest(ctx, SetDigestParams{ID: row.ID, Digest: "", DigestCites: []byte("{}"), DigestHash: hashOf(row.PassageSetHash), Now: s.now()})
		})
	}
	msgs := []models.Message{{Role: "system", Content: DigestPrompt}, {Role: "user", Content: body.String()}}
	res, err := s.Models.Chat(ctx, models.Design, models.ChatRequest{Messages: msgs, MaxTokens: digestOutputTokens, Schema: digestSchema(),
		SchemaName: "design_digest", Ref: "design:digest:" + in.key})
	if err != nil {
		return err
	}
	b.add(res, tokens)
	var ans digestAnswer
	if err := json.Unmarshal([]byte(res.Content), &ans); err != nil {
		ans = digestAnswer{Notes: res.Content}
	}
	notes := keepCited(ans.Notes, cs)
	used := map[string]Cite{}
	for _, m := range citedMarkers(notes) {
		used[m] = cs[m]
	}
	raw, _ := json.Marshal(used)
	facts := inferred(in, ans, byID)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := content.WriteInferred(ctx, tx, in.connectionID, items, facts, s.now(), s.Log); err != nil {
			return err
		}
		return New(tx).SetDigest(ctx, SetDigestParams{ID: row.ID, Digest: notes, DigestCites: raw, DigestHash: hashOf(notes, used), Now: s.now()})
	})
}

var (
	markerGroup = regexp.MustCompile(`\[\s*([A-Za-z]+\d+(?:\s*[,;]\s*[A-Za-z]+\d+)*)\s*\]`)
	markerID    = regexp.MustCompile(`[A-Za-z]+\d+`)
)

// keepCited keeps the list lines of a digest that cite a known passage,
// with only the known markers.
func keepCited(text string, cs map[string]Cite) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		ok := false
		l = markerGroup.ReplaceAllStringFunc(l, func(g string) string {
			var keep strings.Builder
			for _, id := range markerID.FindAllString(g, -1) {
				id = strings.ToUpper(id)
				if _, found := cs[id]; found {
					keep.WriteString("[" + id + "]")
					ok = true
				}
			}
			return keep.String()
		})
		if ok {
			out = append(out, "- "+strings.TrimSpace(strings.TrimLeft(l, "-*+ ")))
		}
	}
	return strings.Join(out, "\n")
}

func passageCite(it RepositoryItemsRow, p ItemPassagesRow, kind string) Cite {
	text := []rune(p.Norm)
	if len(text) > 160 {
		text = text[:160]
	}
	start, end := int(deref(p.StartLine)), int(deref(p.EndLine))
	return Cite{Kind: "passage", ItemID: it.ID, Hash: hex.EncodeToString(p.Hash), Title: it.Title, URL: lineURL(it.Url, start, end), Path: it.Path,
		Start: start, End: end, Code: it.Kind == "code", SourceType: kind, Text: string(text)}
}

// ensureSections creates the sections of a repository page and removes the
// sections of components that are gone.
func (s *Service) ensureSections(ctx context.Context, entityID int64, comps []Component) ([]DesignSection, error) {
	q := New(s.Pool)
	type want struct {
		slug, kind, title, comp string
		ordinal                 int32
	}
	ws := []want{{"overview", KindOverview, "Overview", "", 0}, {"architecture", KindArchitecture, "Architecture", "", 1}}
	var keep []string
	for i, c := range comps {
		slug := "component:" + c.Path
		keep = append(keep, slug)
		ws = append(ws, want{slug, KindComponent, c.Name, c.Path, int32(10 + i)}) //nolint:gosec // At most 40 sections.
	}
	ws = append(ws, want{"data", KindData, "Data", "", 1000})
	for _, w := range ws {
		if _, err := q.UpsertSection(ctx, UpsertSectionParams{EntityID: entityID, Slug: w.slug, Kind: w.kind, Title: w.title, Ordinal: w.ordinal,
			ComponentPath: w.comp}); err != nil {
			return nil, err
		}
	}
	if err := q.RemoveSections(ctx, RemoveSectionsParams{EntityID: entityID, Kind: KindComponent, Keep: orEmpty(keep), Now: ptr(s.now())}); err != nil {
		return nil, err
	}
	return q.Sections(ctx, entityID)
}

// sectionInput is the context of one section call.
type sectionInput struct {
	prompt string
	cites  map[string]Cite
	hash   []byte
	kind   string
}

// repoSectionInput assembles the context of a repository section from the
// stored digests, the knowledge passages and the claims.
func (s *Service) repoSectionInput(ctx context.Context, connectionID int64, sec DesignSection) (*sectionInput, error) {
	q := New(s.Pool)
	vis := []int64{connectionID}
	comps, err := q.Components(ctx, ComponentsParams{EntityID: sec.EntityID, ConnectionID: connectionID})
	if err != nil {
		return nil, err
	}
	conn, err := q.CodeConnection(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	kind := conn.Kind
	in := &sectionInput{cites: map[string]Cite{}, kind: sec.Kind}
	n := 0
	var b strings.Builder
	b.WriteString("<context>\n")
	remap := func(digest string, dc map[string]Cite) string {
		return markerOf.ReplaceAllStringFunc(digest, func(m string) string {
			c, ok := dc[strings.Trim(m, "[]")]
			if !ok {
				return ""
			}
			for id, x := range in.cites {
				if x.ItemID == c.ItemID && x.Hash == c.Hash {
					return "[" + id + "]"
				}
			}
			n++
			id := fmt.Sprintf("S%d", n)
			in.cites[id] = c
			return "[" + id + "]"
		})
	}
	var digests []string
	var compName string
	for _, c := range comps {
		if sec.Kind == KindComponent && c.Path != sec.ComponentPath {
			continue
		}
		if sec.Kind == KindComponent {
			compName = c.Name
		}
		var dc map[string]Cite
		_ = json.Unmarshal(c.DigestCites, &dc)
		digest := c.Digest
		if sec.Kind != KindComponent {
			digest = cutWords(digest, sectionDigestWords)
		}
		digests = append(digests, c.Path+"\x00"+digest)
		fmt.Fprintf(&b, "<component name=%q path=%q kind=%q>\n%s\n</component>\n", c.Name, c.Path, c.Kind, data(remap(digest, dc)))
	}
	items, err := q.RepositoryItems(ctx, RepositoryItemsParams{Visible: vis, ConnectionID: connectionID})
	if err != nil {
		return nil, err
	}
	repo := ""
	if len(comps) > 0 {
		repo = comps[0].Repo
	}
	var docs []RepositoryItemsRow
	var docIDs []int64
	for _, it := range items {
		if it.Kind == "code" || it.Repo != repo || !docFile(it.Path) {
			continue
		}
		docs = append(docs, it)
		docIDs = append(docIDs, it.ID)
	}
	ps, err := q.ItemPassages(ctx, ItemPassagesParams{Items: docIDs, Visible: vis})
	if err != nil {
		return nil, err
	}
	var docHashes []string
	k := 0
	for _, it := range docs {
		for _, p := range ps {
			if p.ItemID != it.ID || k == maxDocPassages {
				continue
			}
			if sec.Kind == KindComponent && sec.ComponentPath != "." && !strings.HasPrefix(it.Path, sec.ComponentPath+"/") &&
				!strings.Contains(strings.ToLower(p.Norm), strings.ToLower(compName)) {
				continue
			}
			k++
			n++
			id := fmt.Sprintf("S%d", n)
			in.cites[id] = passageCite(it, p, kind)
			docHashes = append(docHashes, hex.EncodeToString(p.Hash))
			fmt.Fprintf(&b, "<passage id=%q kind=\"doc\" file=%q>%s</passage>\n", id, it.Path, data(p.Norm))
		}
	}
	var claimKeys []string
	if sec.Kind == KindOverview {
		cl, err := claims.Load(ctx, s.Pool, vis, claims.Query{Subjects: []int64{sec.EntityID}})
		if err != nil {
			return nil, err
		}
		c := 0
		for _, x := range cl {
			if x.Status != claims.Supported && x.Status != claims.Conflicting {
				continue
			}
			switch x.Predicate {
			case "owned_by", "implements", "part_of", "depends_on", "consumes_api", "provides_api":
			default:
				continue
			}
			c++
			id := fmt.Sprintf("C%d", c)
			text := x.SubjectName + " " + strings.ReplaceAll(x.Predicate, "_", " ") + " " + x.Object()
			in.cites[id] = Cite{Kind: "claim", ClaimID: x.ID, Text: text}
			claimKeys = append(claimKeys, fmt.Sprintf("%d:%s", x.ID, x.Status))
			fmt.Fprintf(&b, "<claim id=%q>%s</claim>\n", id, data(text))
		}
	}
	b.WriteString("</context>")
	in.prompt = b.String()
	in.hash = hashOf(PromptVersion, sec.Kind, sec.ComponentPath, digests, docHashes, claimKeys)
	return in, nil
}

// docFile reports whether a knowledge file is a source for the reasons of
// a design: a README, a document or a decision record.
func docFile(p string) bool {
	lower := strings.ToLower(p)
	if strings.HasPrefix(path.Base(lower), "readme") {
		return true
	}
	return strings.HasSuffix(lower, ".md") && (strings.HasPrefix(lower, "docs/") || strings.Contains(lower, "/docs/") ||
		strings.Contains(lower, "adr/") || strings.Contains(lower, "decisions/"))
}

func cutWords(s string, n int) string {
	var out []string
	words := 0
	for _, l := range strings.Split(s, "\n") {
		w := len(strings.Fields(l))
		if words+w > n && len(out) > 0 {
			break
		}
		words += w
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// buildSection writes a new model version of a repository section when
// its input changed, or always when an instruction asks for a change. A
// pinned section keeps its user version: a stale one gets a design_update
// card with the model draft.
func (s *Service) buildSection(ctx context.Context, connectionID int64, sec DesignSection, instruction string, b *budget) (bool, error) {
	q := New(s.Pool)
	in, err := s.repoSectionInput(ctx, connectionID, sec)
	if err != nil {
		return false, err
	}
	if err := q.SetSectionInput(ctx, SetSectionInputParams{ID: sec.ID, InputHash: in.hash}); err != nil {
		return false, err
	}
	vkey := pages.VisibilityKey([]int64{connectionID})
	latest, err := q.LatestModelVersion(ctx, LatestModelVersionParams{SectionID: sec.ID, VisibilityKey: vkey})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if instruction == "" && err == nil && slices.Equal(latest.InputHash, in.hash) {
		stale, err := s.staleCites(ctx, latest)
		if err != nil {
			return false, err
		}
		if !stale {
			return false, s.checkPinned(ctx, sec, in.hash, latest, []int64{connectionID})
		}
	}
	task := tasks[sec.Kind]
	v, err := s.generate(ctx, sec, in, task, instruction, []int64{connectionID}, b)
	if err != nil {
		return false, err
	}
	v.VisibilityKey, v.Connections = vkey, []int64{connectionID}
	id, err := q.InsertVersion(ctx, *v)
	if err != nil {
		return false, err
	}
	nv, err := q.GetVersion(ctx, id)
	if err != nil {
		return false, err
	}
	return true, s.checkPinned(ctx, sec, in.hash, nv, []int64{connectionID})
}

// generate calls the design model for a section and checks the answer. A
// failed check gets one retry. After it, the sentences that still fail are
// dropped, so that every sentence keeps a source.
func (s *Service) generate(ctx context.Context, sec DesignSection, in *sectionInput, task, instruction string, conns []int64, b *budget) (*InsertVersionParams, error) {
	pr, err := settings.Read[pages.PageRules](ctx, s.Pool, pages.Setting)
	if err != nil {
		return nil, err
	}
	rules, err := settings.Read[Rules](ctx, s.Pool, Setting)
	if err != nil {
		return nil, err
	}
	pr.SummaryWords = rules.SectionWords
	if sec.Kind == KindArchitecture {
		pr.SummaryWords = min(rules.SectionWords, 100)
	}
	terms, err := s.terms(ctx, conns)
	if err != nil {
		return nil, err
	}
	user := in.prompt
	if instruction != "" {
		user += "\n<instruction>" + data(instruction) + "</instruction>"
	}
	msgs := []models.Message{{Role: "system", Content: fmt.Sprintf(SectionPrompt, pr.SentenceWords, pr.SummaryWords, task)},
		{Role: "user", Content: user}}
	known := map[string]bool{}
	for k := range in.cites {
		known[k] = true
	}
	var ans sectionAnswer
	var ck checked
	var model string
	kept := -1
	for range 2 {
		res, err := s.Models.Chat(ctx, models.Design, models.ChatRequest{Messages: msgs, MaxTokens: sectionOutTokens, Schema: sectionSchema(),
			SchemaName: "design_section", Ref: "design:section:" + sec.Slug})
		if err != nil {
			return nil, err
		}
		b.add(res, models.EstimateTokens(user))
		var a sectionAnswer
		if err := json.Unmarshal([]byte(res.Content), &a); err != nil {
			a.Markdown = res.Content
		}
		c := checkText(a.Markdown, in.cites, pr, terms)
		// The retry replaces the first answer only when it keeps more
		// cited sentences.
		if n := len(splitSentences(drop(c.Markdown, c.Bad))); n > kept {
			ans, ck, model, kept = a, c, res.Model, n
		}
		if len(c.Bad) == 0 && c.Markdown != "" {
			break
		}
		msgs = append(msgs, models.Message{Role: "assistant", Content: res.Content}, models.Message{Role: "user", Content: failureText(c.Failures)})
	}
	md := drop(ck.Markdown, ck.Bad)
	if sec.Kind == KindOverview && !citesDoc(md, in.cites) {
		md = strings.TrimSpace(md + "\n\n" + NoReason)
	}
	diagram, dm := "", []string(nil)
	if sec.Kind != KindOverview {
		diagram, dm = mermaid(ans.Nodes, ans.Edges, known)
	}
	used := citeSet{Markers: map[string]Cite{}, Diagram: orEmpty(dm)}
	for _, m := range append(citedMarkers(md), dm...) {
		used.Markers[m] = in.cites[m]
	}
	var remaining []pages.Failure
	for _, f := range ck.Failures {
		if f.Rule != pages.RuleUncited && f.Rule != RuleReason {
			remaining = append(remaining, f)
		}
	}
	raw, _ := json.Marshal(used)
	lint, _ := json.Marshal(orEmpty(remaining))
	note := ""
	if instruction != "" {
		note = "Regenerated with a reviewer instruction: " + instruction
	}
	return &InsertVersionParams{SectionID: sec.ID, Author: "model", Markdown: md, Diagram: diagram, Cites: raw, InputHash: in.hash, Lint: lint,
		Model: model, Note: note, Now: s.now()}, nil
}

// NoReason is the sentence of an overview that cites no document.
const NoReason = "No document records the reasons for this design."

func citesDoc(md string, cs map[string]Cite) bool {
	for _, m := range citedMarkers(md) {
		if c, ok := cs[m]; ok && !c.Code {
			return true
		}
	}
	return false
}

// terms are the terms that the lint checks for deprecated forms.
func (s *Service) terms(ctx context.Context, vis []int64) ([]vocab.Term, error) {
	if s.Vocab == nil {
		return nil, nil
	}
	return s.Vocab.Terms(ctx, vis)
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func ptr[T any](v T) *T { return &v }

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
