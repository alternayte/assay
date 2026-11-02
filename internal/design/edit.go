package design

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/pages"
	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/settings"
	"github.com/alternayte/assay/internal/visibility"
)

// SectionEdit is a corrected section.
type SectionEdit struct {
	Markdown string `json:"markdown" minLength:"1" maxLength:"8000" pattern:"\\S" patternDescription:"text" doc:"The text. Keep markers such as [S1] to keep their sources."`
	Diagram  string `json:"diagram,omitempty" maxLength:"8000" doc:"A Mermaid diagram, or empty for none."`
	Note     string `json:"note,omitempty" maxLength:"1000" doc:"Why you change the section."`
}

// SavedEdit is the result of an edit: a version for a reviewer, a card
// for a member.
type SavedEdit struct {
	Version  int64 `json:"version,omitempty"`
	Proposal int64 `json:"proposal,omitempty"`
}

// InvalidError is an edit or a report that the rules refuse.
type InvalidError struct{ Code, Message string }

func (e *InvalidError) Error() string { return e.Message }

// diagramStarts are the first words of a Mermaid diagram.
var diagramStarts = []string{"flowchart", "graph", "sequenceDiagram", "classDiagram", "stateDiagram", "stateDiagram-v2", "erDiagram"}

func checkDiagram(d string) error {
	d = strings.TrimSpace(d)
	if d == "" {
		return nil
	}
	first := strings.Fields(d)[0]
	if !slices.Contains(diagramStarts, first) {
		return &InvalidError{Code: "diagram", Message: "Start the diagram with flowchart, graph, sequenceDiagram, classDiagram, stateDiagram or erDiagram."}
	}
	return nil
}

// target is a section of a page that a reader can correct.
type target struct {
	page    *DesignPage
	section DesignSection
	shown   DesignPageSection
	input   []byte
}

func (s *Service) target(ctx context.Context, vis visibility.Set, key, slug string) (*target, error) {
	p, err := s.Get(ctx, vis, key)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(p.Sections, func(x DesignPageSection) bool { return x.Slug == slug })
	if i < 0 || !p.Sections[i].Editable {
		return nil, ErrNotFound
	}
	sec, err := New(s.Pool).SectionBySlug(ctx, SectionBySlugParams{EntityID: p.entityID, Slug: slug})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t := &target{page: p, section: sec, shown: p.Sections[i], input: sec.InputHash}
	if p.Level == "system" {
		d, err := s.Entities.Get(ctx, vis, key)
		if err != nil {
			return nil, err
		}
		sys, err := s.members(ctx, vis, d)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, 0, len(sys.repoID))
		for _, id := range sys.repoID {
			ids = append(ids, id)
		}
		rc, err := s.repoConnection(ctx, vis, ids)
		if err != nil {
			return nil, err
		}
		in, err := s.systemInput(ctx, vis, d, sys, rc)
		if err != nil {
			return nil, err
		}
		t.input = in.overview.hash
	}
	if t.input == nil {
		t.input = []byte{}
	}
	return t, nil
}

// keptCites are the cites of the shown version whose markers the new text
// keeps.
func keptCites(shown DesignPageSection, md, diagram string) citeSet {
	set := citeSet{Markers: map[string]Cite{}, Diagram: []string{}}
	for _, c := range shown.Cites {
		if strings.Contains(md, "["+c.ID+"]") {
			set.Markers[c.ID] = c.Cite
		}
	}
	if strings.TrimSpace(diagram) == strings.TrimSpace(shown.Diagram) {
		for _, m := range shown.DiagramCites {
			for _, c := range shown.Cites {
				if c.ID == m {
					set.Markers[m] = c.Cite
					set.Diagram = append(set.Diagram, m)
				}
			}
		}
	}
	return set
}

