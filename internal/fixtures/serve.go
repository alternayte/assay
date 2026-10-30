package fixtures

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Serve runs the fixture server until ctx ends. The fake model API answers
// under /v1 and /api/alpha/decisions, the fake Confluence API under
// /confluence, the fake Jira API under /jira, the corpus manifest at
// /manifest.json and the test control routes under /control.
func Serve(ctx context.Context, addr string, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	sources := NewSources()
	mux.Handle("/confluence/", sources)
	mux.Handle("/jira/", sources)
	mux.Handle("/github/", sources)
	mux.Handle("/ghes/", sources)
	mux.Handle("/azdo/", sources)
	mux.Handle("GET /manifest.json", sources)
	// The control routes let the browser tests change a page and read the
	// methods that Assay sent. No connector calls them.
	mux.HandleFunc("GET /control/hits", func(w http.ResponseWriter, _ *http.Request) {
		type hit struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		}
		hs := []hit{}
		for _, h := range sources.Hits() {
			hs = append(hs, hit{Method: h.Method, Path: h.Path})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hs)
	})
	mux.HandleFunc("POST /control/pages/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Append string `json:"append"`
		}
		p, ok := sources.Page(r.PathValue("id"))
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !ok {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p.Body += in.Append
		p.Updated = time.Time{}
		sources.PutPage(p)
		w.WriteHeader(http.StatusNoContent)
	})
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
