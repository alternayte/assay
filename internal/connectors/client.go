// Package connectors holds the read-only HTTP client of every connector and
// the connector contract. Each source kind lives in a sub-package.
package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/outbound"
)

// Errors of a source request.
var (
	// ErrUnauthorized is a 401 or 403. Sync pauses the connection.
	ErrUnauthorized = errors.New("connectors: the source refused the token")
	// ErrNotFound is a 404. On fetch, sync tombstones the item.
	ErrNotFound = errors.New("connectors: the source object does not exist")
)

// StatusError is another error status of the source.
type StatusError struct {
	Status int
	Path   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("the source answered %d for %s", e.Status, e.Path)
}

// SkipError tells sync to skip an item with a reason, such as too_large.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "skipped: " + e.Reason }

// Ref is a changed item and its update time.
type Ref struct {
	ID      string
	Updated time.Time
}

// Validation is the result of a connection test.
type Validation struct {
	User   string `json:"user" doc:"The source user of the token."`
	Sample int    `json:"sample" doc:"The number of items in the scope, or in the source when no scope is set."`
}

// Scope is one option of a scope picker.
type Scope struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Connector reads one source system. It never writes to it.
type Connector interface {
	// Validate checks the token and counts the items in scope.
	Validate(ctx context.Context) (Validation, error)
	// Scopes lists the scope options, such as spaces or projects.
	Scopes(ctx context.Context) ([]Scope, error)
	// Changed calls fn for each item in scope updated at or after since,
	// oldest first.
	Changed(ctx context.Context, since time.Time, fn func(Ref) error) error
	// ListIDs calls fn for each item ID in scope.
	ListIDs(ctx context.Context, fn func(id string) error) error
	// Fetch returns one item. It returns ErrNotFound on a 404.
	Fetch(ctx context.Context, id string) (*content.Item, error)
}

// Limits bound what a connector reads.
type Limits struct {
	MaxItemBytes    int
	MaxFilesPerRepo int
}

// Client is the HTTP client of one connection. It sends only GET and HEAD,
// only to the connection host, and follows no redirect to another host. It
// keeps the concurrency and rate limits of the connection, and retries 429
// and 5xx responses with Retry-After or exponential backoff with jitter.
type Client struct {
	http    *http.Client
	base    *url.URL
	auth    string
	sem     chan struct{}
	limiter *limiter
	// Backoff is the first retry delay without Retry-After.
	Backoff time.Duration
	// Retries is the number of retries after the first attempt.
	Retries int
}

// ClientOptions configure a client.
type ClientOptions struct {
	BaseURL       string
	Authorization string // the Authorization header value
	Concurrency   int
	PerSecond     float64
}

// NewClient returns the client of a connection.
func NewClient(s outbound.Settings, o ClientOptions) (*Client, error) {
	h, err := outbound.New(s, outbound.Options{BaseURL: o.BaseURL, Methods: []string{http.MethodGet, http.MethodHead}, Timeout: 60 * time.Second})
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(strings.TrimRight(o.BaseURL, "/"))
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.PerSecond <= 0 {
		o.PerSecond = 5
	}
	return &Client{
		http: h, base: u, auth: o.Authorization,
		sem:     make(chan struct{}, o.Concurrency),
		limiter: &limiter{every: time.Duration(float64(time.Second) / o.PerSecond)},
		Backoff: time.Second, Retries: 4,
	}, nil
}

// URL returns the absolute URL of a path below the base URL.
func (c *Client) URL(path string, q url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	return u.String()
}

// Host is the host of the base URL.
func (c *Client) Host() string { return c.base.Host }

// GetJSON sends a GET request and decodes the JSON answer into out.
func (c *Client) GetJSON(ctx context.Context, path string, q url.Values, out any) error {
	b, err := c.Get(ctx, path, q, "application/json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("connectors: the answer for %s is not valid JSON: %w", path, err)
	}
	return nil
}

// maxBody bounds one answer.
const maxBody = 64 << 20

// Get sends a GET request and returns the body.
func (c *Client) Get(ctx context.Context, path string, q url.Values, accept string) ([]byte, error) {
	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		b, retry, err := c.once(ctx, path, q, accept)
		if err == nil {
			return b, nil
		}
		last = err
		if retry < 0 {
			return nil, err
		}
		wait := retry
		if wait == 0 {
			wait = c.Backoff << attempt
			wait += time.Duration(rand.Int64N(int64(wait)/2 + 1)) //nolint:gosec // Jitter needs no secure source.
		}
		c.limiter.pause(wait)
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, last
}

// once sends one request. retry is -1 when a retry cannot help, 0 for the
// default backoff, or the Retry-After delay.
func (c *Client) once(ctx context.Context, path string, q url.Values, accept string) ([]byte, time.Duration, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, -1, ctx.Err()
	}
	defer func() { <-c.sem }()
	if err := c.limiter.wait(ctx); err != nil {
		return nil, -1, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL(path, q), nil)
	if err != nil {
		return nil, -1, err
	}
	req.Header.Set("Accept", accept)
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, outbound.ErrForeignHost) || errors.Is(err, outbound.ErrMethod) {
			return nil, -1, err
		}
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, -1, fmt.Errorf("%w (%d)", ErrUnauthorized, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return nil, -1, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, retryAfter(resp.Header.Get("Retry-After")), &StatusError{Status: resp.StatusCode, Path: path}
	case resp.StatusCode >= 300:
		return nil, -1, &StatusError{Status: resp.StatusCode, Path: path}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, 0, err
	}
	return b, 0, nil
}

// Do sends a request with the connection credentials. The guard refuses
// every method except GET and HEAD before anything is sent.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.auth != "" {
		req.Header.Set("Authorization", c.auth)
	}
	return c.http.Do(req)
}

func retryAfter(v string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s >= 0 {
		return min(time.Duration(s)*time.Second, 5*time.Minute)
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return min(d, 5*time.Minute)
		}
	}
	return 0
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limiter spaces requests evenly at the rate of the connection.
type limiter struct {
	mu    gosync.Mutex
	next  time.Time
	every time.Duration
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.every)
	l.mu.Unlock()
	return sleep(ctx, time.Until(at))
}

// pause holds every request of the client for d.
func (l *limiter) pause(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if at := time.Now().Add(d); at.After(l.next) {
		l.next = at
	}
}

// Rules are the connector rules of the settings.
type Rules struct {
	InitiativeTypes []string
	BlockLinkType   string
	DecisionLabels  []string
	GlossaryLabels  []string
}

// Overlap widens the since time of a source query whose dates use the time
// zone of the source user. Fourteen hours cover every UTC offset.
const Overlap = 14 * time.Hour
