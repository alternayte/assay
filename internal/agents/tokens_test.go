package agents_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alternayte/assay/internal/testkit"
	"github.com/alternayte/assay/internal/visibility"
)

// raw sends a request with a bearer token and no cookie.
func raw(t *testing.T, w *world, method, path, value string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, w.env.HTTP.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+value)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

const listTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

func TestSCN_VIS_010_TokenHasVisibleSetOfUser(t *testing.T) {
	w := start(t)
	member, owner := token(t, w.member), token(t, w.owner)
	const restricted = "Project Falcon acquisition plan"

	// REST with a token.
	_, m := raw(t, w, http.MethodGet, "/api/v1/search?q=Falcon%20acquisition", member, "")
	_, o := raw(t, w, http.MethodGet, "/api/v1/search?q=Falcon%20acquisition", owner, "")
	if strings.Contains(m, restricted) || !strings.Contains(o, restricted) {
		t.Fatalf("search: member %v, owner %v", strings.Contains(m, restricted), strings.Contains(o, restricted))
	}

	// MCP with a token.
	for _, c := range []struct {
		value string
		want  bool
	}{{member, false}, {owner, true}} {
		s := connect(t, w, c.value)
		out := call(t, s, toolsOf(t, s), "search_context", map[string]any{"query": "Falcon acquisition plan"})
		got := false
		for _, r := range out["results"].([]any) {
			got = got || r.(map[string]any)["title"] == restricted
		}
		if got != c.want {
			t.Fatalf("MCP search_context with the token of the %v reader: restricted result %v", c.want, got)
		}
	}
}

func TestSCN_USR_005_DeactivationEndsSessionAndToken(t *testing.T) {
	w := start(t)
	value := token(t, w.member)
	if code, body := raw(t, w, http.MethodPost, "/mcp", value, listTools); code != http.StatusOK {
		t.Fatalf("MCP before deactivation: %d %s", code, body)
	}
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
	var users struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/users", nil).JSON(t, &users)
	var id string
	for _, u := range users.Users {
		if u.Email == "member@acme.test" {
			id = u.ID
		}
	}
	w.owner.Must(http.StatusOK, http.MethodPost, "/api/v1/users/"+id+"/deactivate", nil)

	if r := w.member.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("REST with the session after deactivation: %d", r.Status)
	}
	if code, _ := raw(t, w, http.MethodPost, "/mcp", value, listTools); code != http.StatusUnauthorized {
		t.Fatalf("MCP with the token after deactivation: %d", code)
	}
}

