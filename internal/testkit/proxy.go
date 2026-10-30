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
		// Hijack first and write the status line by hand, so that no
		// framing of the HTTP server enters the tunnel.
		down, buf, err := hj.Hijack()
		if err != nil {
			_ = up.Close()
			return
		}
		if _, err := down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
			_ = up.Close()
			_ = down.Close()
			return
		}
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Peek(n)
			_, _ = up.Write(b)
		}
		done := make(chan struct{}, 2)
		go func() {
			_, _ = io.Copy(up, down)
			done <- struct{}{}
		}()
		go func() {
			_, _ = io.Copy(down, up)
			done <- struct{}{}
		}()
		<-done
		_ = up.Close()
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
