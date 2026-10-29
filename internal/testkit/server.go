package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alternayte/auth-all/ratelimit"

	"github.com/alternayte/assay/internal/app"
)

// BaseURL is the public URL of a test server. Clients send it as Origin.
const BaseURL = "http://assay.test"

// SetupToken is the setup token of a test server.
const SetupToken = "test-setup-token"

// Env is a running test server.
type Env struct {
	t      testing.TB
	Server *app.Server
	HTTP   *httptest.Server
	Config app.Config
	Logs   *bytes.Buffer
}

// Option changes the configuration of a test server.
type Option func(*app.Config, *app.Options)

// WithBaseURL sets the public URL.
func WithBaseURL(u string) Option {
	return func(c *app.Config, _ *app.Options) { c.BaseURL = u }
}

// WithClock sets the clock of the server.
func WithClock(now func() time.Time) Option {
	return func(_ *app.Config, o *app.Options) { o.Now = now }
}

// WithSignInRules sets the sign-in rate limits.
func WithSignInRules(rules []ratelimit.Rule) Option {
	return func(_ *app.Config, o *app.Options) { o.Auth.SignInRules = rules }
}

// Start runs a server on a fresh migrated database.
func Start(t testing.TB, opts ...Option) *Env {
	t.Helper()
	return StartOn(t, DB(t), opts...)
}

// StartOn runs a server on the database at dbURL.
func StartOn(t testing.TB, dbURL string, opts ...Option) *Env {
	t.Helper()
	cfg := app.Config{
		DatabaseURL: dbURL,
		SecretKey:   bytes.Repeat([]byte{7}, 32),
		BaseURL:     BaseURL,
		Listen:      "127.0.0.1:0",
		Role:        app.RoleAll,
		SetupToken:  SetupToken,
	}
	var o app.Options
	for _, opt := range opts {
		opt(&cfg, &o)
	}
	logs := &bytes.Buffer{}
	s, err := app.New(context.Background(), cfg, app.NewLogger(logs, slog.LevelDebug), o)
	if err != nil {
		t.Fatalf("testkit: start server: %v", err)
	}
	h := httptest.NewServer(s.Handler())
	t.Cleanup(func() {
		h.Close()
		s.Close()
	})
	return &Env{t: t, Server: s, HTTP: h, Config: cfg, Logs: logs}
}

// Client is an HTTP client with a cookie jar that sends the server origin.
type Client struct {
	t      testing.TB
	env    *Env
	http   *http.Client
	Origin string
	Bearer string
}

// Client returns an anonymous client.
func (e *Env) Client() *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{t: e.t, env: e, http: &http.Client{Jar: jar}, Origin: e.Config.BaseURL}
}

// Response is a finished request.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON decodes the body into v.
func (r *Response) JSON(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %s: %v", r.Body, err)
	}
}

// Code returns the error code of an error response.
func (r *Response) Code() string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &e)
	return e.Error.Code
}

// Raw is a request body that is sent as it is, with its content type.
type Raw struct {
	ContentType string
	Data        []byte
}

// Do sends a request with an optional body: a Raw body, or any other value
// as JSON.
func (c *Client) Do(method, path string, body any) *Response {
	c.t.Helper()
	var rd io.Reader
	contentType := "application/json"
	switch b := body.(type) {
	case nil:
	case Raw:
		rd, contentType = bytes.NewReader(b.Data), b.ContentType
	default:
		data, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.env.HTTP.URL+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if c.Origin != "" && method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Origin", c.Origin)
	}
	if c.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.Bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// Must sends a request and fails the test unless the status is want.
func (c *Client) Must(want int, method, path string, body any) *Response {
	c.t.Helper()
	r := c.Do(method, path, body)
	if r.Status != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, r.Status, want, r.Body)
	}
	return r
}

// SignIn signs the client in.
func (c *Client) SignIn(email, password string) *Response {
	c.t.Helper()
	return c.Do(http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": email, "password": password})
}

// AdminPassword is the password of the first admin.
const AdminPassword = "admin-password-1"

// Setup creates the first admin and returns a signed-in client.
func (e *Env) Setup(email string) *Client {
	e.t.Helper()
	c := e.Client()
	c.Must(http.StatusCreated, http.MethodPost, "/api/v1/setup", map[string]string{
		"token": SetupToken, "email": email, "name": "First Admin", "password": AdminPassword,
	})
	c.Must(http.StatusOK, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": email, "password": AdminPassword})
	return c
}

// Invite creates a user with role through an invite, and returns the
// signed-in client and the user ID.
func (e *Env) Invite(admin *Client, email, role, password string) (*Client, string) {
	e.t.Helper()
	var inv struct {
		URL string `json:"url"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/invites", map[string]string{"email": email, "role": role}).JSON(e.t, &inv)
	token := inv.URL[len(e.Config.BaseURL+"/invite#"):]
	c := e.Client()
	var u struct {
		ID string `json:"id"`
	}
	c.Must(http.StatusCreated, http.MethodPost, "/api/v1/invites/accept", map[string]string{"token": token, "name": email, "password": password}).JSON(e.t, &u)
	c.Must(http.StatusOK, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": email, "password": password})
	return c, u.ID
}
