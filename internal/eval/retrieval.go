package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Expected is one item that search should rank for a question. Source
// matches the title or the URL of an item, as Sources does. Grade 3 is the
// item that answers the question, 2 an item that helps, 1 a related item.
type Expected struct {
	Source string `yaml:"source"`
	Grade  int    `yaml:"grade"`
}

// RetrievalQueryArgs are the search_context arguments of each query. The
// eval scores the ranking, so the output limit must not drop results.
var RetrievalQueryArgs = map[string]any{"limit": 20, "kinds": []string{"results"}, "max_tokens": 50000}

// Ranked is one item of a search ranking.
type Ranked struct {
	ItemID int64  `json:"itemId"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// Ranking is the search results of one question in rank order, one per
// chunk.
type Ranking struct {
	Question Question
	Results  []Ranked
	Err      string
}

// RetrievalRow is the score of one question. Rank is the first rank of an
// item of grade 2 or more, or 0 when none is in the ranking.
type RetrievalRow struct {
	ID       string
	Rank     int
	RR       float64
	Hit1     bool
	Hit3     bool
	Recall5  float64
	Recall20 float64
	NDCG10   float64
	Err      string
}

// RetrievalReport is the mean score of a retrieval run.
type RetrievalReport struct {
	Pipeline Pipeline
	Queries  int
	MRR      float64
	Hit1     float64
	Hit3     float64
	Recall5  float64
	Recall20 float64
	NDCG10   float64
	Rows     []RetrievalRow
}

// Pipeline names the search pipeline of the instance. A score is comparable
// only with the same pipeline.
type Pipeline struct {
	EmbedModel  string
	Vector      string // on, off or unknown
	RerankModel string
	Rerank      string // on, off or unknown
	Note        string
}

// rankItems converts chunk results to an item ranking: the first time an
// item shows is its rank.
func rankItems(rs []Ranked) []Ranked {
	seen := map[int64]bool{}
	out := []Ranked{}
	for _, r := range rs {
		if seen[r.ItemID] {
			continue
		}
		seen[r.ItemID] = true
		out = append(out, r)
	}
	return out
}

// ScoreRetrieval scores rankings. The first time an item shows is its rank,
// so several chunks of one item count once. Hit, MRR and recall count expected items
// of grade 2 or more. Recall counts the expected sources that an item in the
// first k matches. nDCG@10 uses the grades as gains with a log2(rank + 1)
// discount; each expected source gives its gain once, at its first match.
func ScoreRetrieval(p Pipeline, rs []Ranking) RetrievalReport {
	rep := RetrievalReport{Pipeline: p, Queries: len(rs)}
	for _, r := range rs {
		row := RetrievalRow{ID: r.Question.ID, Err: r.Err}
		items := rankItems(r.Results)
		if len(items) > 20 {
			items = items[:20]
		}
		// first[i] is the first rank that matches expected source i.
		first := make([]int, len(r.Question.Expected))
		for i, e := range r.Question.Expected {
			for k, it := range items {
				if matches(Cited{Title: it.Title, URL: it.URL}, e.Source) {
					first[i] = k + 1
					break
				}
			}
		}
		relevant := 0
		found5, found20 := 0, 0
		for i, e := range r.Question.Expected {
			if e.Grade < 2 {
				continue
			}
			relevant++
			if f := first[i]; f > 0 {
				if row.Rank == 0 || f < row.Rank {
					row.Rank = f
				}
				if f <= 5 {
					found5++
				}
				found20++
			}
		}
		if row.Rank > 0 {
			row.RR = 1 / float64(row.Rank)
		}
		row.Hit1, row.Hit3 = row.Rank == 1, row.Rank > 0 && row.Rank <= 3
		row.Recall5, row.Recall20 = ratio(found5, relevant), ratio(found20, relevant)
		row.NDCG10 = ndcg(r.Question.Expected, first, 10)
		rep.Rows = append(rep.Rows, row)
		rep.MRR += row.RR
		rep.Hit1 += b2f(row.Hit1)
		rep.Hit3 += b2f(row.Hit3)
		rep.Recall5 += row.Recall5
		rep.Recall20 += row.Recall20
		rep.NDCG10 += row.NDCG10
	}
	if n := float64(len(rs)); n > 0 {
		rep.MRR /= n
		rep.Hit1 /= n
		rep.Hit3 /= n
		rep.Recall5 /= n
		rep.Recall20 /= n
		rep.NDCG10 /= n
	}
	return rep
}

// ndcg is the nDCG at k of expected sources whose first match ranks are
// first (0 for no match).
func ndcg(es []Expected, first []int, k int) float64 {
	gain := make([]float64, k)
	grades := []int{}
	for i, e := range es {
		grades = append(grades, e.Grade)
		if f := first[i]; f > 0 && f <= k {
			gain[f-1] = math.Max(gain[f-1], float64(e.Grade))
		}
	}
	var dcg, idcg float64
	for i, g := range gain {
		dcg += g / math.Log2(float64(i+2))
	}
	slices.SortFunc(grades, func(a, b int) int { return b - a })
	for i, g := range grades {
		if i >= k {
			break
		}
		idcg += float64(g) / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Markdown renders the report: the pipeline, one summary table, then one row
// per query with its first expected rank.
func (r RetrievalReport) Markdown(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	p := r.Pipeline
	fmt.Fprintf(&b, "Embedding model: %s. Vector search: %s. Rerank: %s", orUnknown(p.EmbedModel), p.Vector, p.Rerank)
	if p.RerankModel != "" {
		fmt.Fprintf(&b, " (%s)", p.RerankModel)
	}
	b.WriteString(".")
	if p.Note != "" {
		fmt.Fprintf(&b, " %s", p.Note)
	}
	b.WriteString("\n\n| Measure | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Queries | %d |\n", r.Queries)
	fmt.Fprintf(&b, "| MRR | %.3f |\n", r.MRR)
	fmt.Fprintf(&b, "| Hit@1 | %.3f |\n", r.Hit1)
	fmt.Fprintf(&b, "| Hit@3 | %.3f |\n", r.Hit3)
	fmt.Fprintf(&b, "| Recall@5 | %.3f |\n", r.Recall5)
	fmt.Fprintf(&b, "| Recall@20 | %.3f |\n", r.Recall20)
	fmt.Fprintf(&b, "| nDCG@10 | %.3f |\n", r.NDCG10)
	b.WriteString("\n## Queries\n\n| ID | First expected rank | Hit@1 | Hit@3 | Recall@5 | Recall@20 | nDCG@10 | Error |\n|---|---|---|---|---|---|---|---|\n")
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}
	for _, w := range r.Rows {
		rank := "none"
		if w.Rank > 0 {
			rank = fmt.Sprint(w.Rank)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %.2f | %.2f | %.3f | %s |\n", w.ID, rank, yes(w.Hit1), yes(w.Hit3), w.Recall5, w.Recall20,
			w.NDCG10, strings.ReplaceAll(w.Err, "|", "/"))
	}
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// Retrieve calls the MCP tool search_context for each question with
// expected items and returns the results. Other questions are skipped.
func Retrieve(ctx context.Context, client *http.Client, base, token string, qs []Question) ([]Ranking, error) {
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	hc := &http.Client{Timeout: client.Timeout, Transport: bearer{token: token, next: next}}
	c := mcp.NewClient(&mcp.Implementation{Name: "assay-eval", Version: "1"}, nil)
	s, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: strings.TrimRight(base, "/") + "/mcp", HTTPClient: hc}, nil)
	if err != nil {
		return nil, fmt.Errorf("eval: connect to MCP: %w", err)
	}
	defer func() { _ = s.Close() }()
	out := []Ranking{}
	for _, q := range qs {
		if len(q.Expected) == 0 {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		r := Ranking{Question: q}
		args := map[string]any{"query": q.Question}
		for k, v := range RetrievalQueryArgs {
			args[k] = v
		}
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "search_context", Arguments: args})
		switch {
		case err != nil:
			r.Err = err.Error()
		case res.IsError:
			r.Err = "tool error"
			for _, ct := range res.Content {
				if t, ok := ct.(*mcp.TextContent); ok {
					r.Err = t.Text
				}
			}
		default:
			var body struct {
				Results []Ranked `json:"results"`
			}
			raw, _ := json.Marshal(res.StructuredContent)
			if err := json.Unmarshal(raw, &body); err != nil {
				r.Err = err.Error()
			}
			r.Results = body.Results
		}
		out = append(out, r)
	}
	return out, nil
}

// ReadPipeline reads the embed and rerank settings and the active embedding
// space of an instance. The routes need an admin token; with another token
// the pipeline is unknown.
func ReadPipeline(ctx context.Context, client *http.Client, base, token string) Pipeline {
	p := Pipeline{Vector: "unknown", Rerank: "unknown"}
	var fns struct {
		Functions []struct {
			Function   string `json:"function"`
			Model      string `json:"model"`
			Enabled    bool   `json:"enabled"`
			OverBudget bool   `json:"overBudget"`
		} `json:"functions"`
	}
	var spaces struct {
		Spaces []struct {
			Model  string `json:"model"`
			Status string `json:"status"`
		} `json:"spaces"`
	}
	if err := getJSON(ctx, client, base, token, "/api/v1/models/functions", &fns); err != nil {
		p.Note = "The pipeline is unknown: " + err.Error()
		return p
	}
	if err := getJSON(ctx, client, base, token, "/api/v1/models/spaces", &spaces); err != nil {
		p.Note = "The pipeline is unknown: " + err.Error()
		return p
	}
	embedOn := false
	p.Rerank = "off"
	for _, f := range fns.Functions {
		on := f.Enabled && !f.OverBudget
		switch f.Function {
		case "embed":
			embedOn = on
		case "rerank":
			if on {
				p.Rerank, p.RerankModel = "on", f.Model
			}
		}
	}
	p.Vector = "off"
	for _, s := range spaces.Spaces {
		if s.Status == "active" {
			p.EmbedModel = s.Model
			if embedOn {
				p.Vector = "on"
			}
		}
	}
	if p.EmbedModel == "" {
		p.EmbedModel = "none"
	}
	return p
}

func getJSON(ctx context.Context, client *http.Client, base, token, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s answered %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
