// Package eval runs a question set against an Assay instance and scores
// the answers (REQ-EVL-002 to REQ-EVL-004): citation precision and recall,
// fact recall, no-evidence accuracy, latency and cost.
package eval

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Question is one question of a set with what a right answer has.
type Question struct {
	ID       string `yaml:"id"`
	Question string `yaml:"question"`
	// Sources are parts of the titles or URLs of the items that the answer
	// should cite.
	Sources []string `yaml:"sources"`
	// Facts are words that the answer should state.
	Facts []string `yaml:"facts"`
	// NoEvidence is true for a question that the sources cannot answer.
	NoEvidence bool `yaml:"no_evidence"`
}

// Load reads and checks a question set.
func Load(path string) ([]Question, error) {
	b, err := os.ReadFile(path) //nolint:gosec // The operator names the question file.
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes and checks a question set: unique IDs, a question text, and
// sources and facts, or no_evidence with neither.
func Parse(b []byte) ([]Question, error) {
	var qs []Question
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&qs); err != nil {
		return nil, fmt.Errorf("eval: %w", err)
	}
	seen := map[string]bool{}
	var errs []error
	for i, q := range qs {
		where := fmt.Sprintf("question %d (%s)", i+1, q.ID)
		switch {
		case q.ID == "":
			errs = append(errs, fmt.Errorf("%s: no id", where))
		case seen[q.ID]:
			errs = append(errs, fmt.Errorf("%s: the id is not unique", where))
		}
		seen[q.ID] = true
		if strings.TrimSpace(q.Question) == "" {
			errs = append(errs, fmt.Errorf("%s: no question", where))
		}
		if q.NoEvidence && (len(q.Sources) > 0 || len(q.Facts) > 0) {
			errs = append(errs, fmt.Errorf("%s: a no-evidence question has no sources or facts", where))
		}
		if !q.NoEvidence && (len(q.Sources) == 0 || len(q.Facts) == 0) {
			errs = append(errs, fmt.Errorf("%s: give sources and facts, or no_evidence", where))
		}
	}
	return qs, errors.Join(errs...)
}

// Cited is one item that an answer cites.
type Cited struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Result is the answer to one question.
type Result struct {
	Question   Question
	Markdown   string
	Cited      []Cited
	NoEvidence bool
	Latency    time.Duration
	CostUSD    float64
	Err        string
}

// Row is the score of one question.
type Row struct {
	ID         string
	Cited      int
	Correct    int
	Expected   int
	Found      int
	Facts      int
	FactsFound int
	NoEvidence bool // the answer said no evidence
	Right      bool // the no-evidence answer matched the question
	Latency    time.Duration
	CostUSD    float64
	Err        string
}

// Report is the score of a run.
type Report struct {
	Questions          int
	CitationPrecision  float64
	CitationRecall     float64
	FactRecall         float64
	NoEvidenceAccuracy float64
	LatencyP50         time.Duration
	LatencyP95         time.Duration
	CostUSD            float64
	Rows               []Row
}

func matches(c Cited, pattern string) bool {
	p := strings.ToLower(pattern)
	return strings.Contains(strings.ToLower(c.Title), p) || strings.Contains(strings.ToLower(c.URL), p)
}

var space = regexp.MustCompile(`\s+`)

func norm(s string) string { return space.ReplaceAllString(strings.ToLower(s), " ") }

