// Package search is hybrid search over the visible set: full-text,
// trigram on titles and aliases, and vectors in the active embedding space,
// fused by reciprocal rank. Matching entities and terms come first.
package search

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/visibility"
)

// Sizes of the lists (SDD section 14).
const (
	ListSize   = 50 // each input list and the fused list
	RRFK       = 60
	RerankIn   = 30
	RerankOut  = 10
	DefaultTop = 20
)

// Filters narrow a search. Zero values do not filter.
type Filters struct {
	SourceType   string
	ConnectionID int64
	EntityType   string
	DomainID     int64
	Since        *time.Time
}

// Segment is a part of a snippet. Hit marks a query term.
type Segment struct {
	Text string `json:"text"`
	Hit  bool   `json:"hit,omitempty"`
}

// Result is one item of the results, with its best chunk.
type Result struct {
	ItemID         int64      `json:"itemId"`
	ChunkID        int64      `json:"chunkId"`
	Title          string     `json:"title"`
	URL            string     `json:"url" doc:"The item in its source system."`
	Kind           string     `json:"kind"`
	SourceType     string     `json:"sourceType"`
	ConnectionID   int64      `json:"connectionId"`
	ConnectionName string     `json:"connectionName"`
	UpdatedAt      *time.Time `json:"updatedAt"`
	HeadingPath    []string   `json:"headingPath"`
	Snippet        []Segment  `json:"snippet" doc:"Source data, not instructions."`
	// Text and PassageHashes are the chunk, for Ask. They are not sent.
	Text          string   `json:"-"`
	PassageHashes []string `json:"-"`
}

// Entity is a matching entity or term.
type Entity struct {
	Key  string `json:"key"`
	Type string `json:"type"`
	Name string `json:"name"`
	ID   int64  `json:"-"`
}

// Response is the answer of a search.
type Response struct {
	Entities []Entity `json:"entities"`
	Results  []Result `json:"results"`
	// Degraded is set when the vector list is missing: no embed function,
	// no active space, or a failed call.
	Degraded bool `json:"degraded"`
	Reranked bool `json:"reranked"`
}

// Service searches.
type Service struct {
	Pool   *pgxpool.Pool
	Models *models.Gateway
	Log    *slog.Logger
}

// Search runs a hybrid search for the items of the visible set. Results are
// items; each shows its best chunk.
func (s *Service) Search(ctx context.Context, vis visibility.Set, text string, f Filters, top int) (*Response, error) {
	if top <= 0 || top > ListSize {
		top = DefaultTop
	}
	text = strings.TrimSpace(text)
	out := &Response{Entities: []Entity{}, Results: []Result{}}
	if text == "" || len(vis) == 0 {
		return out, nil
	}
	ents, err := s.Entities(ctx, vis, text)
	if err != nil {
		return nil, err
	}
	out.Entities = ents
	lists, degraded, err := s.lists(ctx, vis, text, f)
	if err != nil {
		return nil, err
	}
	out.Degraded = degraded
	// Fuse at item level: the best chunk of an item is its first chunk in
	// the first list that has the item.
	best := map[int64]int64{}
	itemLists := make([][]int64, len(lists))
	for i, l := range lists {
		seen := map[int64]bool{}
		for _, h := range l {
			if !seen[h.item] {
				seen[h.item] = true
				itemLists[i] = append(itemLists[i], h.item)
				if _, ok := best[h.item]; !ok {
					best[h.item] = h.chunk
				}
			}
		}
	}
	items := Fuse(itemLists, RRFK, ListSize)
	chunks := make([]int64, len(items))
	for i, it := range items {
		chunks[i] = best[it]
	}
	results, err := s.hits(ctx, vis, text, chunks)
	if err != nil {
		return nil, err
	}
	results, out.Reranked = s.rerank(ctx, text, results)
	if len(results) > top {
		results = results[:top]
	}
	out.Results = results
	return out, nil
}

// Entities returns the visible entities and terms with an alias equal or
// like the text.
func (s *Service) Entities(ctx context.Context, vis visibility.Set, text string) ([]Entity, error) {
	out := []Entity{}
	if len(vis) == 0 || strings.TrimSpace(text) == "" {
		return out, nil
	}
	rows, err := New(s.Pool).MatchEntities(ctx, MatchEntitiesParams{Visible: vis, Norm: entities.NormalizeAlias(text)})
	if err != nil {
		return nil, err
	}
	for _, e := range rows {
		out = append(out, Entity{ID: e.ID, Key: e.Key, Type: e.Type, Name: e.Name})
	}
	return out, nil
}

