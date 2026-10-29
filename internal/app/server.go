package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/doctor"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/search"
	"github.com/alternayte/assay/internal/secrets"
	"github.com/alternayte/assay/internal/settings"
	syncer "github.com/alternayte/assay/internal/sync"
	"github.com/alternayte/assay/internal/users"
	"github.com/alternayte/assay/internal/vocab"
	"github.com/alternayte/assay/web"
)

// Server is one Assay process.
type Server struct {
	cfg      Config
	log      *slog.Logger
	pool     *pgxpool.Pool
	auth     *auth.Auth
	users    *users.Service
	settings *settings.Service
	secrets  *secrets.Store
	mailer   *settings.Mailer
	models   *models.Gateway
	doctor   *doctor.Doctor
	sync     *syncer.Service
	embedder *content.Embedder
	search   *search.Service
	river    *river.Client[pgx.Tx]
	metrics  *metrics
	tracer   *sdktrace.TracerProvider
	handler  http.Handler
	now      func() time.Time
	opts     Options
	addr     chan string
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
	a, err := auth.New(pool, cfg.BaseURL, log, opts)
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
	pool, a, err := Open(ctx, cfg, log, opts.Auth)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: log, pool: pool, auth: a, now: opts.Now, opts: opts}
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
	s.settings = &settings.Service{Pool: s.pool, Now: s.now, Defs: []settings.Def{settings.SMTP, Retention, syncer.Rules}}
	s.mailer = &settings.Mailer{Service: s.settings, Secrets: s.secrets}
	s.users = &users.Service{Pool: s.pool, Auth: s.auth, BaseURL: s.cfg.BaseURL, SetupToken: s.cfg.SetupToken, Now: s.now, Mail: s.mailer.Send}
	s.models = &models.Gateway{Pool: s.pool, Secrets: s.secrets, Outbound: s.cfg.Outbound, Now: s.now}
	s.sync = &syncer.Service{Pool: s.pool, Secrets: s.secrets, Outbound: s.cfg.Outbound, Log: s.log, Now: s.now, Metrics: syncer.NewMetrics()}
	s.embedder = &content.Embedder{Pool: s.pool, Models: s.models, Log: s.log}
	s.search = &search.Service{Pool: s.pool, Models: s.models, Log: s.log}
	s.doctor = &doctor.Doctor{Pool: s.pool, Secrets: s.secrets, Models: s.models, Sync: s.sync, Outbound: s.cfg.Outbound}
	return nil
}

// registerAPI adds the routes of every feature.
func (s *Server) registerAPI(a huma.API) {
	s.users.Register(a)
	s.settings.Register(a)
	s.mailer.RegisterSMTP(a)
	audit.Register(a, s.pool)
	s.models.Register(a)
	s.doctor.Register(a)
	s.sync.Register(a)
	content.Register(a, s.pool)
	s.search.Register(a)
	vocab.RegisterDomains(a, s.pool)
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
	s.metrics = newMetrics(s.pool, s.sync.Metrics.Collectors()...)
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

// Embedder fills the embedding spaces.
func (s *Server) Embedder() *content.Embedder { return s.embedder }

// Sync runs the connections.
func (s *Server) Sync() *syncer.Service { return s.sync }

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

	var apiErr error
	r.Group(func(g chi.Router) {
		g.Use(s.auth.LoadSession)
		a, err := s.newAPI(g)
		if err != nil {
			apiErr = err
			return
		}
		s.registerAPI(a)
	})
	if apiErr != nil {
		return nil, apiErr
	}
	spa, err := s.spa(web.Dist())
	if err != nil {
		return nil, err
	}
	r.NotFound(spa.ServeHTTP)
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
