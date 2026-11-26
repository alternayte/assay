// Package auth configures auth-all for Assay: email and password sign-in,
// sign-in with the identity provider, database sessions, access tokens, the
// role hierarchy, user administration, sign-in rate limits and the OAuth
// authorization server of MCP clients.
package auth

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"strings"
	"time"

	authall "github.com/alternayte/auth-all"
	"github.com/alternayte/auth-all/oauth"
	"github.com/alternayte/auth-all/plugins/admin"
	"github.com/alternayte/auth-all/plugins/apikeys"
	"github.com/alternayte/auth-all/plugins/oauthprovider"
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
	// OAuth is the authorization server of the MCP clients.
	OAuth *oauthprovider.Plugin
	// MCPResource is the resource identifier of /mcp: the base URL and
	// /mcp. Every OAuth access token names it as its audience.
	MCPResource string
}

// TokenPrefix starts every access token.
const TokenPrefix = "assay_"

// Access token scopes (REQ-AGT-005). A token without a scope reads. A session
// holds both.
const (
	ScopeRead    = "read"
	ScopePropose = "propose"
)

// ScopeOfflineAccess lets an MCP client get a refresh token, so that it
// keeps its connection after the access token expires.
const ScopeOfflineAccess = oauthprovider.ScopeOfflineAccess

// ConsentPath is the SPA page where a user approves an MCP client. The
// sign-in page is the login path.
const ConsentPath = "/oauth/consent"

// kekInfo is the HKDF info string of the key encryption key of the OAuth
// signing keys.
const kekInfo = "assay-oauth-kek"

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
// session cookie and the MCP resource. secretKey is ASSAY_SECRET_KEY: the key
// encryption key of the OAuth signing keys comes from it.
func New(pool *pgxpool.Pool, baseURL string, secretKey []byte, log *slog.Logger, opts Options) (*Auth, error) {
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
	// A new ASSAY_SECRET_KEY gives a new key encryption key. The plugin then
	// cannot read the stored signing keys and issues a new one, so every
	// connected client connects again.
	kek, err := hkdf.Key(sha256.New, secretKey, nil, kekInfo, 32)
	if err != nil {
		return nil, err
	}
	mcp := strings.TrimRight(baseURL, "/") + "/mcp"
	scopes := []string{ScopeRead, ScopePropose, ScopeOfflineAccess}
	provider := oauthprovider.New(
		oauthprovider.KeyEncryptionKey(kek),
		oauthprovider.LoginPath(SignInPath),
		oauthprovider.ConsentPath(ConsentPath),
		oauthprovider.Scopes(scopes...),
		oauthprovider.Resources(oauthprovider.Resource{Identifier: mcp, Scopes: scopes}),
		oauthprovider.AllowClientIDMetadataDocuments(oauthprovider.CIMDOptions{}),
		oauthprovider.AllowDynamicRegistration(),
	)
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
		authall.WithPlugins(r, adm, keys, provider),
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
	return &Auth{Auth: a, Roles: r, Admin: adm, Keys: keys, Limiter: limiter, OAuth: provider, MCPResource: mcp}, nil
}