// editPayload is the payload of a design_edit card.
type editPayload struct {
	EntityKey   string  `json:"entityKey"`
	SectionID   int64   `json:"sectionId"`
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Note        string  `json:"note,omitempty"`
	Current     textOf  `json:"current"`
	Proposed    textOf  `json:"proposed"`
	Cites       citeSet `json:"cites"`
	InputHash   string  `json:"inputHash"`
	Connections []int64 `json:"connections"`
}

// Edit corrects a section. A reviewer saves a pinned user version at once.
// A member edit becomes a design_edit card.
func (s *Service) Edit(ctx context.Context, vis visibility.Set, key, slug string, e SectionEdit, userID string, reviewer bool, via string) (*SavedEdit, error) {
	e.Markdown, e.Diagram = strings.TrimSpace(e.Markdown), strings.TrimSpace(e.Diagram)
	if err := checkDiagram(e.Diagram); err != nil {
		return nil, err
	}
	t, err := s.target(ctx, vis, key, slug)
	if err != nil {
		return nil, err
	}
	set := keptCites(t.shown, e.Markdown, e.Diagram)
	out := &SavedEdit{}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if reviewer {
			id, err := s.saveUser(ctx, tx, t.section.ID, userID, t.page.connections, e.Markdown, e.Diagram, set, t.input, e.Note)
			out.Version = id
			if err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "design.edit", Target: "entity:" + key,
				Details: map[string]any{"section": slug, "version": id}})
		}
		p := editPayload{EntityKey: key, SectionID: t.section.ID, Slug: slug, Title: t.section.Title, Note: e.Note,
			Current: textOf{t.shown.Markdown, t.shown.Diagram}, Proposed: textOf{e.Markdown, e.Diagram}, Cites: set, InputHash: hexOf(t.input),
			Connections: t.page.connections}
		payload, _ := json.Marshal(p)
		id, err := New(tx).InsertCard(ctx, InsertCardParams{Kind: "design_edit", Target: "entity:" + key, Payload: payload, ProposerID: &userID,
			Via: via, Connections: orEmpty(t.page.connections)})
		out.Proposal = id
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// saveUser writes a pinned user version.
func (s *Service) saveUser(ctx context.Context, tx pgx.Tx, sectionID int64, userID string, conns []int64, md, diagram string, set citeSet,
	input []byte, note string) (int64, error) {
	if set.Markers == nil {
		set.Markers = map[string]Cite{}
	}
	set.Diagram = orEmpty(set.Diagram)
	raw, _ := json.Marshal(set)
	lint := []pages.Failure{}
	if pr, err := settings.Read[pages.PageRules](ctx, tx, pages.Setting); err == nil {
		rules, rerr := settings.Read[Rules](ctx, tx, Setting)
		if rerr == nil {
			pr.SummaryWords = max(rules.SectionWords, 40)
		}
		terms, _ := s.terms(ctx, conns)
		lint = orEmpty(pages.Lint(md, pr, terms))
	}
	lr, _ := json.Marshal(lint)
	cs := slices.Clone(conns)
	slices.Sort(cs)
	cs = slices.Compact(cs)
	var uid *string
	if userID != "" {
		uid = &userID
	}
	return New(tx).InsertVersion(ctx, InsertVersionParams{SectionID: sectionID, Author: "user", UserID: uid, VisibilityKey: pages.VisibilityKey(cs),
		Connections: orEmpty(cs), Markdown: md, Diagram: diagram, Cites: raw, InputHash: input, Lint: lr, Note: note, Now: s.now()})
}

// problemPayload is the payload of a design_problem card.
type problemPayload struct {
	EntityKey   string  `json:"entityKey"`
	SectionID   int64   `json:"sectionId"`
	Slug        string  `json:"slug"`
	Title       string  `json:"title"`
	Level       string  `json:"level"`
	Reason      string  `json:"reason"`
	VersionID   int64   `json:"versionId"`
	Current     textOf  `json:"current"`
	Cites       citeSet `json:"cites"`
	InputHash   string  `json:"inputHash"`
	Connections []int64 `json:"connections"`
}

