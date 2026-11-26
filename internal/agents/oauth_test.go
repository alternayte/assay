package agents_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_AGT_008_UnauthorizedNamesResourceMetadata(t *testing.T) {
	env := testkit.Start(t)
	env.Setup("owner@acme.test")
	anon := env.Client()

	// No token and a bad token both get 401 with the resource metadata URL
	// and the Assay error envelope.
	const metadata = testkit.BaseURL + "/.well-known/oauth-protected-resource/mcp"
	for name, value := range map[string]string{"no token": "", "bad token": "eyJhbGciOiJFUzI1NiIsInR5cCI6ImF0K2p3dCJ9.e30.c2ln"} {
		anon.Bearer = value
		r := anon.Do(http.MethodPost, "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		if r.Status != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", name, r.Status)
		}
		if h := r.Header.Get("WWW-Authenticate"); !strings.Contains(h, `resource_metadata="`+metadata+`"`) {
			t.Fatalf("%s: WWW-Authenticate %q", name, h)
		}
		if r.Code() != "unauthorized" {
			t.Fatalf("%s: body %s", name, r.Body)
		}
	}
	anon.Bearer = ""

	// The metadata names the issuer as the only authorization server, and the
	// issuer metadata offers metadata documents with public clients.
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	anon.Must(http.StatusOK, http.MethodGet, strings.TrimPrefix(metadata, testkit.BaseURL), nil).JSON(t, &prm)
	if prm.Resource != testkit.BaseURL+"/mcp" || !slices.Equal(prm.AuthorizationServers, []string{testkit.BaseURL + "/api/auth"}) {
		t.Fatalf("resource metadata %+v", prm)
	}
	var as struct {
		CIMD    bool     `json:"client_id_metadata_document_supported"`
		Methods []string `json:"token_endpoint_auth_methods_supported"`
		PKCE    []string `json:"code_challenge_methods_supported"`
	}
	anon.Must(http.StatusOK, http.MethodGet, "/.well-known/oauth-authorization-server/api/auth", nil).JSON(t, &as)
	if !as.CIMD || !slices.Contains(as.Methods, "none") || !slices.Equal(as.PKCE, []string{"S256"}) {
		t.Fatalf("authorization server metadata %+v", as)
	}
}

func TestSCN_AGT_009_OAuthGrantScopesAudienceAndDisconnect(t *testing.T) {
	env := testkit.Start(t)
	owner := env.Setup("owner@acme.test")
	client := env.RegisterOAuthClient("Acme agent")

	// A read grant searches, and cannot call propose_finding.
	read := env.OAuthToken(owner, client, "read offline_access")
	s := connect(t, &world{env: env}, read)
	if r, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_context", Arguments: map[string]any{"query": "Payments"}}); err != nil || r.IsError {
		t.Fatalf("search_context with a read grant: %v %+v", err, r)
	}
	r, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "propose_finding", Arguments: map[string]any{
		"subject": paymentsAPI, "predicate": "stated", "object": map[string]any{"text": "x"},
		"evidence": []map[string]any{{"item_id": 1, "quote": "x"}}}})
	if err != nil || !r.IsError {
		t.Fatalf("propose_finding with a read grant: %v %+v", err, r)
	}
	// The audit entry names the client and its host.
	var details json.RawMessage
	if err := env.Server.Pool().QueryRow(context.Background(),
		`SELECT details FROM audit_log WHERE action = 'mcp.search_context' ORDER BY id DESC LIMIT 1`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Client     string `json:"client"`
		ClientHost string `json:"clientHost"`
	}
	_ = json.Unmarshal(details, &d)
	if d.Client != client || d.ClientHost != "127.0.0.1" {
		t.Fatalf("audit details %s", details)
	}

	// An authorization request without the /mcp resource fails, so that no
	// token names the issuer as its audience.
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {testkit.OAuthRedirect}, "scope": {"read"},
		"code_challenge": {"x"}, "code_challenge_method": {"S256"}}
	if r := owner.Do(http.MethodGet, "/api/auth/oauth2/authorize?"+q.Encode(), nil); r.Status != http.StatusBadRequest {
		t.Fatalf("authorize without the resource: %d %s", r.Status, r.Body)
	}
	// A token for another audience, here the issuer, opens neither /mcp nor
	// REST. The authorize call goes to auth-all directly, past that check.
	direct := httptest.NewServer(env.Server.Auth().Handler())
	t.Cleanup(direct.Close)
	issuer := env.OAuthTokenAt(owner, direct.URL, client, "read")
	other := env.Client()
	other.Bearer = issuer
	if r := other.Do(http.MethodPost, "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}); r.Status != http.StatusUnauthorized {
		t.Fatalf("an issuer token on /mcp: %d", r.Status)
	}
	if r := other.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("an issuer token on REST: %d", r.Status)
	}
	other.Bearer = read
	if r := other.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("an /mcp token on REST: %d", r.Status)
	}

	// Connected apps lists the client. Disconnect revokes its tokens.
	var apps struct {
		Apps []struct {
			ClientID string   `json:"clientId"`
			Name     string   `json:"name"`
			Host     string   `json:"host"`
			Scopes   []string `json:"scopes"`
		} `json:"apps"`
	}
	owner.Must(http.StatusOK, http.MethodGet, "/api/v1/connected-apps", nil).JSON(t, &apps)
	if len(apps.Apps) != 1 || apps.Apps[0].Name != "Acme agent" || apps.Apps[0].Host != "127.0.0.1" {
		t.Fatalf("connected apps %+v", apps)
	}
	owner.Must(http.StatusOK, http.MethodDelete, "/api/auth/oauth2/consents/"+url.PathEscape(client), nil)
	if _, err := s.ListTools(context.Background(), nil); err == nil {
		t.Fatal("the session works after Disconnect")
	}
}