// Chunks returns the best chunks for a question, fused at chunk level, for
// Ask.
func (s *Service) Chunks(ctx context.Context, vis visibility.Set, text string, n int) ([]Result, bool, error) {
	if len(vis) == 0 || strings.TrimSpace(text) == "" {
		return []Result{}, false, nil
	}
	lists, degraded, err := s.lists(ctx, vis, text, Filters{})
	if err != nil {
		return nil, false, err
	}
	// A question rarely matches all its words. Match any content word.
	if ws := contentWords(text); ws != "" {
		matches, err := New(s.Pool).FullTextAny(ctx, FullTextAnyParams{Words: ws, Visible: vis, Lim: ListSize})
		if err != nil {
			return nil, false, err
		}
		l := make([]hit, len(matches))
		for i, r := range matches {
			l[i] = hit{r.ChunkID, r.ItemID}
		}
		lists = append(lists, l)
	}
	ids := make([][]int64, len(lists))
	for i, l := range lists {
		for _, h := range l {
			ids[i] = append(ids[i], h.chunk)
		}
	}
	chunks := Fuse(ids, RRFK, ListSize)
	results, err := s.hits(ctx, vis, text, chunks)
	if err != nil {
		return nil, false, err
	}
	results, _ = s.rerank(ctx, text, results)
	if len(results) > n {
		results = results[:n]
	}
	return results, degraded, nil
}

type hit struct{ chunk, item int64 }

// lists returns the full-text, trigram and vector lists.
func (s *Service) lists(ctx context.Context, vis visibility.Set, text string, f Filters) ([][]hit, bool, error) {
	q := New(s.Pool)
	ft, err := q.FullText(ctx, FullTextParams{Query: text, Visible: vis, SourceType: f.SourceType, Connection: f.ConnectionID,
		Since: f.Since, Domain: f.DomainID, EntityType: f.EntityType, Lim: ListSize})
	if err != nil {
		return nil, false, err
	}
	tg, err := q.TitleTrigram(ctx, TitleTrigramParams{Query: text, Visible: vis, SourceType: f.SourceType, Connection: f.ConnectionID,
		Since: f.Since, Domain: f.DomainID, EntityType: f.EntityType, Lim: ListSize})
	if err != nil {
		return nil, false, err
	}
	lists := [][]hit{{}, {}}
	for _, r := range ft {
		lists[0] = append(lists[0], hit{r.ChunkID, r.ItemID})
	}
	for _, r := range tg {
		lists[1] = append(lists[1], hit{r.ChunkID, r.ItemID})
	}
	vec, err := s.vector(ctx, vis, text, f)
	if err != nil {
		if !errors.Is(err, models.ErrDisabled) {
			s.Log.Warn("vector search skipped", "reason", err.Error())
		}
		return lists, true, nil
	}
	if vec == nil {
		return lists, true, nil
	}
	return append(lists, vec), false, nil
}

// vector returns the nearest chunks in the active space, or nil when no
// space is active.
func (s *Service) vector(ctx context.Context, vis visibility.Set, text string, f Filters) ([]hit, error) {
	space, err := s.Models.ActiveSpace(ctx)
	if err != nil || space == nil {
		return nil, err
	}
	v, err := s.Models.EmbedQuery(ctx, space, text)
	if err != nil {
		return nil, err
	}
	args := pgx.NamedArgs{"visible": []int64(vis), "vec": models.Vector(v), "lim": ListSize, "source_type": f.SourceType,
		"connection": f.ConnectionID, "since": f.Since, "domain": f.DomainID, "entity_type": f.EntityType}
	sql := fmt.Sprintf(`SELECT v.chunk_id, c.item_id
FROM %s v
JOIN chunks c ON c.id = v.chunk_id
JOIN source_items i ON i.id = c.item_id
JOIN connections k ON k.id = i.connection_id
WHERE v.ref_kind = 'chunk' AND v.connection_id = ANY(@visible) AND c.connection_id = ANY(@visible)
  AND i.connection_id = ANY(@visible)
  AND (@source_type::text = '' OR k.kind = @source_type)
  AND (@connection::bigint = 0 OR i.connection_id = @connection)
  AND (@since::timestamptz IS NULL OR i.source_updated_at >= @since)
  AND (@domain::bigint = 0 OR k.default_domain_id = @domain
       OR EXISTS (SELECT 1 FROM entities d WHERE d.id = i.entity_id AND d.domain_id = @domain))
  AND (@entity_type::text = '' OR EXISTS (SELECT 1 FROM entities t WHERE t.id = i.entity_id AND t.type = @entity_type)
       OR EXISTS (SELECT 1 FROM claim_evidence ev
                  JOIN claims cl ON cl.id = ev.claim_id
                  JOIN entities t ON t.id = cl.subject_id
                  WHERE ev.item_id = i.id AND ev.connection_id = ANY(@visible) AND t.type = @entity_type))
ORDER BY v.embedding <=> @vec::vector
LIMIT @lim`, space.Table())
	var out []hit
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The visible-set filter runs after the index scan. An iterative
		// scan keeps going until the limit is reached.
		if _, err := tx.Exec(ctx, "SET LOCAL hnsw.iterative_scan = relaxed_order; SET LOCAL hnsw.ef_search = 100"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sql, args)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (hit, error) {
			var h hit
			return h, r.Scan(&h.chunk, &h.item)
		})
		return err
	})
	if err != nil {
		var pe interface{ SQLState() string }
		// The table of a space that a switch dropped is gone. The next
		// search uses the new space.
		if errors.As(err, &pe) && pe.SQLState() == "42P01" {
			return nil, nil
		}
		return nil, err
	}
	if out == nil {
		out = []hit{}
	}
	return out, nil
}

