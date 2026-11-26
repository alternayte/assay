// Package connectors holds the read-only HTTP client of every connector and
// the connector contract. Each source kind lives in a sub-package.
package connectors

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
	// RateLimit is true when the source refused the request for its rate
	// limit. It is never a refused token.
	RateLimit bool
}

func (e *StatusError) Error() string {
	if e.RateLimit {
		return fmt.Sprintf("the source rate limit is used up for %s", e.Path)
	}
	return fmt.Sprintf("the source answered %d for %s", e.Status, e.Path)
}

// SkipError tells sync to skip an item with a reason, such as too_large.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return "skipped: " + e.Reason }

// Ref is a changed item and its update time.
type Ref struct {
	ID      string
	Updated time.Time
	// Version is the source revision of a repository file, its blob ID.
	// Sync fetches the file only when the stored revision differs.
	Version string
	// Item is the whole item when the listing carries it. Sync then stores
	// it without Fetch.
	Item *content.Item
	// Skip is the reason to skip a listed item without Fetch, such as
	// too_large.
	Skip string
}

// Validation is the result of a connection test.
type Validation struct {
	User   string `json:"user" doc:"The source user of the token."`
	Sample int    `json:"sample" doc:"The number of items in the scope, or in the source when no scope is set."`
	// More is true when the source gives no total, such as Jira Cloud:
	// Sample then counts one page, and the scope can hold more.
	More bool `json:"more" doc:"True when the source gives no total. The scope then has sample items or more."`
}

// Scope is one option of a scope picker.
type Scope struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Part is one scope unit of a connection, such as a space, a project or a
// repository. It is the largest piece of a sync that needs no shared state:
// each part has its own cursor and runs as its own job.
type Part struct {
	// Key is stable in the source: a space key, a project key or the full
	// name of a repository.
	Key string
}

// Connector reads one source system. It never writes to it.
type Connector interface {
	// Validate checks the token and counts the items in scope.
	Validate(ctx context.Context) (Validation, error)
	// Scopes lists the scope options, such as spaces or projects.
	Scopes(ctx context.Context) ([]Scope, error)
	// Parts lists the scope units of the connection. A connector with one
	// unit returns one part.
	Parts(ctx context.Context) ([]Part, error)
	// Changed calls fn for each item of a part updated at or after since,
	// oldest first. A repository connector calls fn for every file with
	// its blob ID as the version. Sync stores items inside fn, so a
	// connector that lists page by page holds one page at a time.
	Changed(ctx context.Context, part Part, since time.Time, fn func(Ref) error) error
	// ListIDs calls fn for each item ID of a part.
	ListIDs(ctx context.Context, part Part, fn func(id string) error) error
	// Fetch returns one item. It returns ErrNotFound on a 404.
	Fetch(ctx context.Context, id string) (*content.Item, error)
}

// Limits bound what a connector reads. Source files of a repository have
// their own limits, so that code does not push out knowledge files.
type Limits struct {
	MaxItemBytes        int
	MaxFilesPerRepo     int
	MaxCodeItemBytes    int
	MaxCodeFilesPerRepo int
}

// Skipped is an item or a group of items that a connector did not read,
// with the reason.
type Skipped struct {
	ID     string
	Reason string
}

// SkipLister is a connector that also skips items without fetching them,
// such as generated code. Sync records them in the run.
type SkipLister interface {
	Skipped() []Skipped
}

// Known is an item that sync stored for a part, with its revision.
type Known struct {
	ID       string
	Revision string
}

// Recaller is a connector whose listing starts from the items that sync
// stored, such as the pages that a crawl of a web site found. Sync calls
// Recall before Changed.
type Recaller interface {
	Recall(part Part, known []Known)
}

// Client is the HTTP client of one connection. It sends only GET and HEAD,
// only to the connection host, and follows no redirect to another host. A
// client with a site URL on another host, such as a scoped Atlassian Cloud
// token that reads through api.atlassian.com, also sends to the site host. It
// keeps the concurrency and rate limits of the connection, and retries rate
// limit and 5xx responses with Retry-After, the rate limit reset or
// exponential backoff with jitter. The rate of a stored connection holds
// across all clients of all processes.
type Client struct {
	http *http.Client
	base *url.URL
	// authHeader carries auth: Authorization, or another header such as
	// PRIVATE-TOKEN for GitLab.
	authHeader string
	site       *url.URL
	auth       string
	userAgent  string
	sem        chan struct{}
	pace       *pace
	// Backoff is the first retry delay without Retry-After.
	Backoff time.Duration
	// Retries is the number of retries after the first attempt.
	Retries int
}

