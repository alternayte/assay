// Package auth configures auth-all for Assay: email and password sign-in,
// sign-in with the identity provider, database sessions, access tokens, the
// role hierarchy, user administration and sign-in rate limits.
package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	authall "github.com/alternayte/auth-all"
	"github.com/alternayte/auth-all/oauth"
	"github.com/alternayte/auth-all/plugins/admin"
	"github.com/alternayte/auth-all/plugins/apikeys"
	"github.com/alternayte/auth-all/plugins/roles"
	"github.com/alternayte/auth-all/ratelimit"
	"github.com/alternayte/auth-all/ratelimit/storelimit"
	"github.com/alternayte/auth-all/schema"
	"github.com/alternayte/auth-all/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Roles in ascending order of permission.
const (
	RoleMember   = "member"
	RoleReviewer = "reviewer"
	RoleAdmin    = "admin"
)

// ValidRole reports whether r is an Assay role.
func ValidRole(r string) bool {
	return r == RoleMember || r == RoleReviewer || r == RoleAdmin
}

// CookieName is the name of the session cookie.
const CookieName = "assay_session"

// Auth is the configured auth-all instance with the plugins that Assay calls.
type Auth struct {
	*authall.Auth
	Roles   *roles.Plugin
	Admin   *admin.Plugin
	Keys    *apikeys.Plugin
	Limiter *storelimit.Limiter
}

// TokenPrefix starts every access token.
const TokenPrefix = "assay_"

// Access token scopes (REQ-AGT-005). A token without a scope reads. A session
// holds both.
const (
	ScopeRead    = "read"
	ScopePropose = "propose"
)

// MaxTokenTTL is the longest life of an access token (REQ-AGT-005).
const MaxTokenTTL = 365 * 24 * time.Hour

// Options change the defaults for tests.
type Options struct {
	SignInRules []ratelimit.Rule
	// Providers returns the OAuth providers of a request. The SSO
	// settings set it.
	Providers func(ctx context.Context) ([]oauth.Provider, error)
}

// SignInPath is the SPA page that a failed provider sign-in returns to with
// the error code.
const SignInPath = "/sign-in"

// New builds the instance over pool. baseURL decides the Secure flag of the
// session cookie.
func New(pool *pgxpool.Pool, baseURL string, log *slog.Logger, opts Options) (*Auth, error) {
	st, err := postgres.NewPool(pool, postgres.AllowStatementCache())
	if err != nil {
		return nil, err
	}
	rules := opts.SignInRules
	if rules == nil {
		rules = ratelimit.DefaultSignInRules()
	}
	limiter, err := storelimit.New(st, rules)
	if err != nil {
		return nil, err
	}
	r := roles.New(roles.Hierarchy(RoleMember, RoleReviewer, RoleAdmin), roles.Default(RoleMember))
	adm := admin.New(admin.AdminRole(RoleAdmin))
	keys := apikeys.New(apikeys.Prefix(TokenPrefix), apikeys.MaxTTL(MaxTokenTTL), apikeys.AdminRole(RoleAdmin),
		apikeys.Scopes(ScopeRead, ScopePropose), apikeys.DefaultScopes(ScopeRead))
	secure := strings.HasPrefix(baseURL, "https://")
	options := []authall.Option{
		authall.WithStore(st),
		authall.WithSchema(schema.Options{Prefix: schema.DefaultPrefix, IDType: schema.IDUUID}),
		authall.WithSchemaCheck(authall.SchemaCheckCatalog),
		authall.WithBaseURL(baseURL),
		authall.WithEmailPassword(),
		authall.WithCookie(authall.CookieOptions{Name: CookieName, SameSite: http.SameSiteLaxMode, Secure: &secure}),
		authall.WithRateLimiter(limiter),
		authall.WithLogger(log),
		authall.WithPlugins(r, adm, keys),
		authall.WithBrowserErrorRedirect(SignInPath),
		// An existing user links to an IdP account only by a verified
		// address on both sides.
		authall.WithAccountLinking(authall.AccountLinkingOptions{AllowVerifiedEmailAutoLink: true}),
	}
	if opts.Providers != nil {
		options = append(options, authall.WithProviderSource(opts.Providers))
	}
	a, err := authall.New(options...)
	if err != nil {
		return nil, err
	}
	return &Auth{Auth: a, Roles: r, Admin: adm, Keys: keys, Limiter: limiter}, nil
}
