package app

import (
	"net/http"
	"net/url"
	"strings"

	"riverqueue.com/riverui"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
)

// JobsPath is the prefix of River UI, the job queue page for admins.
const JobsPath = "/admin/jobs"

func (s *Server) newJobsUI() (*riverui.Handler, error) {
	return riverui.NewHandler(&riverui.HandlerOpts{
		Endpoints: riverui.NewEndpoints(s.river, nil),
		Logger:    s.log.With("component", "riverui"),
		Prefix:    JobsPath,
	})
}

// adminOnly admits an admin with a session. An access token never reaches
// the page, because River UI can cancel, retry and delete jobs. A browser
// without a session goes to sign-in and comes back.
func adminOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := api.Caller(r.Context())
		switch {
		case p == nil && r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, JobsPath+"/api/"):
			http.Redirect(w, r, "/sign-in?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		case p == nil:
			http.Error(w, "Sign in to continue.", http.StatusUnauthorized)
		case p.APIKey != nil || p.User.MustChangePassword || api.Role(p) != auth.RoleAdmin:
			http.Error(w, "Only an admin can open the job queue.", http.StatusForbidden)
		default:
			setSecurityHeaders(w)
			h.ServeHTTP(w, r)
		}
	})
}