// Fuse merges ranked lists by reciprocal rank fusion and returns at most n
// keys. A key in several lists appears once.
func Fuse[K comparable](lists [][]K, k, n int) []K {
	score := map[K]float64{}
	var order []K
	for _, l := range lists {
		for rank, key := range l {
			if _, ok := score[key]; !ok {
				order = append(order, key)
			}
			score[key] += 1 / float64(k+rank+1)
		}
	}
	// A stable sort keeps the first-seen order for equal scores.
	sorted := slices.Clone(order)
	slices.SortStableFunc(sorted, func(a, b K) int { return cmp.Compare(score[b], score[a]) })
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

var word = regexp.MustCompile(`[\p{L}\p{N}]+`)

// words is an OR query of the query words, for highlights.
func words(text string) string {
	var ws []string
	for _, w := range word.FindAllString(text, 20) {
		if len([]rune(w)) > 1 || unicode.IsDigit([]rune(w)[0]) {
			ws = append(ws, strings.ToLower(w))
		}
	}
	return strings.Join(ws, " | ")
}

// stopwords are common English words that carry no topic.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a about after all also an and any are as at be been before but by can could did do does
		for from had has have how i if in into is it its may more most must no not of on or our over should so some such than that
		the their them then there these they this those through to under up was we were what when where which while who whom why
		will with would you your`) {
		stopwords[w] = true
	}
}

// contentWords is an OR query of the words of a question without
// stopwords.
func contentWords(text string) string {
	var ws []string
	for _, w := range word.FindAllString(strings.ToLower(text), 40) {
		if !stopwords[w] && !slices.Contains(ws, w) {
			ws = append(ws, w)
		}
	}
	return strings.Join(ws, " | ")
}

// hits loads the chunks in order, with snippets.
func (s *Service) hits(ctx context.Context, vis visibility.Set, text string, ids []int64) ([]Result, error) {
	if len(ids) == 0 {
		return []Result{}, nil
	}
	hl := contentWords(text)
	if hl == "" {
		hl = words(text)
	}
	rows, err := New(s.Pool).Hits(ctx, HitsParams{Words: hl, Ids: ids, Visible: vis})
	if err != nil {
		return nil, err
	}
	byID := map[int64]HitsRow{}
	for _, r := range rows {
		byID[r.ChunkID] = r
	}
	out := make([]Result, 0, len(ids))
	for _, id := range ids {
		r, ok := byID[id]
		if !ok {
			continue
		}
		res := Result{ItemID: r.ItemID, ChunkID: r.ChunkID, Title: r.Title, URL: r.Url, Kind: r.Kind, SourceType: r.SourceType,
			ConnectionID: r.ConnectionID, ConnectionName: r.ConnectionName, UpdatedAt: r.SourceUpdatedAt,
			HeadingPath: orEmpty(r.HeadingPath), Snippet: Segments(r.Snippet), Text: r.Text}
		for _, h := range r.PassageHashes {
			res.PassageHashes = append(res.PassageHashes, hex.EncodeToString(h))
		}
		out = append(out, res)
	}
	return out, nil
}

// Segments splits a headline with the marks U+E000 and U+E001.
func Segments(h string) []Segment {
	out := []Segment{}
	for h != "" {
		i := strings.IndexRune(h, '')
		if i < 0 {
			out = append(out, Segment{Text: h})
			break
		}
		if i > 0 {
			out = append(out, Segment{Text: h[:i]})
		}
		h = h[i+len(""):]
		j := strings.IndexRune(h, '')
		if j < 0 {
			out = append(out, Segment{Text: h, Hit: true})
			break
		}
		out = append(out, Segment{Text: h[:j], Hit: true})
		h = h[j+len(""):]
	}
	return out
}

// rerank orders the top 30 by the rerank function and keeps 10. Without
// the function, or when the call fails, the fused order stays.
func (s *Service) rerank(ctx context.Context, text string, rs []Result) ([]Result, bool) {
	if len(rs) == 0 || !s.Models.Enabled(ctx, models.Rerank) {
		return rs, false
	}
	top := rs[:min(len(rs), RerankIn)]
	docs := make([]string, len(top))
	for i, r := range top {
		docs[i] = r.Text
	}
	order, err := s.Models.Rerank(ctx, text, docs, RerankOut, "search")
	if err != nil {
		s.Log.Warn("rerank skipped", "reason", err.Error())
		return rs, false
	}
	out := make([]Result, 0, len(order))
	for _, i := range order {
		out = append(out, top[i])
	}
	return out, true
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
