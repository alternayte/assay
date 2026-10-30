package entities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/visibility"
)

// Traversal limits (REQ-ENT-006).
const (
	MaxDepth = 3
	MaxEdges = 200
)

// ErrNotFound is a hidden or missing entity.
var ErrNotFound = errors.New("entities: the entity does not exist")

// Service reads and merges entities.
type Service struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
	// Rename gives a registry record a new name in tx. A merge with a
	// canonical name and a registry winner calls it.
	Rename func(ctx context.Context, tx pgx.Tx, vis visibility.Set, key, name, userID string) error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Summary is one entity of the Explore list.
type Summary struct {
	Key       string     `json:"key"`
	Type      string     `json:"type"`
	Name      string     `json:"name"`
	Status    string     `json:"status" enum:"active,proposed"`
	Domain    string     `json:"domain"`
	DomainID  *int64     `json:"domainId"`
	ChangedAt *time.Time `json:"changedAt"`
}

// Filter narrows Explore. Zero values do not filter.
type Filter struct {
	Type     string
	DomainID int64
	Status   string
	Query    string
	Limit    int
}

// Explore lists the visible entities, newest change first.
func (s *Service) Explore(ctx context.Context, vis visibility.Set, f Filter) ([]Summary, error) {
	out := []Summary{}
	if len(vis) == 0 {
		return out, nil
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := New(s.Pool).Explore(ctx, ExploreParams{Visible: vis, Type: f.Type, Domain: f.DomainID, Status: f.Status,
		Query: NormalizeAlias(f.Query), Lim: int32(f.Limit)}) //nolint:gosec // The limit is at most 500.
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, Summary{Key: r.Key, Type: r.Type, Name: r.Name, Status: r.Status, Domain: r.DomainName, DomainID: r.DomainID,
			ChangedAt: &r.ChangedAt})
	}
	return out, nil
}

// EntitySource is an item that gives evidence about an entity.
type EntitySource struct {
	ItemID         int64      `json:"itemId"`
	Title          string     `json:"title"`
	URL            string     `json:"url"`
	Kind           string     `json:"kind"`
	SourceType     string     `json:"sourceType"`
	ConnectionName string     `json:"connectionName"`
	UpdatedAt      *time.Time `json:"updatedAt"`
}

