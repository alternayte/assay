package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/alternayte/auth-all/oauth"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"riverqueue.com/riverui"

	"github.com/alternayte/assay/internal/agents"
	"github.com/alternayte/assay/internal/ask"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/design"
	"github.com/alternayte/assay/internal/doctor"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/extract"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/pages"
	"github.com/alternayte/assay/internal/registry"
	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/review"
	"github.com/alternayte/assay/internal/search"
	"github.com/alternayte/assay/internal/secrets"
	"github.com/alternayte/assay/internal/settings"
	"github.com/alternayte/assay/internal/sso"
	syncer "github.com/alternayte/assay/internal/sync"
	"github.com/alternayte/assay/internal/tokens"
	"github.com/alternayte/assay/internal/users"
	"github.com/alternayte/assay/internal/visibility"
	"github.com/alternayte/assay/internal/vocab"
	"github.com/alternayte/assay/web"
)

// Server is one Assay process.
type Server struct {
	cfg        Config
	log        *slog.Logger
	pool       *pgxpool.Pool
	auth       *auth.Auth
	users      *users.Service
	sso        *sso.Service
	settings   *settings.Service
	secrets    *secrets.Store
	mailer     *settings.Mailer
	models     *models.Gateway
	doctor     *doctor.Doctor
	sync       *syncer.Service
	embedder   *content.Embedder
	search     *search.Service
	ask        *ask.Service
	entities   *entities.Service
	extract    *extract.Service
	resolve    *resolve.Service
	registry   *registry.Service
	vocab      *vocab.Service
	pages      *pages.Service
	design     *design.Service
	review     *review.Service
	tokens     *tokens.Service
	agents     *agents.Service
	river      *river.Client[pgx.Tx]
	metrics    *metrics
	tracer     *sdktrace.TracerProvider
	handler    http.Handler
	spaHandler http.Handler
	jobsUI     *riverui.Handler
	now        func() time.Time
	opts       Options
	addr       chan string
}

// Options change defaults for tests.
type Options struct {
	Auth auth.Options
	// Now is the clock. Nil means time.Now.
	Now func() time.Time

	// workers adds job workers. Only package tests set it.
	workers func(*river.Workers)
	// jobGrace is the time that running jobs get to finish at shutdown.
	jobGrace time.Duration
}

// Open connects to the database, builds auth-all and applies the
// migrations.
func Open(ctx context.Context, cfg Config, log *slog.Logger, opts auth.Options) (*pgxpool.Pool, *auth.Auth, error) {
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("database: %w", err)
	}
	a, err := auth.New(pool, cfg.BaseURL, cfg.SecretKey, log, opts)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	if err := Migrate(ctx, pool, a, log); err != nil {
		pool.Close()
		return nil, nil, err
	}
	return pool, a, nil
}

