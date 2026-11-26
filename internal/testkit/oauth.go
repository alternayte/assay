package testkit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// OAuthRedirect is the loopback redirect URI of the test MCP client.
const OAuthRedirect = "http://127.0.0.1/callback"

// RegisterOAuthClient registers a public MCP client with dynamic
// registration and returns its client ID.
func (e *Env) RegisterOAuthClient(name string) string {
	e.t.Helper()
	var out struct {
		ClientID string `json:"client_id"`
	}
	e.Client().Must(http.StatusCreated, http.MethodPost, "/api/auth/oauth2/register", map[string]any{
		"client_name": name, "redirect_uris": []string{OAuthRedirect}, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
	}).JSON(e.t, &out)
	if out.ClientID == "" {
		e.t.Fatal("the registration returned no client ID")
	}
	return out.ClientID
}

// OAuthToken runs the authorization code flow with PKCE as the signed-in
// client c: authorize, the consent page, and the token call. It approves
// the request on the consent page when Assay shows it, and returns the
// access token for the /mcp resource with the scopes in scope.
func (e *Env) OAuthToken(c *Client, clientID, scope string) string {
	e.t.Helper()
	return e.OAuthTokenAt(c, e.HTTP.URL, clientID, scope)
}

// OAuthTokenAt is OAuthToken with the authorize call sent to base, so that a
// test can send it to another handler of the same server.
func (e *Env) OAuthTokenAt(c *Client, base, clientID, scope string) string {
	e.t.Helper()
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {OAuthRedirect}, "scope": {scope},
		"state": {"state-1"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	if base == e.HTTP.URL {
		q.Set("resource", e.Config.BaseURL+"/mcp")
	}
	h := *c.http
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/api/auth/oauth2/authorize?"+q.Encode(), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := h.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("authorize: status %d: %s", resp.StatusCode, body)
	}
	target := resp.Header.Get("Location")
	if rest, ok := strings.CutPrefix(target, e.Config.BaseURL+"/oauth/consent?"); ok {
		v, _ := url.ParseQuery(rest)
		id := v.Get("request_id")
		c.Must(http.StatusOK, http.MethodGet, "/api/auth/oauth2/request?request_id="+url.QueryEscape(id), nil)
		var d struct {
			RedirectTo string `json:"redirectTo"`
		}
		c.Must(http.StatusOK, http.MethodPost, "/api/auth/oauth2/decide", map[string]any{"requestId": id, "approve": true}).JSON(e.t, &d)
		target = d.RedirectTo
	}
	u, err := url.Parse(target)
	if err != nil || !strings.HasPrefix(target, OAuthRedirect) || u.Query().Get("code") == "" {
		e.t.Fatalf("the flow returned to %q", target)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "redirect_uri": {OAuthRedirect},
		"client_id": {clientID}, "code_verifier": {verifier}}
	if base == e.HTTP.URL {
		form.Set("resource", e.Config.BaseURL+"/mcp")
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	e.Client().Must(http.StatusOK, http.MethodPost, "/api/auth/oauth2/token",
		Raw{ContentType: "application/x-www-form-urlencoded", Data: []byte(form.Encode())}).JSON(e.t, &tok)
	if tok.AccessToken == "" {
		e.t.Fatal("the token call returned no access token")
	}
	return tok.AccessToken
}
