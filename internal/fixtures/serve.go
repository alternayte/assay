package fixtures

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Serve runs the fixture server until ctx ends. The fake model API answers
// under /v1 and /api/alpha/decisions, the fake Confluence API under
// /confluence, the fake Jira API under /jira and the corpus manifest at
// /manifest.json.
func Serve(ctx context.Context, addr string, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	sources := NewSources()
	mux.Handle("/confluence/", sources)
	mux.Handle("/jira/", sources)
	mux.Handle("GET /manifest.json", sources)
	mux.Handle("/", NewModels())
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("fixtures listening", "address", ln.Addr().String(), "model_key", FakeKey, "source_token", SourceToken)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(c); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
