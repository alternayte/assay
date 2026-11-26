package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	authall "github.com/alternayte/auth-all"
	"github.com/alternayte/auth-all/plugins/oauthprovider"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/settings"
)

// EnvOAuth turns OAuth sign-in of MCP clients on or off. A set value wins
// over the setting, and Settings shows the field as locked.
const EnvOAuth = "ASSAY_MCP_OAUTH"

// CheckEnv validates the value of EnvOAuth. An empty value is not set.
func CheckEnv(v string) error {
	if v == "" {
		return nil
	}
	if _, err := strconv.ParseBool(v); err != nil {
		return errors.New(EnvOAuth + " is \"" + v + "\". Set it to true or false")
	}
	return nil
}

// OAuthConfig is the agents setting.
type OAuthConfig struct {
	MCPOAuth bool `json:"mcpOAuth" doc:"True lets MCP clients sign in with OAuth."`
}

// OAuthSetting is the definition of the agents setting. The app sets its
// Guard to Service.Guard.
var OAuthSetting = settings.Define("agents", "MCP clients that sign in with OAuth.", OAuthConfig{MCPOAuth: true}, nil)

// Guard refuses a change of the agents setting while EnvOAuth sets it.
func (s *Service) Guard(context.Context, settings.DBTX, json.RawMessage) error {
	if s.OAuthEnv != "" {
		return api.Problem(http.StatusConflict, "locked", EnvOAuth+" sets this setting.")
	}
	return nil
}

// OAuthOn reports whether MCP clients can sign in with OAuth.
func (s *Service) OAuthOn(ctx context.Context) (bool, error) {
	if s.OAuthEnv != "" {
		on, _ := strconv.ParseBool(s.OAuthEnv)
		return on, nil
	}
	c, err := settings.Read[OAuthConfig](ctx, s.Pool, OAuthSetting)
	return c.MCPOAuth, err
}

// resourceMetadataURL is the RFC 9728 metadata URL of /mcp.
func (s *Service) resourceMetadataURL() string {
	u, _ := oauthprovider.ResourceMetadataURL(s.Auth.MCPResource)
	return u
}

