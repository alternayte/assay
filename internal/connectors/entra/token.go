// Package entra holds the client-credentials token client of Microsoft Entra
// ID. Microsoft Graph has no token flow over GET, so the token request is the
// one POST that Assay sends to a source system. The client sends it only to
// the configured token URL. Graph reads go through the GET-only connector
// client with the token as the Authorization header.
package entra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/alternayte/assay/internal/outbound"
)

// Authority is the public Entra ID sign-in host.
const Authority = "https://login.microsoftonline.com"

// GraphURL is the public Microsoft Graph v1.0 endpoint.
const GraphURL = "https://graph.microsoft.com/v1.0"

// TokenURL returns the v2.0 token endpoint of a tenant at an authority.
func TokenURL(authority, tenant string) string {
	return strings.TrimRight(authority, "/") + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
}

// TokenURLFromIssuer returns the token endpoint of an Entra ID v2.0 issuer,
// such as https://login.microsoftonline.com/<tenant>/v2.0. It fails for an
// issuer of another form.
func TokenURLFromIssuer(issuer string) (string, error) {
	base, ok := strings.CutSuffix(strings.TrimRight(issuer, "/"), "/v2.0")
	if !ok {
		return "", errors.New("entra: the issuer is not an Entra ID v2.0 issuer; it must end with /v2.0")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("entra: the issuer %q is not a URL", issuer)
	}
	return base + "/oauth2/v2.0/token", nil
}

// ScopeFor returns the .default scope of a resource URL, such as
// https://graph.microsoft.com/.default for the Graph URL.
func ScopeFor(resource string) (string, error) {
	u, err := url.Parse(resource)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("entra: %q is not a URL", resource)
	}
	return u.Scheme + "://" + u.Host + "/.default", nil
}

// ErrRefused means that the token endpoint refused the app registration,
// such as a wrong client secret or an unknown tenant. A retry cannot help.
var ErrRefused = errors.New("entra: the token endpoint refused the credentials")

// refused is a refusal of the token endpoint. Its text is the answer of the
// endpoint.
type refused struct{ msg string }

func (e *refused) Error() string        { return e.msg }
func (e *refused) Is(target error) bool { return target == ErrRefused }

// Credentials are the app registration and the token endpoint.
type Credentials struct {
	// TokenURL is the only URL that the client sends to.
	TokenURL     string
	ClientID     string
	ClientSecret string
	// Scope is the .default scope of the resource, see ScopeFor.
	Scope string
}

// TokenSource gets and caches the access token of an app registration with
// the client-credentials grant. It is safe for concurrent use.
type TokenSource struct {
	http  *http.Client
	creds Credentials
	// Now is the clock. Tests set it.
	Now func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// NewTokenSource returns a token source. Its HTTP client sends only POST and
// only to creds.TokenURL.
func NewTokenSource(s outbound.Settings, creds Credentials) (*TokenSource, error) {
	if creds.ClientID == "" || creds.ClientSecret == "" || creds.Scope == "" {
		return nil, errors.New("entra: the client ID, the client secret and the scope are required")
	}
	h, err := outbound.New(s, outbound.Options{BaseURL: creds.TokenURL, Methods: []string{http.MethodPost},
		OnlyURL: true, Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	return &TokenSource{http: h, creds: creds, Now: time.Now}, nil
}

// refreshBefore is the time before expiry at which a cached token is
// replaced.
const refreshBefore = 2 * time.Minute

// Token returns a valid access token. It requests a new one when the cached
// token expires within two minutes.
func (t *TokenSource) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.Now()
	if t.token != "" && now.Add(refreshBefore).Before(t.expiry) {
		return t.token, nil
	}
	token, ttl, err := t.request(ctx)
	if err != nil {
		return "", err
	}
	t.token, t.expiry = token, now.Add(ttl)
	return token, nil
}

// Authorization returns the Authorization header value with a valid token.
func (t *TokenSource) Authorization(ctx context.Context) (string, error) {
	token, err := t.Token(ctx)
	if err != nil {
		return "", err
	}
	return "Bearer " + token, nil
}

func (t *TokenSource) request(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", t.creds.ClientID)
	form.Set("client_secret", t.creds.ClientSecret)
	form.Set("scope", t.creds.Scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.creds.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("entra: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		AccessToken string      `json:"access_token"`
		ExpiresIn   json.Number `json:"expires_in"`
		Error       string      `json:"error"`
		Description string      `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", 0, fmt.Errorf("entra: the token endpoint answered %d with no valid JSON", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || body.AccessToken == "" {
		msg := body.Error
		if body.Description != "" {
			msg += ": " + firstLine(body.Description)
		}
		if msg == "" {
			msg = "no access token"
		}
		err := fmt.Errorf("entra: the token endpoint answered %d: %s", resp.StatusCode, msg)
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			return "", 0, &refused{msg: err.Error()}
		}
		return "", 0, err
	}
	secs, err := body.ExpiresIn.Int64()
	if err != nil || secs <= 0 {
		secs = 3600
	}
	return body.AccessToken, time.Duration(secs) * time.Second, nil
}

// firstLine keeps the first line of an Entra error description, which
// carries the AADSTS code. The later lines hold trace and correlation IDs.
func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}