// Report records a problem with a section as a design_problem card.
func (s *Service) Report(ctx context.Context, vis visibility.Set, key, slug, reason, userID, via string) (int64, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, &InvalidError{Code: "reason", Message: "Give a reason."}
	}
	t, err := s.target(ctx, vis, key, slug)
	if err != nil {
		return 0, err
	}
	set := keptCites(t.shown, t.shown.Markdown, t.shown.Diagram)
	p := problemPayload{EntityKey: key, SectionID: t.section.ID, Slug: slug, Title: t.section.Title, Level: t.page.Level, Reason: reason,
		VersionID: t.shown.VersionID, Current: textOf{t.shown.Markdown, t.shown.Diagram}, Cites: set, InputHash: hexOf(t.input),
		Connections: t.page.connections}
	payload, _ := json.Marshal(p)
	return New(s.Pool).InsertCard(ctx, InsertCardParams{Kind: "design_problem", Target: "entity:" + key, Payload: payload, ProposerID: &userID,
		Via: via, Connections: orEmpty(t.page.connections)})
}

// ApplyEdit accepts a design_edit card: the proposed text, or the text of
// the reviewer, becomes a pinned user version.
func (s *Service) ApplyEdit(ctx context.Context, tx pgx.Tx, _ visibility.Set, raw []byte, proposer, reviewer string, ed resolve.Edits) error {
	var p editPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	md, diagram, author, note := p.Proposed.Markdown, p.Proposed.Diagram, proposer, p.Note
	if strings.TrimSpace(ed.Text) != "" {
		md, diagram, author = strings.TrimSpace(ed.Text), strings.TrimSpace(ed.Diagram), reviewer
		note = "A reviewer changed the proposed text."
	}
	if err := checkDiagram(diagram); err != nil {
		return &resolve.CardError{Code: "diagram", Message: err.Error()}
	}
	_, err := s.saveUser(ctx, tx, p.SectionID, author, p.Connections, md, diagram, keepMarkers(p.Cites, md, diagram), unhex(p.InputHash), note)
	return err
}

// ApplyProblem accepts a design_problem card. With corrected text, the
// text becomes a pinned user version. Without it, the section of a
// repository regenerates with the reason as the instruction.
func (s *Service) ApplyProblem(ctx context.Context, tx pgx.Tx, _ visibility.Set, raw []byte, _, reviewer string, ed resolve.Edits) error {
	var p problemPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if text := strings.TrimSpace(ed.Text); text != "" {
		diagram := strings.TrimSpace(ed.Diagram)
		if err := checkDiagram(diagram); err != nil {
			return &resolve.CardError{Code: "diagram", Message: err.Error()}
		}
		_, err := s.saveUser(ctx, tx, p.SectionID, reviewer, p.Connections, text, diagram, keepMarkers(p.Cites, text, diagram), unhex(p.InputHash),
			"Correction of a reported problem: "+p.Reason)
		return err
	}
	if p.Level != "repository" {
		return &resolve.CardError{Code: "text_required", Message: "Give the corrected text. A system page section regenerates when a reader opens it."}
	}
	_, err := s.River.InsertTx(ctx, tx, SectionArgs{SectionID: p.SectionID, Instruction: p.Reason}, nil)
	return err
}