func TestSCN_AGT_006_TokensStoredAsHashes(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	access := token(t, w.member)
	var inv struct {
		URL string `json:"url"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/invites", map[string]string{"email": "new@acme.test", "role": "member"}).JSON(t, &inv)
	invite := inv.URL[strings.Index(inv.URL, "#")+1:]
	var link struct {
		URL string `json:"url"`
	}
	var users struct {
		Users []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/users", nil).JSON(t, &users)
	for _, u := range users.Users {
		if u.Email == "member@acme.test" {
			w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/users/"+u.ID+"/reset-link", nil).JSON(t, &link)
		}
	}
	reset := link.URL[strings.Index(link.URL, "#")+1:]
	values := map[string]string{"access": access, "invite": invite, "reset": reset, "setup": testkit.SetupToken}
	for name, v := range values {
		if len(v) < 16 {
			t.Fatalf("%s token %q", name, v)
		}
	}

	// No column of any table holds a token value.
	rows, err := w.env.Server.Pool().Query(ctx, `
SELECT table_schema, table_name, column_name FROM information_schema.columns
WHERE table_schema = 'public' AND data_type IN ('text', 'character varying', 'jsonb', 'json', 'bytea')`)
	if err != nil {
		t.Fatal(err)
	}
	type col struct{ schema, table, column string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.schema, &c.table, &c.column); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	for _, c := range cols {
		for name, v := range values {
			var n int
			q := fmt.Sprintf(`SELECT count(*) FROM %q.%q WHERE position($1 in convert_from(convert_to(%q::text, 'UTF8'), 'UTF8')) > 0`, c.schema, c.table, c.column)
			if err := w.env.Server.Pool().QueryRow(ctx, q, v).Scan(&n); err != nil {
				t.Fatalf("%s.%s: %v", c.table, c.column, err)
			}
			if n > 0 {
				t.Errorf("the %s token is in %s.%s", name, c.table, c.column)
			}
		}
	}

	// The stores keep SHA-256 digests.
	digest := sha256.Sum256([]byte(access))
	if n := w.env.Count(`SELECT count(*) FROM auth_api_keys WHERE key_hash = $1`, hex.EncodeToString(digest[:])); n != 1 {
		t.Fatalf("%d keys with the digest of the access token", n)
	}
	inviteDigest := sha256.Sum256([]byte(invite))
	if n := w.env.Count(`SELECT count(*) FROM invites WHERE token_hash = $1`, inviteDigest[:]); n != 1 {
		t.Fatalf("%d invites with the digest of the invite token", n)
	}
	resetDigest := sha256.Sum256([]byte(reset))
	if n := w.env.Count(`SELECT count(*) FROM password_resets WHERE token_hash = $1`, resetDigest[:]); n != 1 {
		t.Fatalf("%d resets with the digest of the reset token", n)
	}
}

func TestSCN_CLM_006_ClaimWithoutEvidenceIsRetracted(t *testing.T) {
	src, base := testkit.FakeSources(t)
	env := testkit.Start(t)
	owner := env.Setup("owner@acme.test")
	w := &world{env: env, owner: owner}
	id := testkit.CreateConnection(t, owner, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api"}}})
	if r := env.RunNow(owner, id, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	// The CODEOWNERS file is the only evidence of the owner of the repository.
	var claim int64
	var key string
	if err := env.Server.Pool().QueryRow(context.Background(), `
SELECT c.id, s.key FROM claims c JOIN entities s ON s.id = c.subject_id
JOIN claim_evidence e ON e.claim_id = c.id JOIN source_items i ON i.id = e.item_id
WHERE c.predicate = 'owned_by' AND s.type = 'repository' AND i.external_id LIKE '%CODEOWNERS'
  AND NOT EXISTS (SELECT 1 FROM claim_evidence x WHERE x.claim_id = c.id AND x.item_id <> i.id)`).Scan(&claim, &key); err != nil {
		t.Fatal(err)
	}
	for _, f := range src.RepoFiles("github", "acme/payments-api") {
		if strings.HasSuffix(f, "CODEOWNERS") {
			src.DeleteFile("github", "acme/payments-api", f)
		}
	}
	if r := env.RunNow(owner, id, "reconcile"); r.Status != "succeeded" || r.Deleted == 0 {
		t.Fatalf("reconcile after the delete: %+v", r)
	}
	if n := env.Count(`SELECT count(*) FROM claims WHERE id = $1 AND retracted_at IS NOT NULL`, claim); n != 1 {
		t.Fatal("the claim is not retracted")
	}
	mentions := func(body string) bool { return strings.Contains(body, fmt.Sprintf(`"id":%d,`, claim)) }
	if r := owner.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(key)+"/page", nil); mentions(string(r.Body)) {
		t.Fatal("the page shows the retracted claim")
	}
	value := token(t, owner)
	s := connect(t, w, value)
	tools := toolsOf(t, s)
	e := call(t, s, tools, "get_entity", map[string]any{"key": key})
	ctx := call(t, s, tools, "search_context", map[string]any{"query": "Who owns the payments-api repository?"})
	for _, out := range []map[string]any{e, ctx} {
		b, _ := json.Marshal(out)
		if mentions(string(b)) {
			t.Fatalf("MCP shows the retracted claim: %s", b)
		}
	}
	c, err := env.Server.Ask().Build(context.Background(), visibilityOf(t, env, owner), "Who owns the payments-api repository?")
	if err != nil {
		t.Fatal(err)
	}
	for _, cl := range c.Claims {
		if cl.ClaimID == claim {
			t.Fatal("the answer context holds the retracted claim")
		}
	}
}

func visibilityOf(t *testing.T, env *testkit.Env, cl *testkit.Client) visibility.Set {
	t.Helper()
	var me struct {
		ID string `json:"id"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).JSON(t, &me)
	vis, err := visibility.For(context.Background(), env.Server.Pool(), me.ID)
	if err != nil {
		t.Fatal(err)
	}
	return vis
}

