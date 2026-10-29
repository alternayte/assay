// Package fixtures holds the fixture server: fake source APIs, a fake
// OpenAI-compatible model API and a fake Decisions API, with the synthetic
// Acme corpus. Tests, the e2e suite and the demo use it. Assay never calls a
// real model in tests.
package fixtures

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// FakeKey is the API key that the fake model API accepts.
const FakeKey = "fake-key"

// FakeModelIDs are the models that the fake lists.
var FakeModelIDs = []string{"fake-chat", "fake-embed-64", "fake-embed-32", "fake-rerank", "fake-decide"}

// Request is one request that the fake received.
type Request struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
}

// Reply is a scripted response.
type Reply struct {
	Status  int
	Header  http.Header
	Body    any
	Block   bool // Block holds the request until the client gives up.
	Content string
}

// Decider answers one Decisions API question. It returns nil to use the
// default answer.
type Decider func(name string, question map[string]any, state map[string]any) map[string]any

// Models is the fake model API. The zero value is not usable; call NewModels.
type Models struct {
	mu       sync.Mutex
	requests []Request
	queue    map[string][]Reply
	chat     func(body map[string]any) string
	decider  Decider
}

// NewModels returns a fake model API.
func NewModels() *Models {
	return &Models{queue: map[string][]Reply{}}
}

// Requests returns the requests so far.
func (m *Models) Requests() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.requests...)
}

// RequestsTo returns the requests to one path.
func (m *Models) RequestsTo(path string) []Request {
	var out []Request
	for _, r := range m.Requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

// Reset forgets the requests and the scripted replies.
func (m *Models) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = nil
	m.queue = map[string][]Reply{}
}

// Queue adds scripted replies for a path. They answer in order before the
// default behaviour applies again.
func (m *Models) Queue(path string, replies ...Reply) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue[path] = append(m.queue[path], replies...)
}

// OnChat sets the content of chat replies.
func (m *Models) OnChat(f func(body map[string]any) string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chat = f
}

// OnDecide sets the decider of the Decisions API.
func (m *Models) OnDecide(d Decider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decider = d
}

func (m *Models) next(path string) (Reply, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queue[path]
	if len(q) == 0 {
		return Reply{}, false
	}
	m.queue[path] = q[1:]
	return q[0], true
}

// ServeHTTP serves the fake API under /v1 and /api/alpha/decisions.
func (m *Models) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if i := strings.Index(path, "/v1/"); i > 0 {
		path = path[i:]
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	m.mu.Lock()
	m.requests = append(m.requests, Request{Method: r.Method, Path: path, Header: r.Header.Clone(), Body: body})
	m.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+FakeKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"message": "Invalid API key", "code": 401}})
		return
	}
	if rep, ok := m.next(path); ok {
		if rep.Block {
			<-r.Context().Done()
			return
		}
		for k, v := range rep.Header {
			w.Header()[k] = v
		}
		if rep.Status >= 400 || rep.Body != nil {
			status := rep.Status
			if status == 0 {
				status = http.StatusOK
			}
			writeJSON(w, status, rep.Body)
			return
		}
		if rep.Content != "" && path == "/v1/chat/completions" {
			writeChat(w, body, rep.Content)
			return
		}
	}

	switch {
	case r.Method == http.MethodGet && path == "/v1/models":
		data := make([]map[string]any, 0, len(FakeModelIDs))
		for _, id := range FakeModelIDs {
			data = append(data, map[string]any{"id": id, "object": "model"})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	case r.Method == http.MethodPost && path == "/v1/chat/completions":
		m.mu.Lock()
		f := m.chat
		m.mu.Unlock()
		content := "This is a fake answer."
		if f != nil {
			content = f(body)
		}
		writeChat(w, body, content)
	case r.Method == http.MethodPost && path == "/v1/embeddings":
		writeJSON(w, http.StatusOK, embedReply(body))
	case r.Method == http.MethodPost && path == "/v1/rerank":
		writeJSON(w, http.StatusOK, rerankReply(body))
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/alpha/decisions"):
		m.mu.Lock()
		d := m.decider
		m.mu.Unlock()
		writeJSON(w, http.StatusOK, decideReply(body, d))
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "not found"}})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// EstimateTokens counts tokens the way the fake reports usage.
func EstimateTokens(s string) int {
	runes := (utf8.RuneCountInString(s) + 3) / 4
	words := len(strings.Fields(s))
	return max(runes, words)
}

// writeChat answers a chat completion, as server-sent events when the
// request sets stream. The stream sends the content word by word, then the
// usage.
func writeChat(w http.ResponseWriter, body map[string]any, content string) {
	reply := chatReply(body, content)
	if stream, _ := body["stream"].(bool); !stream {
		writeJSON(w, http.StatusOK, reply)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
	model := reply["model"]
	for _, piece := range strings.SplitAfter(content, " ") {
		send(map[string]any{"id": "fake-chat-1", "object": "chat.completion.chunk", "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": piece}}}})
	}
	send(map[string]any{"id": "fake-chat-1", "object": "chat.completion.chunk", "model": model, "choices": []any{},
		"usage": reply["usage"]})
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func chatReply(body map[string]any, content string) map[string]any {
	in := 0
	if msgs, ok := body["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				s, _ := mm["content"].(string)
				in += EstimateTokens(s)
			}
		}
	}
	model, _ := body["model"].(string)
	return map[string]any{
		"id": "fake-chat-1", "object": "chat.completion", "model": model,
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": content}}},
		"usage": map[string]any{"prompt_tokens": in, "completion_tokens": EstimateTokens(content), "total_tokens": in + EstimateTokens(content)},
	}
}

