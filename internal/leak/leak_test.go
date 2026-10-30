// Package leak_test is the leak suite (SI-01, SI-02): it calls every read
// route of REST and MCP as a reader without the restricted space and looks
// for the canary of that space.
package leak_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

const canary = "canary-7q4z"

const paymentsAPI = "backstage:component:default/payments-api"

type world struct {
	env                   *testkit.Env
	owner, admin2, member *testkit.Client
	tokens                map[*testkit.Client]string
}

// start syncs GitHub, Jira and Confluence ENG, shared with Everyone. With
// restricted, it also syncs the RST space of the owner and plants the
// canary in claims, aliases, a dispute, a cached page, a question and a
// card.
func start(t *testing.T, restricted bool) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test"), tokens: map[*testkit.Client]string{}}
	w.admin2, _ = env.Invite(w.owner, "admin2@acme.test", "admin", "admin2-password-1")
	w.member, _ = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	_, base := testkit.FakeSources(t)
	mk := func(name, kind, path string, scope map[string]any, share bool) {
		id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: name, Kind: kind, BaseURL: base, Path: path, Scope: scope})
		if share {
			testkit.ShareWithEveryone(t, env, w.owner, id)
		}
		if r := env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %s: %+v", name, r)
		}
	}
	mk("acme-github", "github", "", map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}, true)
	mk("acme-jira", "jira", "", map[string]any{"projects": []string{"PAY", "REM"}}, true)
	mk("acme-eng", "confluence", "/confluence", map[string]any{"spaces": []string{"ENG"}}, true)
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	for _, fn := range []string{"compile", "answer"} {
		testkit.SetFunction(t, w.owner, fn, testkit.Function{Endpoint: ep, Model: "fake-chat"})
	}
	if restricted {
		mk("acme-restricted", "confluence", "/confluence", map[string]any{"spaces": []string{"RST"}}, false)
		w.plant(t)
	}
	for _, c := range []*testkit.Client{w.owner, w.admin2, w.member} {
		var out struct {
			Value string `json:"value"`
		}
		c.Must(http.StatusCreated, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "leak"}).JSON(t, &out)
		w.tokens[c] = out.Value
	}
	return w
}

// plant puts the canary next to public data, as the owner sees it.
func (w *world) plant(t *testing.T) {
	t.Helper()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := w.env.Server.Pool().Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	const passage = `(SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p JOIN source_items i ON i.id = p.item_id
	WHERE i.external_id = '2002' AND p.norm LIKE '%CANARY%' ORDER BY p.ordinal LIMIT 1)`
	// A restricted source and alias of a public entity.
	exec(`INSERT INTO entity_sources (entity_id, connection_id)
SELECT e.id, k.id FROM entities e, connections k WHERE e.key = $1 AND k.name = 'acme-restricted'`, paymentsAPI)
	exec(`INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name)
SELECT e.id, k.id, 'CANARY-7Q4Z gateway', 'canary 7q4z gateway', true FROM entities e, connections k
WHERE e.key = $1 AND k.name = 'acme-restricted'`, paymentsAPI)
	// A restricted claim, a restricted dispute of a public claim, and a
	// relation to an entity with only restricted sources.
	exec(`WITH c AS (INSERT INTO claims (subject_id, predicate, object_text, object_norm)
                 SELECT id, 'stated', 'CANARY-7Q4Z exposed test card numbers', 'canary 7q4z exposed test card numbers'
                 FROM entities WHERE key = $1 RETURNING id), p AS `+passage+`
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT c.id, p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM c, p`, paymentsAPI)
	exec(`WITH c AS (SELECT c.id FROM claims c JOIN entities e ON e.id = c.subject_id WHERE e.key = $1 AND c.predicate = 'owned_by' LIMIT 1),
     p AS `+passage+`
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT c.id, p.connection_id, p.item_id, p.hash, 'passage', 'disputes', p.norm, 'confluence' FROM c, p`, paymentsAPI)
	exec(`WITH k AS (SELECT id FROM connections WHERE name = 'acme-restricted'),
     e AS (INSERT INTO entities (type, key) VALUES ('service', 'confluence:canary:service:vault') RETURNING id),
     s AS (INSERT INTO entity_sources (entity_id, connection_id) SELECT e.id, k.id FROM e, k),
     a AS (INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name)
           SELECT e.id, k.id, 'CANARY-7Q4Z vault', 'canary 7q4z vault', true FROM e, k),
     c AS (INSERT INTO claims (subject_id, predicate, object_entity_id)
           SELECT (SELECT id FROM entities WHERE key = $1), 'depends_on', e.id FROM e RETURNING id),
     p AS `+passage+`
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT c.id, p.connection_id, p.item_id, p.hash, 'passage', 'supports', p.norm, 'confluence' FROM c, p`, paymentsAPI)

	// The owner compiles the page, asks about the canary and reports the
	// restricted claim.
	w.owner.Must(http.StatusOK, http.MethodPost, "/api/v1/entities/"+url.PathEscape(paymentsAPI)+"/page/compile", nil)
	w.owner.Must(http.StatusOK, http.MethodPost, "/api/v1/ask", map[string]string{"question": "What did CANARY-7Q4Z expose?"})
	var claim int64
	if err := w.env.Server.Pool().QueryRow(context.Background(),
		`SELECT id FROM claims WHERE object_text LIKE 'CANARY-7Q4Z%'`).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/problems", claim),
		map[string]any{"problem": "outdated", "reason": "CANARY-7Q4Z is closed."})
}