// Detail is an entity with its visible aliases, claims and sources.
type Detail struct {
	Key      string         `json:"key"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Status   string         `json:"status" enum:"active,proposed"`
	Domain   string         `json:"domain"`
	DomainID *int64         `json:"domainId"`
	Aliases  []string       `json:"aliases"`
	Claims   []claims.Claim `json:"claims" doc:"Claims with the entity as subject, with their status for you."`
	Incoming []claims.Claim `json:"incoming" doc:"Claims with the entity as object."`
	Timeline []claims.Claim `json:"timeline" doc:"Dated values of time-scoped claims, newest first."`
	Sources  []EntitySource `json:"sources"`
	Merges   []MergeView    `json:"merges" doc:"Active merges into this entity, with the losers that you can see. Split undoes one."`
	// Newest is the newest update time of a source.
	Newest *time.Time `json:"newest"`

	ID      int64   `json:"-"`
	Members []int64 `json:"-"`
}

// MergeView is an active merge into an entity.
type MergeView struct {
	ID            int64     `json:"id"`
	CanonicalName string    `json:"canonicalName"`
	Auto          bool      `json:"auto"`
	At            time.Time `json:"at"`
	Losers        []Ref     `json:"losers"`
}

// Ref names an entity.
type Ref struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Get returns a visible entity. A merged key gives its winner. A hidden
// entity and a missing key give ErrNotFound.
func (s *Service) Get(ctx context.Context, vis visibility.Set, key string) (*Detail, error) {
	if len(vis) == 0 {
		return nil, ErrNotFound
	}
	q := New(s.Pool)
	e, err := q.VisibleEntityByKey(ctx, VisibleEntityByKeyParams{Key: key, Visible: vis})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	members, err := q.Members(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	d := &Detail{ID: e.ID, Members: members, Key: e.Key, Type: e.Type, Name: e.Name, Status: e.Status, Domain: e.DomainName,
		DomainID: e.DomainID, Aliases: []string{}, Claims: []claims.Claim{}, Incoming: []claims.Claim{}, Timeline: []claims.Claim{},
		Sources: []EntitySource{}, Merges: []MergeView{}}
	ms, err := q.ActiveMerges(ctx, ActiveMergesParams{WinnerID: e.ID, Visible: vis})
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		v := MergeView{ID: m.ID, CanonicalName: m.CanonicalName, Auto: m.Auto, At: m.CreatedAt, Losers: []Ref{}}
		for i, k := range m.Keys {
			v.Losers = append(v.Losers, Ref{Key: k, Name: m.Names[i]})
		}
		d.Merges = append(d.Merges, v)
	}
	aliases, err := q.VisibleAliases(ctx, VisibleAliasesParams{Members: members, Visible: vis})
	if err != nil {
		return nil, err
	}
	for _, a := range aliases {
		if a.Alias != d.Name {
			d.Aliases = append(d.Aliases, a.Alias)
		}
	}
	cs, err := claims.Load(ctx, s.Pool, vis, claims.Query{Subjects: members, Objects: members})
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		switch {
		case !slices.Contains(members, c.SubjectID):
			d.Incoming = append(d.Incoming, c)
		case claims.Registry[c.Predicate].TimeScoped:
			d.Timeline = append(d.Timeline, c)
			if c.Current {
				d.Claims = append(d.Claims, c)
			}
		default:
			d.Claims = append(d.Claims, c)
		}
	}
	slices.SortStableFunc(d.Timeline, func(a, b claims.Claim) int {
		if a.Predicate != b.Predicate {
			return strings.Compare(a.Predicate, b.Predicate)
		}
		return timeDesc(a.ValidFrom, b.ValidFrom)
	})
	srcs, err := q.VisibleSources(ctx, VisibleSourcesParams{Members: members, Visible: vis})
	if err != nil {
		return nil, err
	}
	for _, r := range srcs {
		d.Sources = append(d.Sources, EntitySource{ItemID: r.ID, Title: r.Title, URL: r.Url, Kind: r.Kind, SourceType: r.SourceType,
			ConnectionName: r.ConnectionName, UpdatedAt: r.SourceUpdatedAt})
		if r.SourceUpdatedAt != nil && (d.Newest == nil || r.SourceUpdatedAt.After(*d.Newest)) {
			d.Newest = r.SourceUpdatedAt
		}
	}
	slices.SortStableFunc(d.Sources, func(a, b EntitySource) int { return timeDesc(a.UpdatedAt, b.UpdatedAt) })
	return d, nil
}

func timeDesc(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return b.Compare(*a)
}

// Node is an entity of a trace.
type Node struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// Edge is one step of a trace: a claim between two entities.
type Edge struct {
	ClaimID   int64  `json:"claimId"`
	Predicate string `json:"predicate"`
	From      string `json:"from" doc:"The subject key."`
	To        string `json:"to" doc:"The object key."`
	Depth     int    `json:"depth"`
}

// Trace is the result of a traversal.
type Trace struct {
	Root      string `json:"root"`
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
	Truncated bool   `json:"truncated"`
}

// Directions of a trace.
const (
	Out  = "out"
	In   = "in"
	Both = "both"
)

// Trace follows visible entity-valued claims from an entity, breadth first,
// to depth and at most 200 edges. An edge needs visible support, and both
// its entities need a visible source.
func (s *Service) Trace(ctx context.Context, vis visibility.Set, key string, predicates []string, depth int, direction string) (*Trace, error) {
	d, err := s.Get(ctx, vis, key)
	if err != nil {
		return nil, err
	}
	q := New(s.Pool)
	t := &Trace{Root: d.Key, Nodes: []Node{}, Edges: []Edge{}}
	seenNode := map[int64]bool{d.ID: true}
	seenEdge := map[int64]bool{}
	frontier := []int64{d.ID}
	ids := []int64{d.ID}
	type step struct {
		Edge
		from, to int64
	}
	var steps []step
	for level := 1; level <= depth && len(frontier) > 0 && !t.Truncated; level++ {
		members := []int64{}
		for _, id := range frontier {
			ms, err := q.Members(ctx, id)
			if err != nil {
				return nil, err
			}
			members = append(members, ms...)
		}
		rows, err := q.Edges(ctx, EdgesParams{Ids: members, Predicates: orEmptyStrings(predicates), Visible: vis})
		if err != nil {
			return nil, err
		}
		var next []int64
		for _, r := range rows {
			if seenEdge[r.ID] {
				continue
			}
			var far int64
			switch {
			case slices.Contains(frontier, r.SubjectID) && direction != In:
				far = r.ObjectID
			case slices.Contains(frontier, r.ObjectID) && direction != Out:
				far = r.SubjectID
			default:
				continue
			}
			if len(steps) == MaxEdges {
				t.Truncated = true
				break
			}
			seenEdge[r.ID] = true
			steps = append(steps, step{Edge: Edge{ClaimID: r.ID, Predicate: r.Predicate, Depth: level}, from: r.SubjectID, to: r.ObjectID})
			if !seenNode[far] {
				seenNode[far] = true
				next = append(next, far)
				ids = append(ids, far)
			}
		}
		frontier = next
	}
	nodes, err := q.Nodes(ctx, NodesParams{Ids: ids, Visible: vis})
	if err != nil {
		return nil, err
	}
	keys := map[int64]string{}
	for _, n := range nodes {
		keys[n.ID] = n.Key
		t.Nodes = append(t.Nodes, Node{Key: n.Key, Type: n.Type, Name: n.Name})
	}
	for _, st := range steps {
		st.From, st.To = keys[st.from], keys[st.to]
		t.Edges = append(t.Edges, st.Edge)
	}
	return t, nil
}

// MergeRequest merges entities into one.
type MergeRequest struct {
	Keys      []string
	Canonical string
	By        string
	Auto      bool
}

// ProposeMerge records a merge proposal of a member as a merge card
// (REQ-REV-012). All entities must be visible to the member.
func (s *Service) ProposeMerge(ctx context.Context, vis visibility.Set, keys []string, by string) (int64, error) {
	q := New(s.Pool)
	keys = slices.Compact(slices.Sorted(slices.Values(keys)))
	if len(keys) < 2 {
		return 0, errors.New("entities: a merge needs two entities or more")
	}
	for _, k := range keys {
		if _, err := q.VisibleEntityByKey(ctx, VisibleEntityByKeyParams{Key: k, Visible: vis}); errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		} else if err != nil {
			return 0, err
		}
	}
	conns, err := q.SourceConnections(ctx, SourceConnectionsParams{Keys: keys, Visible: vis})
	if err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]any{"keys": keys})
	var id int64
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if id, err = New(tx).InsertMergeProposal(ctx, InsertMergeProposalParams{Target: "entity:" + keys[0], Payload: payload, ProposerID: &by,
			Connections: conns}); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "entities.merge_proposal", Target: "entity:" + keys[0],
			Details: map[string]any{"proposal": id, "keys": keys}, Connections: conns})
	})
	return id, err
}

// Merge points the losing entities to the winner (REQ-ENT-004). The winner
// is the registry entity when one exists, else the oldest (REQ-RES-010).
// All entities must be visible to the reviewer.
func (s *Service) Merge(ctx context.Context, vis visibility.Set, r MergeRequest) (mergeID int64, winnerKey string, err error) {
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		mergeID, winnerKey, err = s.MergeTx(ctx, tx, vis, r)
		return err
	})
	return mergeID, winnerKey, err
}

// MergeTx merges in the transaction tx.
func (s *Service) MergeTx(ctx context.Context, tx pgx.Tx, vis visibility.Set, r MergeRequest) (int64, string, error) {
	if len(r.Keys) < 2 {
		return 0, "", errors.New("entities: a merge needs two entities or more")
	}
	var ids []int64
	for _, k := range r.Keys {
		d, err := s.Get(ctx, vis, k)
		if err != nil {
			return 0, "", err
		}
		if !slices.Contains(ids, d.ID) {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) < 2 {
		return 0, "", errors.New("entities: the entities are already one")
	}
	return s.mergeTx(ctx, tx, vis, ids, r)
}

func (s *Service) mergeTx(ctx context.Context, tx pgx.Tx, vis visibility.Set, ids []int64, r MergeRequest) (int64, string, error) {
	q := New(tx)
	reg, err := q.HasRegistrySource(ctx, HasRegistrySourceParams{Ids: ids, Visible: vis})
	if err != nil {
		return 0, "", err
	}
	rows, err := q.GetEntitiesByID(ctx, ids)
	if err != nil {
		return 0, "", err
	}
	slices.SortFunc(rows, func(a, b GetEntitiesByIDRow) int {
		ar, br := slices.Contains(reg, a.ID), slices.Contains(reg, b.ID)
		if ar != br {
			return boolFirst(ar)
		}
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	winner := rows[0]
	losers := []int64{}
	for _, e := range rows[1:] {
		losers = append(losers, e.ID)
	}
	canonical := strings.TrimSpace(r.Canonical)
	var by *string
	if r.By != "" {
		by = &r.By
	}
	mergeID, err := q.InsertMerge(ctx, InsertMergeParams{WinnerID: winner.ID, LoserIds: losers, CanonicalName: canonical,
		By: by, Auto: r.Auto})
	if err != nil {
		return 0, "", err
	}
	if err := q.RepointMerged(ctx, RepointMergedParams{Winner: &winner.ID, Ids: losers}); err != nil {
		return 0, "", err
	}
	if err := q.MarkMerged(ctx, MarkMergedParams{Winner: &winner.ID, Ids: losers}); err != nil {
		return 0, "", err
	}
	shared := false
	if canonical != "" && slices.Contains(reg, winner.ID) && s.Rename != nil {
		everyone, err := q.EveryoneConnections(ctx)
		if err != nil {
			return 0, "", err
		}
		group, err := q.Members(ctx, winner.ID)
		if err != nil {
			return 0, "", err
		}
		if shared, err = q.AllVisible(ctx, AllVisibleParams{Ids: group, Visible: append([]int64{}, everyone...)}); err != nil {
			return 0, "", err
		}
	}
	if shared {
		// The registry name comes first (REQ-ENT-007), so the canonical name
		// renames the registry record. Everyone reads the registry, so this
		// happens only when Everyone sees every merged entity: else the name
		// would show to readers who do not see every entity (SI-16).
		if err := s.Rename(ctx, tx, vis, winner.Key, canonical, r.By); err != nil {
			return 0, "", err
		}
	}
	// The caller gets the key that it sees: the winner can be hidden from it.
	key, err := q.VisibleKey(ctx, VisibleKeyParams{ID: winner.ID, Visible: vis})
	if err != nil {
		return 0, "", err
	}
	// The entry names only what the merger sees.
	return mergeID, key, audit.Write(ctx, tx, audit.Entry{Action: "entities.merge", Target: "entity:" + key,
		Details:     map[string]any{"merge": mergeID, "entities": r.Keys, "canonicalName": canonical, "auto": r.Auto},
		Connections: append([]int64{}, vis...)})
}

func boolFirst(b bool) int {
	if b {
		return -1
	}
	return 1
}

// ErrMergeUndone means that the merge is already split.
var ErrMergeUndone = errors.New("entities: the merge is already split")

// ErrMergeChanged means that a later merge moved the entities of the merge.
var ErrMergeChanged = errors.New("entities: a later merge includes the entities")

// Split undoes a merge for a reader who sees every entity of it: the losers
// become active again. A merge with a hidden entity is not found (SI-02).
func (s *Service) Split(ctx context.Context, vis visibility.Set, mergeID int64) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		ok, err := New(tx).MergeVisible(ctx, MergeVisibleParams{ID: mergeID, Visible: vis})
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		return s.SplitTx(ctx, tx, mergeID)
	})
}

// SplitTx splits a merge in the transaction tx.
func (s *Service) SplitTx(ctx context.Context, tx pgx.Tx, mergeID int64) error {
	{
		q := New(tx)
		m, err := q.GetMerge(ctx, mergeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		n, err := q.UndoMerge(ctx, UndoMergeParams{ID: mergeID, Now: ptr(s.now())})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrMergeUndone
		}
		n, err = q.Unmerge(ctx, UnmergeParams{Ids: m.LoserIds, Winner: &m.WinnerID})
		if err != nil {
			return err
		}
		if int(n) != len(m.LoserIds) {
			return ErrMergeChanged
		}
		// A merge moves the entities merged into a loser to the winner. The
		// entities whose newest active merge had another winner go back to
		// the top of that winner.
		owned, err := q.MergedOwners(ctx, &m.WinnerID)
		if err != nil {
			return err
		}
		for _, o := range owned {
			if o.Owner == 0 || o.Owner == m.WinnerID {
				continue
			}
			top, err := q.TopWinner(ctx, o.Owner)
			if err != nil {
				return err
			}
			if top != o.ID {
				if err := q.SetMergedInto(ctx, SetMergedIntoParams{ID: o.ID, Winner: &top}); err != nil {
					return err
				}
			}
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "entities.split", Target: fmt.Sprintf("merge:%d", mergeID),
			Details: map[string]any{"winner": m.WinnerID, "losers": m.LoserIds}})
	}
}

func ptr[T any](v T) *T { return &v }

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