// ApplyUpdate accepts a design_update card: the model draft, or the text
// of the reviewer, becomes the new pinned user version.
func (s *Service) ApplyUpdate(ctx context.Context, tx pgx.Tx, _ visibility.Set, raw []byte, _, reviewer string, ed resolve.Edits) error {
	var p updatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	q := New(tx)
	draft, err := q.GetVersion(ctx, p.Draft)
	if errors.Is(err, pgx.ErrNoRows) {
		return resolve.ErrNotFound
	}
	if err != nil {
		return err
	}
	pinned, err := q.GetVersion(ctx, p.Pinned)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var set citeSet
	_ = json.Unmarshal(draft.Cites, &set)
	md, diagram, note := draft.Markdown, draft.Diagram, "Accepted the model update after a code change."
	if strings.TrimSpace(ed.Text) != "" {
		md, diagram, note = strings.TrimSpace(ed.Text), strings.TrimSpace(ed.Diagram), "Edited the model update after a code change."
		if err := checkDiagram(diagram); err != nil {
			return &resolve.CardError{Code: "diagram", Message: err.Error()}
		}
	}
	conns := append(slices.Clone(draft.Connections), pinned.Connections...)
	_, err = s.saveUser(ctx, tx, p.SectionID, reviewer, conns, md, diagram, keepMarkers(set, md, diagram), draft.InputHash, note)
	return err
}

// keepMarkers keeps the cites whose markers a text or diagram still uses.
func keepMarkers(set citeSet, md, diagram string) citeSet {
	out := citeSet{Markers: map[string]Cite{}, Diagram: []string{}}
	for id, c := range set.Markers {
		if strings.Contains(md, "["+id+"]") {
			out.Markers[id] = c
		}
	}
	for _, m := range set.Diagram {
		if c, ok := set.Markers[m]; ok && diagram != "" {
			out.Markers[m] = c
			out.Diagram = append(out.Diagram, m)
		}
	}
	return out
}

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return []byte{}
	}
	return b
}

// SectionArgs regenerates one repository section with an instruction from
// a reviewer.
type SectionArgs struct {
	SectionID   int64  `json:"section_id"`
	Instruction string `json:"instruction"`
}

// Kind is the River job kind.
func (SectionArgs) Kind() string { return "design_section" }

// InsertOpts puts a regeneration on the design queue.
func (SectionArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: Queue} }

// SectionWorker works section regenerations.
type SectionWorker struct {
	river.WorkerDefaults[SectionArgs]
	S *Service
}

// Timeout bounds one regeneration.
func (w *SectionWorker) Timeout(*river.Job[SectionArgs]) time.Duration { return 10 * time.Minute }

// Work regenerates the section of the job.
func (w *SectionWorker) Work(ctx context.Context, job *river.Job[SectionArgs]) error {
	return w.S.Regenerate(ctx, job.Args.SectionID, job.Args.Instruction)
}

// Regenerate writes a new model version of a repository section.
func (s *Service) Regenerate(ctx context.Context, sectionID int64, instruction string) error {
	q := New(s.Pool)
	row, err := q.SectionByID(ctx, sectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	links, err := q.RepositoryLinks(ctx, []int64{row.EntityID})
	if err != nil || len(links) == 0 {
		return err
	}
	rules, err := settings.Read[Rules](ctx, s.Pool, Setting)
	if err != nil {
		return err
	}
	sec := DesignSection{ID: row.ID, EntityID: row.EntityID, Slug: row.Slug, Kind: row.Kind, Title: row.Title, Ordinal: row.Ordinal,
		ComponentPath: row.ComponentPath, InputHash: row.InputHash, RemovedAt: row.RemovedAt}
	_, err = s.buildSection(ctx, links[0].ConnectionID, sec, instruction, &budget{limit: rules.TokenBudget})
	if errors.Is(err, errBudget) {
		return nil
	}
	return err
}

// QueueRebuild queues a manual build of the repository of a design page.
func (s *Service) QueueRebuild(ctx context.Context, vis visibility.Set, key string) error {
	p, err := s.Get(ctx, vis, key)
	if err != nil {
		return err
	}
	if p.Level != "repository" {
		return ErrNotFound
	}
	comps, err := New(s.Pool).Components(ctx, ComponentsParams{EntityID: p.entityID, ConnectionID: p.connections[0]})
	if err != nil || len(comps) == 0 {
		return err
	}
	_, err = s.River.Insert(ctx, BuildArgs{ConnectionID: p.connections[0], Repo: comps[0].Repo, Trigger: "manual"}, nil)
	return err
}