// ids returns the values that fill the path parameters: keys of public,
// planted and restricted entities, and IDs of the rows of each table.
func (w *world) ids(t *testing.T) map[string][]string {
	t.Helper()
	q := func(sql string) []string {
		rows, err := w.env.Server.Pool().Query(context.Background(), sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v any
			if err := rows.Scan(&v); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprint(v))
		}
		return out
	}
	keys := append([]string{paymentsAPI}, q(`SELECT key FROM entities WHERE id IN (SELECT entity_id FROM entity_sources es
		JOIN connections k ON k.id = es.connection_id WHERE k.name = 'acme-restricted') ORDER BY id LIMIT 30`)...)
	nums := q(`SELECT id::text FROM source_items UNION SELECT id::text FROM questions UNION SELECT id::text FROM proposals
		UNION SELECT id::text FROM connections UNION SELECT id::text FROM decisions UNION SELECT id::text FROM merges`)
	return map[string][]string{"key": keys, "id": nums}
}

// get sends a request as the client with its session.
func get(cl *testkit.Client, method, path string, body any) string {
	r := cl.Do(method, path, body)
	return string(r.Body)
}

var param = regexp.MustCompile(`\{([A-Za-z]+)\}`)

// paths fills each GET route of the OpenAPI document with the values.
func paths(t *testing.T, values map[string][]string) []string {
	t.Helper()
	doc, err := app.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}
	var out []string
	for p, ops := range spec.Paths {
		if _, ok := ops["get"]; !ok {
			continue
		}
		filled := []string{p}
		for _, m := range param.FindAllStringSubmatch(p, -1) {
			vs := values["id"]
			if m[1] == "key" {
				vs = values["key"]
			}
			if m[1] != "key" && m[1] != "id" {
				vs = []string{"x"}
			}
			var next []string
			for _, f := range filled {
				for _, v := range vs {
					next = append(next, strings.Replace(f, m[0], url.PathEscape(v), 1))
				}
			}
			filled = next
		}
		out = append(out, filled...)
	}
	sort.Strings(out)
	return out
}

func (w *world) mcpSession(t *testing.T, cl *testkit.Client) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "leak", Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: w.env.HTTP.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer(w.tokens[cl])}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

// mcpCalls calls every read tool with the values and returns the outputs.
func mcpCalls(t *testing.T, s *mcp.ClientSession, keys []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	call := func(name string, args map[string]any) {
		r, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _ := json.Marshal(r)
		a, _ := json.Marshal(args)
		out[name+" "+string(a)] = string(b)
	}
	for _, q := range []string{"CANARY-7Q4Z", "Project Falcon acquisition", "Payments API debug log", "Falcon Typesetting"} {
		call("search_context", map[string]any{"query": q})
		call("get_decisions", map[string]any{"query": q})
		call("get_terms", map[string]any{"text": q})
	}
	for _, k := range keys {
		call("get_entity", map[string]any{"key": k})
		call("get_knowledge_page", map[string]any{"key": k})
		call("trace_relationships", map[string]any{"key": k})
		call("get_decisions", map[string]any{"entity_key": k})
		call("get_terms", map[string]any{"keys": []string{k}})
	}
	return out
}

// posts are the read routes that take a body.
func posts(cl *testkit.Client) map[string]string {
	out := map[string]string{}
	for _, q := range []string{"What did the debug log of the Payments API expose?", "What is Project Falcon?", "Who owns the Payments API?"} {
		out["ask "+q] = get(cl, http.MethodPost, "/api/v1/ask", map[string]string{"question": q})
	}
	out["links"] = get(cl, http.MethodPost, "/api/v1/terms/links", map[string]string{"text": "CANARY-7Q4Z gateway and Falcon"})
	out["compile"] = get(cl, http.MethodPost, "/api/v1/entities/"+url.PathEscape(paymentsAPI)+"/page/compile", nil)
	return out
}

