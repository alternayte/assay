package app

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// spa serves the web build. A path that is not a file gets index.html, so
// that the client router handles it. /setup answers 404 after the first
// admin exists (SI-14).
func (s *Server) spa(files fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, err
	}
	static := http.FileServerFS(files)
	serveIndex := func(w http.ResponseWriter, r *http.Request, status int) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = w.Write(index)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		setSecurityHeaders(w)
		p := path.Clean(r.URL.Path)
		if strings.HasPrefix(p, "/api/") || p == "/api" {
			http.NotFound(w, r)
			return
		}
		if p == "/setup" {
			done, err := s.users.SetupDone(r.Context())
			if err != nil {
				http.Error(w, "database unavailable", http.StatusServiceUnavailable)
				return
			}
			if done {
				serveIndex(w, r, http.StatusNotFound)
				return
			}
		}
		name := strings.TrimPrefix(p, "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(files, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				static.ServeHTTP(w, r)
				return
			}
		}
		serveIndex(w, r, http.StatusOK)
	}), nil
}

func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
}

