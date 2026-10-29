package testkit

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/outbound"
)

// Proxy is an HTTP proxy that tunnels CONNECT requests and records their
// targets.
type Proxy struct {
	URL     string
	mu      sync.Mutex
	targets []string
}

// Targets returns the hosts that clients tunnelled to.
func (p *Proxy) Targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.targets...)
}

// StartProxy runs a tunnelling proxy until the test ends. A non-empty dial
// address replaces the target address, so that a test can use a name that
// the test certificate covers.
func StartProxy(t testing.TB, dial string) *Proxy {
	t.Helper()
	p := &Proxy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
			return
		}
		p.mu.Lock()
		p.targets = append(p.targets, r.Host)
		p.mu.Unlock()
		d := net.Dialer{Timeout: 5 * time.Second}
		addr := r.Host
		if dial != "" {
			addr = dial
		}
		up, err := d.DialContext(r.Context(), "tcp", addr)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			_ = up.Close()
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		down, _, err := hj.Hijack()
		if err != nil {
			_ = up.Close()
			return
		}
		go func() {
			_, _ = io.Copy(up, down)
			_ = up.Close()
		}()
		_, _ = io.Copy(down, up)
		_ = down.Close()
	}))
	t.Cleanup(srv.Close)
	p.URL = srv.URL
	return p
}

// WithOutbound sets the proxy and CA settings.
func WithOutbound(s outbound.Settings) Option {
	return func(c *app.Config, _ *app.Options) { c.Outbound = s }
}