func TestSCN_VIS_011_NoCanaryInAnyReadResponse(t *testing.T) {
	w := start(t, true)
	values := w.ids(t)

	// The owner sees the canary, so the suite reads real data.
	if !strings.Contains(strings.ToLower(get(w.owner, http.MethodGet, "/api/v1/entities/"+url.PathEscape(paymentsAPI), nil)), canary) {
		t.Fatal("the owner does not see the planted canary")
	}

	checked := 0
	check := func(where, body string) {
		checked++
		if strings.Contains(strings.ToLower(body), canary) {
			i := strings.Index(strings.ToLower(body), canary)
			t.Errorf("%s shows the canary: …%s…", where, body[max(0, i-120):min(len(body), i+60)])
		}
	}
	for _, p := range paths(t, values) {
		check("GET "+p, get(w.member, http.MethodGet, p, nil))
		check("GET "+p+"?q=", get(w.member, http.MethodGet, p+"?q=CANARY-7Q4Z", nil))
	}
	for _, k := range values["key"] {
		check("GET /e/.md "+k, get(w.member, http.MethodGet, "/e/"+url.PathEscape(k)+".md", nil))
	}
	for name, body := range posts(w.member) {
		check("POST "+name, body)
	}
	for name, body := range mcpCalls(t, w.mcpSession(t, w.member), values["key"]) {
		check("MCP "+name, body)
	}
	if checked < 200 {
		t.Fatalf("only %d responses checked", checked)
	}
}

// shape counts the elements of every list in a JSON value by path, with no
// IDs or times, so that two instances compare.
func shape(body string) map[string]int {
	var v any
	if err := json.NewDecoder(bytes.NewBufferString(body)).Decode(&v); err != nil {
		return map[string]int{"status": len(body)}
	}
	out := map[string]int{}
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				walk(path+"."+k, y)
			}
		case []any:
			out[path] += len(x)
			for _, y := range x {
				walk(path+"[]", y)
			}
		}
	}
	walk("", v)
	return out
}

func TestSCN_VIS_011_CountsEqualPublicData(t *testing.T) {
	with, without := start(t, true), start(t, false)
	reads := []string{
		"/api/v1/search?q=payments", "/api/v1/search?q=Falcon", "/api/v1/search?q=CANARY-7Q4Z",
		"/api/v1/entities", "/api/v1/entities?q=canary", "/api/v1/entities?type=decision",
		"/api/v1/entities/" + url.PathEscape(paymentsAPI), "/api/v1/entities/" + url.PathEscape(paymentsAPI) + "/trace",
		"/api/v1/terms", "/api/v1/updates",
	}
	for _, p := range reads {
		a, b := shape(get(with.member, http.MethodGet, p, nil)), shape(get(without.member, http.MethodGet, p, nil))
		if !mapsEqual(a, b) {
			t.Errorf("%s: counts with the restricted space %v, without %v", p, a, b)
		}
	}
	// The owner counts differ, so the planted data reaches the compared
	// responses.
	entity := "/api/v1/entities/" + url.PathEscape(paymentsAPI)
	if mapsEqual(shape(get(with.owner, http.MethodGet, entity, nil)), shape(get(without.owner, http.MethodGet, entity, nil))) {
		t.Fatal("the owner sees the same counts with and without the restricted space")
	}
	page := "/api/v1/entities/" + url.PathEscape(paymentsAPI) + "/page"
	a, b := shape(get(with.member, http.MethodGet, page, nil)), shape(get(without.member, http.MethodGet, page, nil))
	delete(a, ".summary.citations")
	delete(b, ".summary.citations")
	if !mapsEqual(a, b) {
		t.Errorf("page: %v, %v", a, b)
	}
}

func mapsEqual(a, b map[string]int) bool {
	ka, kb := keysOf(a), keysOf(b)
	if !slices.Equal(ka, kb) {
		return false
	}
	for _, k := range ka {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}

func keysOf(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v > 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestSCN_VIS_001_AdminGetsNoRestrictedContent(t *testing.T) {
	w := start(t, true)
	values := w.ids(t)
	check := func(where, body string) {
		if strings.Contains(strings.ToLower(body), canary) {
			t.Errorf("admin2: %s shows the canary", where)
		}
	}
	for _, k := range values["key"] {
		for _, p := range []string{"/api/v1/entities/" + url.PathEscape(k), "/api/v1/entities/" + url.PathEscape(k) + "/page",
			"/api/v1/entities/" + url.PathEscape(k) + "/page.md", "/e/" + url.PathEscape(k) + ".md"} {
			check(p, get(w.admin2, http.MethodGet, p, nil))
		}
	}
	for _, q := range []string{"CANARY-7Q4Z", "Falcon"} {
		check("search "+q, get(w.admin2, http.MethodGet, "/api/v1/search?q="+url.QueryEscape(q), nil))
	}
	for name, body := range mcpCalls(t, w.mcpSession(t, w.admin2), values["key"]) {
		check("MCP "+name, body)
	}

}