// ClientOptions configure a client.
type ClientOptions struct {
	BaseURL string
	// SiteURL is the base of the links that people open, when it is not
	// BaseURL. Entity keys use its host. Empty means BaseURL.
	SiteURL       string
	Authorization string // the Authorization header value
	// AuthHeader is the header that carries Authorization. Empty means
	// Authorization.
	AuthHeader string
	// UserAgent is the User-Agent header. Empty sends the Go default.
	UserAgent   string
	Concurrency int
	PerSecond   float64
	// Pool and ConnectionID share the rate of a stored connection: each
	// request reserves a slot in PostgreSQL. A client without them, for a
	// source that is not stored yet, spaces only its own requests.
	Pool         *pgxpool.Pool
	ConnectionID int64
}

// NewClient returns the client of a connection.
func NewClient(s outbound.Settings, o ClientOptions) (*Client, error) {
	var also []string
	if o.SiteURL != "" {
		also = []string{o.SiteURL}
	}
	h, err := outbound.New(s, outbound.Options{BaseURL: o.BaseURL, AlsoURLs: also, Methods: []string{http.MethodGet, http.MethodHead},
		Timeout: 60 * time.Second})
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(strings.TrimRight(o.BaseURL, "/"))
	site := u
	if o.SiteURL != "" {
		site, _ = url.Parse(strings.TrimRight(o.SiteURL, "/"))
	}
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.PerSecond <= 0 {
		o.PerSecond = 5
	}
	header := o.AuthHeader
	if header == "" {
		header = "Authorization"
	}
	return &Client{
		http: h, base: u, site: site, auth: o.Authorization, authHeader: header, userAgent: o.UserAgent,
		sem:     make(chan struct{}, o.Concurrency),
		pace:    newPace(o.Pool, o.ConnectionID, time.Duration(float64(time.Second)/o.PerSecond)),
		Backoff: time.Second, Retries: 4,
	}, nil
}

// URL returns the absolute URL of a path below the base URL. A path with
// percent escapes, such as the URL-encoded project ID of GitLab, is sent
// as it is.
func (c *Client) URL(path string, q url.Values) string {
	u := *c.base
	raw := strings.TrimRight(u.EscapedPath(), "/") + path
	if p, err := url.PathUnescape(raw); err == nil && strings.Contains(path, "%") {
		u.Path, u.RawPath = p, raw
	} else {
		u.Path, u.RawPath = strings.TrimRight(u.Path, "/")+path, ""
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Link returns the absolute URL of a path below the site URL, for people.
func (c *Client) Link(path string, q url.Values) string {
	u := *c.site
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	return u.String()
}

// Base is the base URL of the connection.
func (c *Client) Base() *url.URL {
	u := *c.base
	return &u
}

// Slow spaces the requests of the client at least every apart, such as
// for the Crawl-delay of a site. A shorter time keeps the rate.
func (c *Client) Slow(every time.Duration) { c.pace.slow(every) }

// Host is the host of the site URL. Entity keys use it.
func (c *Client) Host() string { return c.site.Host }

// GetJSON sends a GET request and decodes the JSON answer into out.
func (c *Client) GetJSON(ctx context.Context, path string, q url.Values, out any) error {
	_, err := c.GetJSONHeader(ctx, path, q, out)
	return err
}

// GetJSONHeader sends a GET request, decodes the JSON answer into out and
// returns the response header, such as the Link header of a paged answer.
func (c *Client) GetJSONHeader(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	b, h, err := c.get(ctx, path, q, "application/json")
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return nil, fmt.Errorf("connectors: the answer for %s is not valid JSON: %w", path, err)
	}
	return h, nil
}

// maxBody bounds one answer.
const maxBody = 64 << 20

// Get sends a GET request and returns the body.
func (c *Client) Get(ctx context.Context, path string, q url.Values, accept string) ([]byte, error) {
	r, err := c.send(ctx, http.MethodGet, c.URL(path, q), path, accept, 0)
	if err != nil {
		return nil, err
	}
	return r.Body, nil
}

// get sends a GET request and returns the body and the response header.
func (c *Client) get(ctx context.Context, path string, q url.Values, accept string) ([]byte, http.Header, error) {
	r, err := c.send(ctx, http.MethodGet, c.URL(path, q), path, accept, 0)
	if err != nil {
		return nil, nil, err
	}
	return r.Body, r.Header, nil
}

// Response is the answer to Send.
type Response struct {
	Header http.Header
	Body   []byte
	// URL is the URL of the answer, after the redirects on the same host.
	URL *url.URL
	// TooLarge is true when the body is longer than the limit. Body is
	// then empty.
	TooLarge bool
}

// Send sends a GET or HEAD request to an absolute URL on the connection
// host, with the retries of Get. A body longer than limit bytes is not
// read; limit 0 reads up to the largest answer.
func (c *Client) Send(ctx context.Context, method, rawURL, accept string, limit int) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("connectors: %q is not a URL: %w", rawURL, err)
	}
	return c.send(ctx, method, rawURL, u.Path, accept, limit)
}

