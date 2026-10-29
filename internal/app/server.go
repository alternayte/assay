package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/users"
)

// Server is one Assay process.
type Server struct {
	cfg     Config
	log     *slog.Logger
	pool    *pgxpool.Pool
	auth    *auth.Auth
	users   *users.Service
	handler http.Handler
	now     func() time.Time
}

// Options change defaults for tests.
type Options struct {
	Auth auth.Options
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
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
	s := &Server{cfg: cfg, log: log, pool: pool, auth: a, now: opts.Now}
	if s.now == nil {
		s.now = time.Now
	}
	h, err := s.routes()
	if err != nil {
		pool.Close()
		return nil, err
	}
	s.handler = h
	return s, nil
}

// Handler is the HTTP handler of the process.
func (s *Server) Handler() http.Handler { return s.handler }

// Pool is the database pool.
func (s *Server) Pool() *pgxpool.Pool { return s.pool }

// Close releases the database pool.
func (s *Server) Close() { s.pool.Close() }

func (s *Server) routes() (http.Handler, error) {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	r.Get("/readyz", s.ready)
	r.Handle("/metrics", promhttp.Handler())
	r.Handle(s.auth.BasePath()+"/*", s.authHandler())

	var apiErr error
	r.Group(func(g chi.Router) {
		g.Use(s.auth.LoadSession)
		a, err := s.newAPI(g)
		if err != nil {
			apiErr = err
			return
		}
		s.users = &users.Service{Pool: s.pool, Auth: s.auth, BaseURL: s.cfg.BaseURL, SetupToken: s.cfg.SetupToken, Now: s.now}
		s.users.Register(a)
	})
	return r, apiErr
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

// Run serves HTTP until ctx ends, then shuts down within 30 seconds.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	s.log.Info("listening", "address", ln.Addr().String(), "role", s.cfg.Role)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 25*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
