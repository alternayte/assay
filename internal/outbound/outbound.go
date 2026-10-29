// Package outbound builds the HTTP clients for every outbound request: to
// source systems and to model endpoints. A client uses the proxy and CA
// settings, sends only to its own host and follows no redirect to another
// host.
package outbound

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// Settings are the proxy and CA settings of the process.
type Settings struct {
	HTTPProxy   string
	HTTPSProxy  string
	NoProxy     string
	ExtraCAPEM  string
	ExtraCAFile string
}

// ErrForeignHost means that a request or a redirect named a host other than
// the host of the client.
var ErrForeignHost = errors.New("outbound: the request goes to another host")

// ErrMethod means that the client does not send this method.
var ErrMethod = errors.New("outbound: the method is not allowed")

// RootCAs returns the system pool with the extra CA certificates.
func (s Settings) RootCAs() (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pems := []string{}
	if strings.TrimSpace(s.ExtraCAPEM) != "" {
		pems = append(pems, s.ExtraCAPEM)
	}
	if s.ExtraCAFile != "" {
		b, err := os.ReadFile(s.ExtraCAFile)
		if err != nil {
			return nil, fmt.Errorf("ASSAY_EXTRA_CA_FILE: %w", err)
		}
		pems = append(pems, string(b))
	}
	for _, p := range pems {
		if !pool.AppendCertsFromPEM([]byte(p)) {
			return nil, errors.New("the extra CA certificates contain no valid PEM certificate")
		}
	}
	return pool, nil
}

// Proxy returns the proxy function of the settings for a transport.
func (s Settings) Proxy() func(*http.Request) (*url.URL, error) {
	cfg := httpproxy.Config{HTTPProxy: s.HTTPProxy, HTTPSProxy: s.HTTPSProxy, NoProxy: s.NoProxy}
	f := cfg.ProxyFunc()
	return func(r *http.Request) (*url.URL, error) { return f(r.URL) }
}

// Options describe one client.
type Options struct {
	// BaseURL names the only host that the client sends to.
	BaseURL string
	// Methods are the methods that the client sends. Empty allows all.
	Methods []string
	// Timeout bounds each request. Zero means no client timeout; the
	// caller then sets a deadline on the context.
	Timeout time.Duration
}

// New returns a client for one host.
func New(s Settings, o Options) (*http.Client, error) {
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("outbound: %q is not an http or https URL", o.BaseURL)
	}
	pool, err := s.RootCAs()
	if err != nil {
		return nil, err
	}
	t := &http.Transport{
		Proxy:                 s.Proxy(),
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	host := hostKey(u)
	guard := &guard{next: t, host: host, methods: o.Methods}
	return &http.Client{
		Transport: guard,
		Timeout:   o.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if hostKey(req.URL) != host {
				return fmt.Errorf("%w: a redirect to %s is not followed", ErrForeignHost, req.URL.Host)
			}
			if len(via) >= 5 {
				return errors.New("outbound: too many redirects")
			}
			return nil
		},
	}, nil
}

type guard struct {
	next    http.RoundTripper
	host    string
	methods []string
}

func (g *guard) RoundTrip(r *http.Request) (*http.Response, error) {
	if len(g.methods) > 0 && !slices.Contains(g.methods, r.Method) {
		return nil, fmt.Errorf("%w: %s", ErrMethod, r.Method)
	}
	if hostKey(r.URL) != g.host {
		return nil, fmt.Errorf("%w: %s", ErrForeignHost, r.URL.Host)
	}
	return g.next.RoundTrip(r)
}

// hostKey is the lowercase host with its effective port.
func hostKey(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return strings.ToLower(u.Hostname()) + ":" + port
}