// New opens the database and builds the HTTP handler.
func New(ctx context.Context, cfg Config, log *slog.Logger, opts Options) (*Server, error) {
	// auth-all reads the SSO provider of each request from the settings.
	var s *Server
	authOpts := opts.Auth
	authOpts.Providers = func(ctx context.Context) ([]oauth.Provider, error) {
		if s == nil || s.sso == nil {
			return nil, nil
		}
		return s.sso.Providers(ctx)
	}
	pool, a, err := Open(ctx, cfg, log, authOpts)
	if err != nil {
		return nil, err
	}
	s = &Server{cfg: cfg, log: log, pool: pool, auth: a, now: opts.Now, opts: opts}
	if s.now == nil {
		s.now = time.Now
	}
	if err := s.build(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

// services builds the feature services. It does not touch the database.
func (s *Server) services() error {
	box, err := secrets.NewBox(s.cfg.SecretKey)
	if err != nil {
		return err
	}
	s.secrets = &secrets.Store{Pool: s.pool, Box: box}
	s.sso = &sso.Service{Pool: s.pool, Secrets: s.secrets, Auth: s.auth, Outbound: s.cfg.Outbound, Env: s.cfg.SSO, BaseURL: s.cfg.BaseURL,
		Log: s.log, Now: s.now}
	signIn := sso.Setting
	signIn.Guard = s.sso.Guard
	s.agents = &agents.Service{Pool: s.pool, Auth: s.auth, BaseURL: s.cfg.BaseURL, Version: Version, OAuthEnv: s.cfg.MCPOAuth, Log: s.log,
		Now: s.now}
	agentsSetting := agents.OAuthSetting
	agentsSetting.Guard = s.agents.Guard
	s.settings = &settings.Service{Pool: s.pool, Now: s.now, Defs: []settings.Def{settings.SMTP, Retention, syncer.Rules, claims.PrioritySetting, resolve.TermSetting,
		pages.Setting, design.Setting, signIn, agentsSetting}}
	s.mailer = &settings.Mailer{Service: s.settings, Secrets: s.secrets}
	s.users = &users.Service{Pool: s.pool, Auth: s.auth, BaseURL: s.cfg.BaseURL, SetupToken: s.cfg.SetupToken, Now: s.now, Mail: s.mailer.Send,
		AdminStatus: s.sso.AdminStatus}
	s.sso.Install(s.auth)
	s.models = &models.Gateway{Pool: s.pool, Secrets: s.secrets, Outbound: s.cfg.Outbound, Now: s.now}
	s.sync = &syncer.Service{Pool: s.pool, Secrets: s.secrets, Outbound: s.cfg.Outbound, Log: s.log, Now: s.now, Metrics: syncer.NewMetrics(),
		DemoURL: s.cfg.DemoURL}
	s.embedder = &content.Embedder{Pool: s.pool, Models: s.models, Log: s.log, Shards: orDefault(s.cfg.EmbedShards, 4)}
	s.vocab = &vocab.Service{Pool: s.pool}
	s.search = &search.Service{Pool: s.pool, Models: s.models, Vocab: s.vocab, Log: s.log}
	s.entities = &entities.Service{Pool: s.pool, Now: s.now}
	s.extract = &extract.Service{Pool: s.pool, Models: s.models, Log: s.log, Metrics: extract.NewMetrics()}
	s.registry = &registry.Service{Pool: s.pool, Log: s.log, Now: s.now}
	s.entities.Rename = s.registry.Rename
	s.review = &review.Service{Pool: s.pool, Now: s.now}
	s.resolve = &resolve.Service{Pool: s.pool, Models: s.models, Entities: s.entities, Log: s.log, Now: s.now,
		Apply: map[string]resolve.ApplyFunc{"term_edit": s.registry.ApplyTermEdit, "new_term": s.registry.ApplyNewTerm,
			"correction": s.review.ApplyCorrection}, CreateSystem: s.registry.CreateSystem}
	s.review.Resolve = s.resolve
	s.tokens = &tokens.Service{Pool: s.pool, Keys: s.auth.Keys, Now: s.now}
	s.pages = &pages.Service{Pool: s.pool, Models: s.models, Entities: s.entities, Vocab: s.vocab, Extract: s.extract, Log: s.log, Now: s.now}
	s.design = &design.Service{Pool: s.pool, Models: s.models, Entities: s.entities, Vocab: s.vocab, Resolve: s.resolve, Log: s.log, Now: s.now}
	s.resolve.Apply["design_edit"] = s.design.ApplyEdit
	s.resolve.Apply["design_problem"] = s.design.ApplyProblem
	s.resolve.Apply["design_update"] = s.design.ApplyUpdate
	s.sync.AfterRun = s.design.QueueBuild
	s.resolve.Apply["needs_edit"] = s.pages.ApplyEdit
	s.resolve.Apply["entity_edit"] = func(ctx context.Context, tx pgx.Tx, vis visibility.Set, payload []byte, _, reviewer string, _ resolve.Edits) error {
		return s.entities.ApplyEdit(ctx, tx, vis, payload, reviewer)
	}
	s.entities.Edited = s.pages.Invalidate
	s.ask = &ask.Service{Pool: s.pool, Models: s.models, Search: s.search, Extract: s.extract, Vocab: s.vocab, Log: s.log, Now: s.now,
		Orientation: s.pages.Orientation}
	s.agents.Ask, s.agents.Entities, s.agents.Pages, s.agents.Design = s.ask, s.entities, s.pages, s.design
	s.agents.Vocab, s.agents.Review = s.vocab, s.review
	if s.cfg.Demo {
		s.users.OnSetup = func(ctx context.Context, adminID string) error { return s.sync.Demo(ctx, adminID, s.cfg.DemoURL) }
	}
	s.doctor = &doctor.Doctor{Pool: s.pool, Secrets: s.secrets, Models: s.models, Sync: s.sync, Outbound: s.cfg.Outbound}
	return nil
}

// registerAPI adds the routes of every feature.
func (s *Server) registerAPI(a huma.API) {
	s.users.Register(a)
	s.sso.Register(a)
	s.settings.Register(a)
	s.mailer.RegisterSMTP(a)
	audit.Register(a, s.pool)
	s.models.Register(a)
	s.doctor.Register(a)
	s.sync.Register(a)
	content.Register(a, s.pool)
	s.search.Register(a)
	s.ask.Register(a)
	s.entities.Register(a)
	s.extract.Register(a)
	s.resolve.Register(a)
	s.review.Register(a)
	s.tokens.Register(a)
	s.agents.Register(a)
	s.registry.Register(a)
	vocab.RegisterDomains(a, s.pool)
	s.vocab.Register(a)
	s.pages.Register(a, s.cfg.BaseURL)
	s.design.Register(a, s.cfg.BaseURL)
}

func (s *Server) build(ctx context.Context) error {
	if err := s.services(); err != nil {
		return err
	}
	if err := s.settings.Seed(ctx); err != nil {
		return err
	}
	var err error
	if s.river, err = s.newRiver(); err != nil {
		return err
	}
	s.sync.River = s.river
	s.sso.River = s.river
	s.extract.River = s.river
	s.design.River = s.river
	s.metrics = newMetrics(s.pool, append(s.sync.Metrics.Collectors(), s.extract.Metrics.Collectors()...)...)
	if s.tracer, err = tracing(ctx); err != nil {
		return err
	}
	h, err := s.routes()
	if err != nil {
		return err
	}
	if s.tracer != nil {
		s.handler = traced(h, s.tracer)
	} else {
		s.handler = h
	}
	return nil
}

// Handler is the HTTP handler of the process.
func (s *Server) Handler() http.Handler { return s.handler }

// Doctor runs the installation checks.
func (s *Server) Doctor() *doctor.Doctor { return s.doctor }

// Models is the model gateway.
func (s *Server) Models() *models.Gateway { return s.models }

// Extract extracts claims from passages.
func (s *Server) Extract() *extract.Service { return s.extract }

// Ask answers questions.
func (s *Server) Ask() *ask.Service { return s.ask }

// Registry returns the registry service.
func (s *Server) Registry() *registry.Service { return s.registry }

// Resolve returns the resolution service.
func (s *Server) Resolve() *resolve.Service { return s.resolve }

// Embedder fills the embedding spaces.
func (s *Server) Embedder() *content.Embedder { return s.embedder }

// Design builds the design pages.
func (s *Server) Design() *design.Service { return s.design }

// SSO serves sign-in with the identity provider and the directory sync.
func (s *Server) SSO() *sso.Service { return s.sso }

// Sync runs the connections.
func (s *Server) Sync() *syncer.Service { return s.sync }

// Auth is the auth-all instance.
func (s *Server) Auth() *auth.Auth { return s.auth }

// Pool is the database pool.
func (s *Server) Pool() *pgxpool.Pool { return s.pool }

// Close flushes the spans and releases the database pool.
func (s *Server) Close() {
	if s.tracer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.tracer.Shutdown(ctx)
	}
	s.pool.Close()
}

func (s *Server) routes() (http.Handler, error) {
	r := chi.NewRouter()
	r.Use(s.metrics.middleware)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/readyz", s.ready)
	r.Handle("/metrics", s.metrics.handler())
	if s.cfg.Role == RoleWorker {
		return r, nil
	}
	r.Handle(s.auth.BasePath()+"/*", s.authHandler())
	// RFC 8414 and RFC 9728 put the OAuth metadata at the origin root.
	metadata := s.agents.MetadataHandler()
	r.Handle("/.well-known/oauth-authorization-server", metadata)
	r.Handle("/.well-known/oauth-authorization-server/*", metadata)
	r.Handle("/.well-known/openid-configuration", metadata)
	r.Handle("/.well-known/openid-configuration/*", metadata)
	resource := s.agents.ProtectedResourceHandler()
	r.Handle("/.well-known/oauth-protected-resource", resource)
	r.Handle("/.well-known/oauth-protected-resource/*", resource)

	spa, err := s.spa(web.Dist())
	if err != nil {
		return nil, err
	}
	if s.jobsUI, err = s.newJobsUI(); err != nil {
		return nil, err
	}
	s.spaHandler = spa
	var apiErr error
	r.Group(func(g chi.Router) {
		g.Use(s.auth.LoadSession)
		a, err := s.newAPI(g)
		if err != nil {
			apiErr = err
			return
		}
		s.registerAPI(a)
		g.Handle(JobsPath, http.RedirectHandler(JobsPath+"/", http.StatusMovedPermanently))
		g.Handle(JobsPath+"/*", adminOnly(s.jobsUI))
		// MCP for agents with an access token or an OAuth grant
		// (REQ-AGT-001).
		g.Handle("/mcp", s.agents.Handler())
		g.Get(pages.GlossaryPath, func(w http.ResponseWriter, req *http.Request) {
			r2 := req.Clone(context.WithValue(req.Context(), chi.RouteCtxKey, nil))
			r2.URL.Path, r2.URL.RawPath = "/api/v1/glossary.md", ""
			r.ServeHTTP(w, r2)
		})
		// /e/{key}.md is the Markdown form of a page (REQ-PAG-005), and
		// /e/{key}/design.md the form of a design page. The SPA serves
		// /e/{key}.
		g.Get("/e/*", func(w http.ResponseWriter, req *http.Request) {
			p, ok := pages.MarkdownPath(req.URL.EscapedPath())
			if !ok {
				p, ok = design.MarkdownPath(req.URL.EscapedPath())
			}
			if !ok {
				s.spaHandler.ServeHTTP(w, req)
				return
			}
			// A new routing context sends the request through the API route.
			r2 := req.Clone(context.WithValue(req.Context(), chi.RouteCtxKey, nil))
			r2.URL.RawPath = p
			r2.URL.Path, _ = url.PathUnescape(p)
			r.ServeHTTP(w, r2)
		})
	})
	if apiErr != nil {
		return nil, apiErr
	}
	r.NotFound(s.spaHandler.ServeHTTP)
	return r, nil
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := s.pool.Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("database unavailable\n"))
		return
	}
	current, err := MigrationsCurrent(ctx, s.pool)
	if err != nil || !current {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("migrations pending\n"))
		return
	}
	_, _ = w.Write([]byte("ready\n"))
}