// unauthorized answers a request to /mcp without a valid token. With OAuth
// on, the challenge names the resource metadata, so that an MCP client finds
// the authorization server. The body is the Assay error envelope.
func (s *Service) unauthorized(w http.ResponseWriter, oauth bool) {
	if oauth {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.resourceMetadataURL()+`"`)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer realm="assay"`)
	}
	writeEnvelope(w, http.StatusUnauthorized)
}

// writeEnvelope writes the Assay error envelope of a refused MCP request.
func writeEnvelope(w http.ResponseWriter, status int) {
	e := api.Problem(http.StatusUnauthorized, "unauthorized", "Send a valid access token as a bearer token.")
	if status == http.StatusForbidden {
		e = api.Problem(http.StatusForbidden, "forbidden", "The access token does not have the scope for this request.")
	}
	b, _ := json.Marshal(e)
	w.Header().Del("Content-Length")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// envelopeWriter replaces the body of an auth-all challenge with the Assay
// error envelope. It keeps the WWW-Authenticate header.
type envelopeWriter struct {
	http.ResponseWriter
	swallow bool
}

func (e *envelopeWriter) WriteHeader(status int) {
	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && e.Header().Get("WWW-Authenticate") != "" {
		e.swallow = true
		writeEnvelope(e.ResponseWriter, status)
		return
	}
	e.ResponseWriter.WriteHeader(status)
}

func (e *envelopeWriter) Write(b []byte) (int, error) {
	if e.swallow {
		return len(b), nil
	}
	return e.ResponseWriter.Write(b)
}

func (e *envelopeWriter) Flush() {
	if f, ok := e.ResponseWriter.(http.Flusher); ok && !e.swallow {
		f.Flush()
	}
}

func (e *envelopeWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// bearerToken returns the bearer or DPoP credential of a request.
func bearerToken(r *http.Request) string {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "bearer") && !strings.EqualFold(scheme, "dpop") {
		return ""
	}
	return strings.TrimSpace(token)
}

// protect returns the /mcp handler around next. An OAuth access token for
// the /mcp resource reaches next with its principal. Every other request goes
// to next as LoadSession left it, so access tokens keep working. With OAuth
// off, an OAuth access token gets 401.
func (s *Service) protect(next http.Handler) http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := authall.PrincipalFrom(r.Context())
		switch {
		case p != nil && p.APIKey != nil:
		case p != nil && p.Method == authall.MethodOAuth && p.OAuth != nil && p.OAuth.Audience == s.Auth.MCPResource:
		default:
			on, err := s.OAuthOn(r.Context())
			if err != nil {
				s.Log.Error("read the agents setting", "error", err.Error())
			}
			s.unauthorized(w, on)
			return
		}
		next.ServeHTTP(w, r)
	})
	guarded := s.Auth.OAuth.RequireAudience(s.Auth.MCPResource, inner, oauthprovider.RequireAudienceOptions{Fallback: inner})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on, err := s.OAuthOn(r.Context())
		if err != nil {
			s.Log.Error("read the agents setting", "error", err.Error())
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !on {
			if t := bearerToken(r); t != "" && s.Auth.OAuth.Claims(t) {
				s.unauthorized(w, false)
				return
			}
			inner.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(&envelopeWriter{ResponseWriter: w}, r)
	})
}

// MetadataHandler serves the authorization server metadata at the origin
// root. With OAuth off, the document names no registration endpoint and no
// metadata document support.
func (s *Service) MetadataHandler() http.Handler {
	h := s.Auth.OAuth.MetadataHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on, err := s.OAuthOn(r.Context())
		if err != nil {
			s.Log.Error("read the agents setting", "error", err.Error())
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if on {
			h.ServeHTTP(w, r)
			return
		}
		rec := &recorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		body := rec.body.Bytes()
		if rec.status == http.StatusOK {
			var doc map[string]any
			if json.Unmarshal(body, &doc) == nil {
				delete(doc, "registration_endpoint")
				delete(doc, "client_id_metadata_document_supported")
				body, _ = json.Marshal(doc)
			}
		}
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(rec.statusOr())
		_, _ = w.Write(body)
	})
}

// ProtectedResourceHandler serves the RFC 9728 metadata of /mcp. With OAuth
// off, it answers 404.
func (s *Service) ProtectedResourceHandler() http.Handler {
	h := s.Auth.OAuth.ProtectedResourceHandler(s.Auth.MCPResource)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on, err := s.OAuthOn(r.Context())
		if err != nil {
			s.Log.Error("read the agents setting", "error", err.Error())
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !on {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ServeAuthRoute serves an allowed auth-all route of the OAuth provider at
// rel, the path under the auth-all base path. With OAuth off, the authorize
// and registration routes do not exist. An authorization request must name
// the /mcp resource, so that no access token opens another route. The client
// that the consent page reads is recorded for Connected apps and the audit
// log.
func (s *Service) ServeAuthRoute(w http.ResponseWriter, r *http.Request, rel string, h http.Handler) {
	authorize := r.Method == http.MethodGet && rel == oauthprovider.PathAuthorize
	if authorize || (r.Method == http.MethodPost && rel == oauthprovider.PathRegister) {
		on, err := s.OAuthOn(r.Context())
		if err != nil {
			s.Log.Error("read the agents setting", "error", err.Error())
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !on {
			http.NotFound(w, r)
			return
		}
	}
	if authorize {
		if res := r.URL.Query()["resource"]; len(res) != 1 || res[0] != s.Auth.MCPResource {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_target",
				"error_description": "the request must name the resource " + s.Auth.MCPResource})
			return
		}
	}
	if r.Method != http.MethodGet || rel != oauthprovider.PathRequest {
		h.ServeHTTP(w, r)
		return
	}
	rec := &recorder{header: http.Header{}}
	h.ServeHTTP(rec, r)
	if rec.statusOr() == http.StatusOK {
		var req struct {
			ClientID   string `json:"clientId"`
			ClientName string `json:"clientName"`
			ClientKind string `json:"clientKind"`
			ClientHost string `json:"clientHost"`
		}
		if json.Unmarshal(rec.body.Bytes(), &req) == nil && req.ClientID != "" {
			if err := New(s.Pool).UpsertClient(r.Context(), UpsertClientParams{ClientID: req.ClientID, Name: req.ClientName,
				Host: req.ClientHost, Kind: req.ClientKind, UpdatedAt: s.now()}); err != nil {
				s.Log.Error("record the MCP client", "error", err.Error())
			}
		}
	}
	for k, v := range rec.header {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.statusOr())
	_, _ = w.Write(rec.body.Bytes())
}

// recorder keeps a response in memory.
type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}

func (r *recorder) statusOr() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// hostOf returns the host of an HTTP or HTTPS URL, and "" for anything else.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.Host
}

// clientHost returns the host of a client: the host of its metadata
// document URL, the host that the consent page showed, or the host of its
// first redirect URI.
func (s *Service) clientHost(ctx context.Context, clientID string) string {
	if h := hostOf(clientID); h != "" {
		return h
	}
	v, err := New(s.Pool).ClientHost(ctx, clientID)
	if err != nil {
		s.Log.Error("read the MCP client host", "error", err.Error())
		return ""
	}
	if strings.Contains(v, "://") {
		return hostOf(v)
	}
	return v
}

// RotationAge is the age at which the signing key of the OAuth access tokens
// rotates.
const RotationAge = 90 * 24 * time.Hour

// KeyArgs is the daily job that rotates the OAuth signing key when it is
// RotationAge old and deletes the spent OAuth rows. A check of the key age,
// not a 90-day timer, survives restarts.
type KeyArgs struct{}

// Kind is the River job kind.
func (KeyArgs) Kind() string { return "oauth_keys" }

// KeyWorker works KeyArgs.
type KeyWorker struct {
	river.WorkerDefaults[KeyArgs]
	S *Service
}

// Work rotates the key when it is due.
func (w *KeyWorker) Work(ctx context.Context, _ *river.Job[KeyArgs]) error {
	return w.S.RotateIfDue(ctx)
}

// RotateIfDue rotates the signing key when it is RotationAge old, or when the
// key from ASSAY_SECRET_KEY cannot read it, and deletes the spent and expired
// OAuth rows. A deployment with no key yet gets its first key at the first
// token.
func (s *Service) RotateIfDue(ctx context.Context) error {
	created, err := New(s.Pool).NewestSigningKey(ctx)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return err
	case !s.signingKeyReadable(ctx):
		// A new ASSAY_SECRET_KEY gives a new key encryption key. auth-all then
		// cannot read the stored key and issues no token until a new key
		// exists.
		if err := s.Auth.OAuth.Rotate(ctx); err != nil {
			return err
		}
		s.Log.Warn("oauth signing key replaced: ASSAY_SECRET_KEY cannot read the stored key. Connected MCP clients must connect again")
	case s.now().Sub(created) >= RotationAge:
		if err := s.Auth.OAuth.Rotate(ctx); err != nil {
			return err
		}
		s.Log.Info("oauth signing key rotated", "previous_created_at", created)
	}
	n, err := s.Auth.OAuth.Cleanup(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		s.Log.Info("oauth rows deleted", "rows", n)
	}
	return nil
}

// signingKeyReadable reports whether auth-all can sign with the stored key.
// The key set route signs nothing, but it loads the signing key first.
func (s *Service) signingKeyReadable(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Auth.BasePath()+oauthprovider.PathJWKS, nil)
	if err != nil {
		return false
	}
	rec := &recorder{header: http.Header{}}
	s.Auth.Handler().ServeHTTP(rec, req)
	return rec.statusOr() == http.StatusOK
}

// PeriodicJobs returns the daily key job.
func (s *Service) PeriodicJobs() []*river.PeriodicJob {
	return []*river.PeriodicJob{river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour), func() (river.JobArgs, *river.InsertOpts) {
		return KeyArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByPeriod: 24 * time.Hour}}
	}, &river.PeriodicJobOpts{RunOnStart: true, ID: "oauth_keys"})}
}