func TestSCN_AGT_003_TokenScopes(t *testing.T) {
	w := start(t)
	read, both, propose := token(t, w.owner), token(t, w.owner, "read", "propose"), token(t, w.owner, "propose")

	// The list shows the scopes; no scope gives read; an unknown scope fails.
	var list struct {
		Tokens []struct {
			Scopes []string `json:"scopes"`
		} `json:"tokens"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/tokens", nil).JSON(t, &list)
	got := []string{}
	for _, k := range list.Tokens {
		got = append(got, strings.Join(k.Scopes, " "))
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"propose", "propose read", "read"}) {
		t.Fatalf("token scopes %v", got)
	}
	w.owner.Must(http.StatusUnprocessableEntity, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "x", "scopes": []string{"admin"}})

	item := w.scalar(t, "SELECT id FROM source_items WHERE external_id = 'PAY-1'").(int64)
	text := w.scalar(t, "SELECT text FROM passages WHERE item_id = $1 AND length(text) > 40 ORDER BY ordinal LIMIT 1", item).(string)
	finding := fmt.Sprintf(`{"subject":%q,"predicate":"stated","object":{"text":"Retries back off for one hour."},"evidence":[{"itemId":%d,"quote":%q}]}`,
		paymentsAPI, item, strings.TrimSpace(text[:30]))
	for _, c := range []struct {
		name, value, method, path, body string
		want                            int
	}{
		{"read searches", read, http.MethodGet, "/api/v1/search?q=Payments", "", http.StatusOK},
		{"read links terms", read, http.MethodPost, "/api/v1/terms/links", `{"text":"Payments API"}`, http.StatusOK},
		{"read cannot propose", read, http.MethodPost, "/api/v1/findings", finding, http.StatusForbidden},
		{"propose cannot search", propose, http.MethodGet, "/api/v1/search?q=Payments", "", http.StatusForbidden},
		{"propose proposes", propose, http.MethodPost, "/api/v1/findings", finding, http.StatusCreated},
		{"no scope changes settings", both, http.MethodPut, "/api/v1/settings/retention", `{"value":{"questionsDays":30},"revision":1}`, http.StatusForbidden},
		{"no scope creates groups", both, http.MethodPost, "/api/v1/groups", `{"name":"Agents"}`, http.StatusForbidden},
	} {
		if code, body := raw(t, w, c.method, c.path, c.value, c.body); code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, code, c.want, body)
		}
	}

	// MCP: a read token cannot call propose_finding, and the refusal is audited.
	s := connect(t, w, read)
	r, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "propose_finding", Arguments: map[string]any{
		"subject": paymentsAPI, "predicate": "stated", "object": map[string]any{"text": "x"},
		"evidence": []map[string]any{{"item_id": item, "quote": strings.TrimSpace(text[:30])}}}})
	if err != nil || !r.IsError {
		t.Fatalf("propose_finding with a read token: %v %+v", err, r)
	}
	if n := w.env.Count(`SELECT count(*) FROM audit_log WHERE action = 'mcp.propose_finding' AND details ? 'error'`); n != 1 {
		t.Fatalf("%d audit rows for the refused call", n)
	}
	r, err = connect(t, w, propose).CallTool(context.Background(), &mcp.CallToolParams{Name: "search_context", Arguments: map[string]any{"query": "Payments"}})
	if err != nil || !r.IsError {
		t.Fatalf("search_context with a propose token: %v %+v", err, r)
	}
}
