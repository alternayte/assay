package app

import (
	"net/http"
	"strings"

	"github.com/alternayte/auth-all/humaauth"
	"github.com/alternayte/auth-all/plugins/oauthprovider"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	_ "github.com/alternayte/assay/internal/api" // Sets the huma error envelope.
)

// Version is the build version. cmd/assay sets it.
var Version = "dev"

// authRoutes are the auth-all routes that Assay serves (D-103). Others
// return 404 and are absent from the OpenAPI document.
var authRoutes = map[string]bool{
	"POST /sign-in/email":       true,
	"POST /sign-out":            true,
	"GET /session":              true,
	"GET /sessions":             true,
	"DELETE /sessions/{id}":     true,
	"POST /sessions/revoke-all": true,
	"POST /password/change":     true,
	// Sign-in with the identity provider. The provider ID is fixed.
	"GET /oauth/sso":          true,
	"GET /oauth/sso/callback": true,
	// The OAuth authorization server of MCP clients. The routes of owned
	// clients and of RFC 7592 client management stay closed.
	"GET " + oauthprovider.PathAuthorize: true,
	"POST " + oauthprovider.PathToken:    true,
	"POST " + oauthprovider.PathRevoke:   true,
	"POST " + oauthprovider.PathRegister: true,
	"GET " + oauthprovider.PathJWKS:      true,
	"GET " + oauthprovider.PathUserInfo:  true,
	"POST " + oauthprovider.PathUserInfo: true,
	"GET " + oauthprovider.PathRequest:   true,
	"POST " + oauthprovider.PathDecide:   true,
	"GET " + oauthprovider.PathConsents:  true,
}

// authRouteAllowed reports whether Assay serves the auth-all route. path is
// the escaped path under the base path, so that an escaped slash stays in
// its segment.
func authRouteAllowed(method, path string) bool {
	if authRoutes[method+" "+path] {
		return true
	}
	if method != http.MethodDelete {
		return false
	}
	// Sign out one session, and withdraw the consent of one client.
	for _, prefix := range []string{"/sessions/", oauthprovider.PathConsents + "/"} {
		if rest, ok := strings.CutPrefix(path, prefix); ok && rest != "" && !strings.Contains(rest, "/") {
			return true
		}
	}
	return false
}

// newAPI builds the huma API on r and merges the allowed auth-all operations
// into its document.
func (s *Server) newAPI(r chi.Router) (huma.API, error) {
	cfg := huma.DefaultConfig("Assay", Version)
	cfg.OpenAPIPath = "/api/v1/openapi"
	cfg.DocsPath = "/api/docs"
	cfg.SchemasPath = "/api/v1/schemas"
	cfg.Info.Description = "The Assay REST API. Requests need a session cookie or a bearer token."
	cfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearer": {Type: "http", Scheme: "bearer"},
	}
	cfg.Security = []map[string][]string{{"bearer": {}}}
	a := humachi.New(r, cfg)
	if err := humaauth.Register(a, s.auth.Auth); err != nil {
		return nil, err
	}
	base := s.auth.BasePath()
	for path, item := range a.OpenAPI().Paths {
		rel, ok := strings.CutPrefix(path, base)
		if !ok {
			continue
		}
		for method, op := range map[string]**huma.Operation{
			http.MethodGet: &item.Get, http.MethodPost: &item.Post, http.MethodPut: &item.Put,
			http.MethodPatch: &item.Patch, http.MethodDelete: &item.Delete,
		} {
			if *op != nil && !authRouteAllowed(method, rel) {
				*op = nil
			}
		}
		if item.Get == nil && item.Post == nil && item.Put == nil && item.Patch == nil && item.Delete == nil {
			delete(a.OpenAPI().Paths, path)
		}
	}
	return a, nil
}

// newHookAPI builds the huma API of the webhook route on r. It serves no
// document: the route is for source systems, not for API clients.
func newHookAPI(r chi.Router) huma.API {
	cfg := huma.DefaultConfig("Assay webhooks", Version)
	cfg.OpenAPIPath, cfg.DocsPath, cfg.SchemasPath = "", "", ""
	cfg.CreateHooks = nil
	return humachi.New(r, cfg)
}

// authHandler serves the allowed auth-all routes.
func (s *Server) authHandler() http.Handler {
	h := s.auth.Handler()
	base := s.auth.BasePath()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, base)
		if !authRouteAllowed(r.Method, strings.TrimPrefix(r.URL.EscapedPath(), base)) {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(rel, "/oauth2/") {
			s.agents.ServeAuthRoute(w, r, rel, h)
			return
		}
		// With password sign-in off, the password route does not exist.
		if r.Method == http.MethodPost && rel == "/sign-in/email" {
			on, err := s.sso.PasswordOn(r.Context())
			if err != nil {
				s.log.Error("read the sign-in setting", "error", err.Error())
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if !on {
				http.NotFound(w, r)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
