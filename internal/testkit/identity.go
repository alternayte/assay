package testkit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/sso"
)

// FakeIdentity starts the fake Entra ID tenant and returns it with the base
// URL of its server.
func FakeIdentity(t testing.TB) (*fixtures.Identity, string) {
	t.Helper()
	id := fixtures.NewIdentity()
	srv := httptest.NewServer(id)
	t.Cleanup(srv.Close)
	return id, srv.URL
}

// WithIdentity configures SSO and the Entra ID directory sync against a fake
// tenant through the environment variables.
func WithIdentity(baseURL string) Option {
	return func(c *app.Config, _ *app.Options) {
		if c.SSO == nil {
			c.SSO = map[string]string{}
		}
		c.SSO[sso.EnvIssuer] = fixtures.Issuer(baseURL)
		c.SSO[sso.EnvClientID] = fixtures.IdentityClientID
		c.SSO[sso.EnvClientSecret] = fixtures.IdentityClientSecret
		c.SSO[sso.EnvDirectory] = sso.DirectoryEntra
		c.SSO[sso.EnvGraphURL] = fixtures.GraphBase(baseURL)
	}
}

// SignInSSO signs a new client in through the SSO button as the user of the
// fake tenant with email. It returns the client and the page where the
// browser lands, such as /explore or /sign-in?error=SSO_ACCESS_DENIED.
func (e *Env) SignInSSO(email, next string) (*Client, string) {
	e.t.Helper()
	c := e.Client()
	h := *c.http
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(u string) string {
		e.t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, u, nil)
		if err != nil {
			e.t.Fatal(err)
		}
		resp, err := h.Do(req)
		if err != nil {
			e.t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			e.t.Fatalf("GET %s: status %d, want 302", u, resp.StatusCode)
		}
		return resp.Header.Get("Location")
	}
	authorize := get(e.HTTP.URL + "/api/auth/oauth/" + sso.ProviderID + "?redirect_to=" + next)
	callback := get(authorize + "&login_hint=" + email)
	// The callback URL names the public URL. The test server listens on
	// another address.
	if !strings.HasPrefix(callback, e.Config.BaseURL) {
		e.t.Fatalf("the IdP returned to %s", callback)
	}
	landing := get(e.HTTP.URL + strings.TrimPrefix(callback, e.Config.BaseURL))
	return c, strings.TrimPrefix(landing, e.Config.BaseURL)
}