// Score scores the results. Precision and recall count citations over all
// questions with expected sources. A cited item is correct when it matches
// an expected source; an expected source is found when a cited item
// matches it. A fact is found when the answer text holds it, without case.
func Score(rs []Result) Report {
	var rep Report
	rep.Questions = len(rs)
	var cited, correct, expected, found, facts, factsFound, right int
	var lat []time.Duration
	for _, r := range rs {
		row := Row{ID: r.Question.ID, Latency: r.Latency, CostUSD: r.CostUSD, NoEvidence: r.NoEvidence, Err: r.Err}
		if !r.Question.NoEvidence {
			row.Cited = len(r.Cited)
			for _, c := range r.Cited {
				if slices.ContainsFunc(r.Question.Sources, func(p string) bool { return matches(c, p) }) {
					row.Correct++
				}
			}
			row.Expected = len(r.Question.Sources)
			for _, p := range r.Question.Sources {
				if slices.ContainsFunc(r.Cited, func(c Cited) bool { return matches(c, p) }) {
					row.Found++
				}
			}
			text := norm(r.Markdown)
			row.Facts = len(r.Question.Facts)
			for _, f := range r.Question.Facts {
				if strings.Contains(text, norm(f)) {
					row.FactsFound++
				}
			}
		}
		row.Right = r.Err == "" && r.NoEvidence == r.Question.NoEvidence
		cited += row.Cited
		correct += row.Correct
		expected += row.Expected
		found += row.Found
		facts += row.Facts
		factsFound += row.FactsFound
		if row.Right {
			right++
		}
		rep.CostUSD += r.CostUSD
		lat = append(lat, r.Latency)
		rep.Rows = append(rep.Rows, row)
	}
	rep.CitationPrecision = ratio(correct, cited)
	rep.CitationRecall = ratio(found, expected)
	rep.FactRecall = ratio(factsFound, facts)
	rep.NoEvidenceAccuracy = ratio(right, len(rs))
	rep.LatencyP50, rep.LatencyP95 = percentile(lat, 0.5), percentile(lat, 0.95)
	return rep
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// percentile is the nearest-rank percentile.
func percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

// Markdown renders the report.
func (r Report) Markdown(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "| Measure | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Questions | %d |\n", r.Questions)
	fmt.Fprintf(&b, "| Citation precision | %.2f |\n", r.CitationPrecision)
	fmt.Fprintf(&b, "| Citation recall | %.2f |\n", r.CitationRecall)
	fmt.Fprintf(&b, "| Fact recall | %.2f |\n", r.FactRecall)
	fmt.Fprintf(&b, "| No-evidence accuracy | %.2f |\n", r.NoEvidenceAccuracy)
	fmt.Fprintf(&b, "| Latency p50 | %d ms |\n", r.LatencyP50.Milliseconds())
	fmt.Fprintf(&b, "| Latency p95 | %d ms |\n", r.LatencyP95.Milliseconds())
	fmt.Fprintf(&b, "| Cost | %.4f USD |\n", r.CostUSD)
	b.WriteString("\n## Questions\n\n| ID | Cited | Correct | Sources found | Facts found | No evidence | Latency | Cost | Error |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, w := range r.Rows {
		ne := "no"
		if w.NoEvidence {
			ne = "yes"
		}
		if !w.Right {
			ne += " (wrong)"
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d/%d | %d/%d | %s | %d ms | %.4f | %s |\n", w.ID, w.Cited, w.Correct, w.Found, w.Expected,
			w.FactsFound, w.Facts, ne, w.Latency.Milliseconds(), w.CostUSD, strings.ReplaceAll(w.Err, "|", "/"))
	}
	return b.String()
}

// answer is the part of the answer event that the scorer reads.
type answer struct {
	Markdown   string   `json:"markdown"`
	Cited      []string `json:"cited"`
	NoEvidence bool     `json:"noEvidence"`
	Sources    []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"sources"`
	Claims []struct {
		ID       string `json:"id"`
		Evidence []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"evidence"`
	} `json:"claims"`
	Notice *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"notice"`
	Usage *struct {
		CostUSD float64 `json:"costUsd"`
	} `json:"usage"`
}

// Ask asks one question with a bearer token and reads the answer event.
func Ask(ctx context.Context, client *http.Client, base, token string, q Question) Result {
	r := Result{Question: q}
	body, _ := json.Marshal(map[string]string{"question": q.Question})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/v1/ask", bytes.NewReader(body))
	if err != nil {
		r.Err = err.Error()
		return r
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		r.Err = fmt.Sprintf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		return r
	}
	var a *answer
	event := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "answer":
			a = &answer{}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), a); err != nil {
				r.Err = err.Error()
				return r
			}
		case strings.HasPrefix(line, "data: ") && event == "error":
			r.Err = strings.TrimPrefix(line, "data: ")
		}
	}
	r.Latency = time.Since(start)
	if err := sc.Err(); err != nil && r.Err == "" {
		r.Err = err.Error()
	}
	if a == nil {
		if r.Err == "" {
			r.Err = "no answer event"
		}
		return r
	}
	if a.Notice != nil && r.Err == "" {
		r.Err = a.Notice.Code
	}
	r.Markdown, r.NoEvidence = a.Markdown, a.NoEvidence
	if a.Usage != nil {
		r.CostUSD = a.Usage.CostUSD
	}
	for _, s := range a.Sources {
		if slices.Contains(a.Cited, s.ID) {
			r.Cited = append(r.Cited, Cited{Title: s.Title, URL: s.URL})
		}
	}
	for _, c := range a.Claims {
		if slices.Contains(a.Cited, c.ID) {
			for _, e := range c.Evidence {
				r.Cited = append(r.Cited, Cited{Title: e.Title, URL: e.URL})
			}
		}
	}
	return r
}

// Run asks each question in order.
func Run(ctx context.Context, client *http.Client, base, token string, qs []Question) []Result {
	out := make([]Result, 0, len(qs))
	for _, q := range qs {
		if ctx.Err() != nil {
			break
		}
		out = append(out, Ask(ctx, client, base, token, q))
	}
	return out
}