// FakeVector is the deterministic embedding of text in dim dimensions. Texts
// that share words get similar vectors.
func FakeVector(text string, dim int) []float32 {
	v := make([]float64, dim)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		h := sha256.Sum256([]byte(strings.Trim(w, ".,;:!?()[]\"'")))
		for i := 0; i < 4; i++ {
			idx := int(binary.BigEndian.Uint32(h[i*4:]) % uint32(dim)) //nolint:gosec // dim is small and positive.
			sign := 1.0
			if h[16+i]&1 == 1 {
				sign = -1
			}
			v[idx] += sign
		}
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	out := make([]float32, dim)
	for i, x := range v {
		if norm > 0 {
			out[i] = float32(x / norm)
		}
	}
	if norm == 0 {
		out[0] = 1
	}
	return out
}

// FakeDim is the dimension of a fake embedding model: the number after the
// last dash, or 64.
func FakeDim(model string) int {
	if i := strings.LastIndex(model, "-"); i >= 0 {
		if n, err := strconv.Atoi(model[i+1:]); err == nil && n > 0 && n <= 4096 {
			return n
		}
	}
	return 64
}

func embedReply(body map[string]any) map[string]any {
	model, _ := body["model"].(string)
	dim := FakeDim(model)
	var inputs []string
	switch in := body["input"].(type) {
	case string:
		inputs = []string{in}
	case []any:
		for _, x := range in {
			s, _ := x.(string)
			inputs = append(inputs, s)
		}
	}
	data := make([]any, 0, len(inputs))
	tokens := 0
	for i, s := range inputs {
		tokens += EstimateTokens(s)
		data = append(data, map[string]any{"object": "embedding", "index": i, "embedding": FakeVector(s, dim)})
	}
	return map[string]any{"object": "list", "model": model, "data": data,
		"usage": map[string]any{"prompt_tokens": tokens, "total_tokens": tokens}}
}

// rerankReply ranks the documents in reverse input order, so that a test can
// see that the rerank order is the result order.
func rerankReply(body map[string]any) map[string]any {
	docs, _ := body["documents"].([]any)
	n := len(docs)
	top := n
	if t, ok := body["top_n"].(float64); ok && int(t) < n {
		top = int(t)
	}
	results := make([]any, 0, top)
	tokens := 0
	for i := 0; i < top; i++ {
		idx := n - 1 - i
		s, _ := docs[idx].(string)
		tokens += EstimateTokens(s)
		results = append(results, map[string]any{"index": idx, "relevance_score": 1 - float64(i)/float64(n+1)})
	}
	return map[string]any{"results": results, "usage": map[string]any{"total_tokens": tokens}}
}

func decideReply(body map[string]any, d Decider) map[string]any {
	questions, _ := body["questions"].(map[string]any)
	state, _ := body["state"].(map[string]any)
	answers := map[string]any{}
	stateJSON, _ := json.Marshal(state)
	in := EstimateTokens(string(stateJSON))
	for name, q := range questions {
		qm, _ := q.(map[string]any)
		instr, _ := qm["instructions"].(string)
		in += EstimateTokens(instr)
		var a map[string]any
		if d != nil {
			a = d(name, qm, state)
		}
		if a == nil {
			a = defaultAnswer(qm)
		}
		answers[name] = a
	}
	model, _ := body["model"].(string)
	return map[string]any{"id": "gen-dec-fake", "model": model + "-fake", "provider": "Fake", "answers": answers,
		"usage": map[string]any{"input_tokens": in, "output_tokens": 0, "cost": 0}}
}

// defaultAnswer gives an even answer: 0.5 for yes, equal choices, the middle
// of a score.
func defaultAnswer(q map[string]any) map[string]any {
	switch q["type"] {
	case "choice":
		crit, _ := q["criteria"].(map[string]any)
		probs := map[string]any{}
		first := ""
		for k := range crit {
			if first == "" || k < first {
				first = k
			}
			probs[k] = 1 / float64(max(len(crit), 1))
		}
		return map[string]any{"type": "choice", "choice": first, "confidence": 0, "probabilities": probs}
	case "score":
		crit, _ := q["criteria"].([]any)
		probs := map[string]any{}
		legend := map[string]any{}
		for i, c := range crit {
			probs[strconv.Itoa(i)] = 1 / float64(max(len(crit), 1))
			legend[strconv.Itoa(i)] = c
		}
		return map[string]any{"type": "score", "score": float64(len(crit)-1) / 2, "confidence": 0, "probabilities": probs, "legend": legend}
	default:
		return map[string]any{"type": "noul", "noul": 0.5}
	}
}