func (c *Client) send(ctx context.Context, method, target, path, accept string, limit int) (*Response, error) {
	var last error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		r, retry, err := c.once(ctx, method, target, path, accept, limit)
		if err == nil {
			return r, nil
		}
		last = err
		if retry < 0 {
			return nil, err
		}
		if retry > 0 {
			// The source named the time to wait. Every request of the
			// connection waits for it, in every process. The next attempt
			// waits in its reservation.
			if err := c.pace.hold(ctx, retry); err != nil {
				return nil, err
			}
			continue
		}
		wait := c.Backoff << attempt
		wait += time.Duration(rand.Int64N(int64(wait)/2 + 1)) //nolint:gosec // Jitter needs no secure source.
		if err := sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
	return nil, last
}

// once sends one request. retry is -1 when a retry cannot help, 0 for the
// default backoff, or the Retry-After delay.
func (c *Client) once(ctx context.Context, method, target, path, accept string, limit int) (*Response, time.Duration, error) {
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, -1, ctx.Err()
	}
	defer func() { <-c.sem }()
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, -1, err
	}
	req.Header.Set("Accept", accept)
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, errPace) {
			return nil, -1, err
		}
		if ctx.Err() != nil || errors.Is(err, outbound.ErrForeignHost) || errors.Is(err, outbound.ErrMethod) || isCertificate(err) {
			return nil, -1, err
		}
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		if wait, ok := rateLimit(resp); ok {
			return nil, wait, &StatusError{Status: resp.StatusCode, Path: path, RateLimit: true}
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, -1, fmt.Errorf("%w: %w", ErrUnauthorized, &StatusError{Status: resp.StatusCode, Path: path})
		}
		return nil, 0, &StatusError{Status: resp.StatusCode, Path: path, RateLimit: true}
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, -1, fmt.Errorf("%w: %w", ErrUnauthorized, &StatusError{Status: resp.StatusCode, Path: path})
	case resp.StatusCode == http.StatusNotFound:
		return nil, -1, ErrNotFound
	case resp.StatusCode >= 500:
		return nil, retryAfter(resp.Header.Get("Retry-After")), &StatusError{Status: resp.StatusCode, Path: path}
	case resp.StatusCode >= 300:
		return nil, -1, &StatusError{Status: resp.StatusCode, Path: path}
	}
	out := &Response{Header: resp.Header, URL: resp.Request.URL}
	if method == http.MethodHead {
		return out, 0, nil
	}
	n := int64(maxBody)
	if limit > 0 {
		n = int64(limit) + 1
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, n))
	if err != nil {
		return nil, 0, err
	}
	if limit > 0 && len(b) > limit {
		out.TooLarge = true
		return out, 0, nil
	}
	out.Body = b
	return out, 0, nil
}

// Do sends a request with the connection credentials in the next request
// slot of the connection. The guard refuses every method except GET and HEAD
// before anything is sent.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if err := c.pace.wait(req.Context()); err != nil {
		return nil, err
	}
	if c.auth != "" {
		req.Header.Set(c.authHeader, c.auth)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	return c.http.Do(req)
}

// isCertificate reports a TLS certificate error. A retry cannot fix it.
func isCertificate(err error) bool {
	var verify *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &verify) || errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &invalid)
}

// rateLimit reports whether a 403 or 429 is for the rate limit of the
// source, and the delay until the source takes requests again. GitHub
// answers 403 for its primary and secondary rate limits: the first with
// X-RateLimit-Remaining 0 and the reset time, the second with Retry-After
// or only a message. A delay of 0 means the default backoff.
func rateLimit(resp *http.Response) (time.Duration, bool) {
	if v := resp.Header.Get("Retry-After"); v != "" {
		return retryAfter(v), true
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
		if err != nil {
			return 0, true
		}
		return min(max(time.Until(time.Unix(reset, 0))+time.Second, time.Second), 5*time.Minute), true
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return 0, strings.Contains(strings.ToLower(string(b)), "rate limit")
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
