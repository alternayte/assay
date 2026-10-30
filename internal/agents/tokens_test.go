package agents_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
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