// RunOn is Run that sends the listen address on addr once it listens.
func (s *Server) RunOn(ctx context.Context, addr chan string) error {
	s.addr = addr
	return s.Run(ctx)
}

// Run serves HTTP and works jobs until ctx ends. Then it stops new work,
// finishes open requests, releases running jobs and returns within 30
// seconds.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
	if s.jobsUI != nil {
		if err := s.jobsUI.Start(ctx); err != nil {
			_ = ln.Close()
			return err
		}
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	if s.cfg.Role != RoleWeb {
		if err := s.river.Start(ctx); err != nil {
			_ = srv.Close()
			return err
		}
	}
	s.log.Info("listening", "address", ln.Addr().String(), "role", s.cfg.Role)
	if s.addr != nil {
		s.addr <- ln.Addr().String()
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	s.log.Info("shutting down")
	base := context.WithoutCancel(ctx)
	done := make(chan error, 2)
	go func() {
		c, cancel := context.WithTimeout(base, 25*time.Second)
		defer cancel()
		err := srv.Shutdown(c)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	go func() {
		if s.cfg.Role == RoleWeb {
			done <- nil
			return
		}
		// Running jobs get 20 seconds to finish. Then River cancels them,
		// and they return to the queue.
		grace := 20 * time.Second
		if s.opts.jobGrace > 0 {
			grace = s.opts.jobGrace
		}
		soft, cancel := context.WithTimeout(base, grace)
		defer cancel()
		if err := s.river.Stop(soft); err == nil {
			done <- nil
			return
		}
		hard, cancel2 := context.WithTimeout(base, 5*time.Second)
		defer cancel2()
		done <- s.river.StopAndCancel(hard)
	}()
	return errors.Join(<-done, <-done)
}
