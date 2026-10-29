package app

import (
	"net/http"
	"strings"

	"github.com/alternayte/auth-all/humaauth"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/alternayte/assay/internal/api"
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
}

func authRouteAllowed(method, path string) bool {
	if authRoutes[method+" "+path] {
		return true
	}
	if method == http.MethodDelete && strings.HasPrefix(path, "/sessions/") && !strings.Contains(path[len("/sessions/"):], "/") {
		return true
	}
	return false
}

// newAPI builds the huma API on r and merges the allowed auth-all operations
// into its document.
func (s *Server) newAPI(r chi.Router) (huma.API, error) {
	api.Install()
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

// authHandler serves the allowed auth-all routes.
func (s *Server) authHandler() http.Handler {
	h := s.auth.Handler()
	base := s.auth.BasePath()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, base)
		if !authRouteAllowed(r.Method, rel) {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}
