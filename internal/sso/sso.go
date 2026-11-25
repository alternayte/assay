// Package sso holds sign-in with the company identity provider through
// OpenID Connect, the mapping of IdP groups to Assay groups and the directory
// sync. auth-all signs the user in and creates the user. Assay owns the
// configuration, the group mappings and the directory state.
package sso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alternayte/auth-all/apierr"
	"github.com/alternayte/auth-all/hook"
	"github.com/alternayte/auth-all/oauth"
	"github.com/alternayte/auth-all/oauth/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/connectors/entra"
	"github.com/alternayte/assay/internal/directory"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/secrets"
	"github.com/alternayte/assay/internal/settings"
)

// ProviderID is the auth-all provider ID of the identity provider. It is
// fixed, so that the auth_accounts rows stay valid when the issuer changes.
const ProviderID = "sso"

// SecretName is the name of the secret that holds the client secret.
const SecretName = "oidc.client_secret" //nolint:gosec // The name of a secret, not a secret.

// DirectoryEntra is the Entra ID directory provider.
const DirectoryEntra = "entra"

// DirectoryNone turns the directory sync off.
const DirectoryNone = "none"

// The environment variables of the sign-in settings. A set variable wins
// over the stored value, and Settings shows the field as locked.
const (
	EnvIssuer       = "ASSAY_OIDC_ISSUER"
	EnvClientID     = "ASSAY_OIDC_CLIENT_ID"
	EnvClientSecret = "ASSAY_OIDC_CLIENT_SECRET" //nolint:gosec // A variable name, not a secret.
	EnvButtonLabel  = "ASSAY_OIDC_BUTTON_LABEL"
	EnvAccessGroup  = "ASSAY_OIDC_ACCESS_GROUP"
	EnvPassword     = "ASSAY_PASSWORD_SIGNIN"
	EnvDirectory    = "ASSAY_DIRECTORY_PROVIDER"
	EnvInterval     = "ASSAY_DIRECTORY_INTERVAL"
	// EnvGraphURL replaces the public Graph URL, for a national cloud or
	// the fixture server. It has no field in Settings.
	EnvGraphURL = "ASSAY_DIRECTORY_GRAPH_URL"
)

// EnvVars are the environment variables of this package.
var EnvVars = []string{EnvIssuer, EnvClientID, EnvClientSecret, EnvButtonLabel, EnvAccessGroup, EnvPassword,
	EnvDirectory, EnvInterval, EnvGraphURL}

// The interval of the directory poll in minutes.
const (
	MinInterval     = 5
	MaxInterval     = 1440
	DefaultInterval = 15
)

// SignInConfig is the sign-in setting.
type SignInConfig struct {
	Issuer          string `json:"issuer" doc:"The OpenID Connect issuer URL. Empty turns SSO off."`
	ClientID        string `json:"clientId"`
	ButtonLabel     string `json:"buttonLabel" doc:"The label of the SSO button. Empty uses the default label."`
	PasswordSignIn  bool   `json:"passwordSignIn"`
	AccessGroup     string `json:"accessGroup" doc:"The IdP group ID whose members can sign in. Empty lets every IdP user sign in."`
	AccessGroupName string `json:"accessGroupName"`
	Directory       string `json:"directory" enum:"none,entra"`
	IntervalMinutes int    `json:"intervalMinutes"`
}

// Setting is the definition of the sign-in setting.
var Setting = settings.Define("signin", "Sign-in with the identity provider, password sign-in and the directory sync.",
	SignInConfig{PasswordSignIn: true, Directory: DirectoryNone, IntervalMinutes: DefaultInterval}, SignInConfig.check)

func (c SignInConfig) check() error {
	if err := checkIssuer(c.Issuer); err != nil {
		return err
	}
	if len([]rune(c.ButtonLabel)) > 60 {
		return errors.New("the button label must be 60 characters or fewer")
	}
	if c.Directory != DirectoryNone && c.Directory != DirectoryEntra {
		return errors.New("the directory must be none or entra")
	}
	if c.IntervalMinutes < MinInterval || c.IntervalMinutes > MaxInterval {
		return fmt.Errorf("the poll interval must be %d to %d minutes", MinInterval, MaxInterval)
	}
	return nil
}

// checkIssuer accepts an empty issuer, an HTTPS URL and an HTTP URL of a
// loopback host, as auth-all does.
func checkIssuer(issuer string) error {
	if issuer == "" {
		return nil
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return errors.New("the issuer must be a URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	h := u.Hostname()
	if u.Scheme == "http" && (h == "localhost" || h == "127.0.0.1" || h == "::1") {
		return nil
	}
	return errors.New("the issuer must use HTTPS")
}

// CheckEnv validates the environment variables of this package.
func CheckEnv(env map[string]string) error {
	var errs []error
	if v, ok := env[EnvPassword]; ok {
		if _, err := strconv.ParseBool(v); err != nil {
			errs = append(errs, fmt.Errorf("%s is %q. Set it to true or false", EnvPassword, v))
		}
	}
	if v, ok := env[EnvDirectory]; ok && v != DirectoryNone && v != DirectoryEntra {
		errs = append(errs, fmt.Errorf("%s is %q. Set it to none or entra", EnvDirectory, v))
	}
	if v, ok := env[EnvInterval]; ok {
		if n, err := strconv.Atoi(v); err != nil || n < MinInterval || n > MaxInterval {
			errs = append(errs, fmt.Errorf("%s is %q. Set it to a number of minutes from %d to %d", EnvInterval, v, MinInterval, MaxInterval))
		}
	}
	if v, ok := env[EnvIssuer]; ok {
		if err := checkIssuer(v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvIssuer, err))
		}
	}
	if v, ok := env[EnvGraphURL]; ok {
		if u, err := url.Parse(v); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			errs = append(errs, fmt.Errorf("%s must be an http or https URL", EnvGraphURL))
		}
	}
	return errors.Join(errs...)
}

// Effective is the sign-in configuration after the environment variables.
type Effective struct {
	SignInConfig
	ClientSecret string
	GraphURL     string
	// Locks maps a field to the environment variable that sets it.
	Locks map[string]string
}

// Ready reports whether SSO sign-in is configured.
func (e Effective) Ready() bool { return e.Issuer != "" && e.ClientID != "" && e.ClientSecret != "" }

// PasswordOn reports whether password sign-in is served. It stays on while
// SSO is not configured, so that nobody is locked out.
func (e Effective) PasswordOn() bool { return e.PasswordSignIn || !e.Ready() }

// DirectoryOn reports whether the directory sync runs.
func (e Effective) DirectoryOn() bool { return e.Directory == DirectoryEntra && e.Ready() }

// Service serves sign-in with the identity provider and the directory sync.
type Service struct {
	Pool     *pgxpool.Pool
	Secrets  *secrets.Store
	Auth     *auth.Auth
	Outbound outbound.Settings
	// Env holds the set environment variables of this package.
	Env     map[string]string
	BaseURL string
	River   *river.Client[pgx.Tx]
	Log     *slog.Logger
	Now     func() time.Time

	mu     sync.Mutex
	cached *cachedProvider
}

type cachedProvider struct {
	key string
	p   oauth.Provider
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Effective reads the stored setting and the secret, and applies the
// environment variables.
func (s *Service) Effective(ctx context.Context, db settings.DBTX) (Effective, error) {
	c, err := settings.Read[SignInConfig](ctx, db, Setting)
	if err != nil {
		return Effective{}, err
	}
	return s.effective(ctx, c)
}

func (s *Service) effective(ctx context.Context, c SignInConfig) (Effective, error) {
	e := Effective{SignInConfig: c, GraphURL: entra.GraphURL, Locks: map[string]string{}}
	str := func(name, field string, dst *string) {
		if v, ok := s.Env[name]; ok {
			*dst = v
			e.Locks[field] = name
		}
	}
	str(EnvIssuer, "issuer", &e.Issuer)
	str(EnvClientID, "clientId", &e.ClientID)
	str(EnvButtonLabel, "buttonLabel", &e.ButtonLabel)
	str(EnvDirectory, "directory", &e.Directory)
	str(EnvGraphURL, "graphUrl", &e.GraphURL)
	if v, ok := s.Env[EnvAccessGroup]; ok {
		e.AccessGroup, e.AccessGroupName = v, v
		e.Locks["accessGroup"] = EnvAccessGroup
	}
	if v, ok := s.Env[EnvPassword]; ok {
		e.PasswordSignIn, _ = strconv.ParseBool(v)
		e.Locks["passwordSignIn"] = EnvPassword
	}
	if v, ok := s.Env[EnvInterval]; ok {
		e.IntervalMinutes, _ = strconv.Atoi(v)
		e.Locks["intervalMinutes"] = EnvInterval
	}
	e.Issuer = strings.TrimRight(e.Issuer, "/")
	if v, ok := s.Env[EnvClientSecret]; ok {
		e.ClientSecret = v
		e.Locks["clientSecret"] = EnvClientSecret
		return e, nil
	}
	secret, err := s.Secrets.Get(ctx, SecretName)
	if err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return Effective{}, err
	}
	e.ClientSecret = string(secret)
	return e, nil
}

// Guard checks a change of the sign-in setting. Password sign-in turns off
// only with SSO configured and one enabled admin with a linked SSO account,
// so that no change locks out every admin.
func (s *Service) Guard(ctx context.Context, db settings.DBTX, raw json.RawMessage) error {
	var c SignInConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return api.Problem(http.StatusUnprocessableEntity, "invalid_value", err.Error())
	}
	e, err := s.effective(ctx, c)
	if err != nil {
		return err
	}
	if !e.PasswordSignIn {
		if !e.Ready() {
			return api.Problem(http.StatusConflict, "sso_not_ready",
				"Set the issuer, the client ID and the client secret before you turn off password sign-in.")
		}
		linked, err := New(db).AdminLinked(ctx, ProviderID)
		if err != nil {
			return err
		}
		if !linked {
			return api.Problem(http.StatusConflict, "no_sso_admin",
				"Password sign-in stays on until an admin signs in with SSO. An admin must not lock out all admins.")
		}
	}
	if e.Directory == DirectoryEntra {
		if e.Issuer == "" {
			return api.Problem(http.StatusConflict, "sso_not_ready", "Set the issuer before you turn on the directory sync.")
		}
		if _, err := entra.TokenURLFromIssuer(e.Issuer); err != nil {
			return api.Problem(http.StatusUnprocessableEntity, "invalid_value",
				"The Entra ID directory needs an Entra ID issuer, such as https://login.microsoftonline.com/<tenant>/v2.0.")
		}
	}
	return nil
}

// Providers is the auth-all provider source. It returns the OpenID Connect
// provider with the ID sso when SSO is configured. A change in Settings
// applies to the next sign-in with no restart.
func (s *Service) Providers(ctx context.Context) ([]oauth.Provider, error) {
	e, err := s.Effective(ctx, s.Pool)
	if err != nil {
		return nil, err
	}
	if !e.Ready() {
		return nil, nil
	}
	sum := sha256.Sum256([]byte(e.Issuer + "\x00" + e.ClientID + "\x00" + e.ClientSecret))
	key := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.cached.key == key {
		return []oauth.Provider{s.cached.p}, nil
	}
	// The discovery document names the token and key endpoints, which can
	// be on another host of the issuer.
	h, err := outbound.New(s.Outbound, outbound.Options{BaseURL: e.Issuer, AnyHost: true, Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	p := oidc.New(oidc.WithID(ProviderID), oidc.WithIssuer(e.Issuer), oidc.WithClientID(e.ClientID),
		oidc.WithClientSecret(e.ClientSecret), oidc.WithScopes("openid", "email", "profile"), oidc.WithHTTPClient(h))
	s.cached = &cachedProvider{key: key, p: p}
	return []oauth.Provider{p}, nil
}

// PasswordOn reports whether password sign-in is served now.
func (s *Service) PasswordOn(ctx context.Context) (bool, error) {
	e, err := s.Effective(ctx, s.Pool)
	if err != nil {
		return false, err
	}
	return e.PasswordOn(), nil
}

// ErrNoAccess refuses a sign-in of a user outside the access group.
var ErrNoAccess = apierr.New("SSO_ACCESS_DENIED", http.StatusForbidden, "Your organization has not given you access to Assay.")

// Install registers the sign-in hooks on auth-all.
func (s *Service) Install(a *auth.Auth) {
	a.Hooks().OnBeforeUserCreate(func(ctx context.Context, ev *hook.UserCreate) error {
		if ev.Provider != ProviderID || ev.Identity == nil {
			return nil
		}
		e, err := s.Effective(ctx, s.Pool)
		if err != nil {
			return err
		}
		return allowed(e, ev.Identity)
	})
	a.Hooks().OnBeforeOAuthSignIn(func(ctx context.Context, ev *hook.OAuthSignIn) error {
		if ev.Provider != ProviderID || ev.Identity == nil || ev.User == nil {
			return nil
		}
		return s.signIn(ctx, ev.User.ID, ev.Identity)
	})
}

// allowed refuses an identity outside the access group.
func allowed(e Effective, id *oauth.Identity) error {
	if e.AccessGroup != "" && !slices.Contains(id.Claims.Strings("groups"), e.AccessGroup) {
		return ErrNoAccess
	}
	return nil
}

// subject is the directory object ID of an identity. Entra ID sends it as
// oid, because its sub differs per application. Another issuer gives sub.
func subject(id *oauth.Identity) string {
	if v, ok := id.Claims.Get("oid"); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return id.ProviderAccountID
}

// signIn checks the access group, records the directory user and sets the
// members of the mapped groups from the groups claim.
func (s *Service) signIn(ctx context.Context, userID string, id *oauth.Identity) error {
	e, err := s.Effective(ctx, s.Pool)
	if err != nil {
		return err
	}
	if err := allowed(e, id); err != nil {
		return err
	}
	groups := id.Claims.Strings("groups")
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		if err := q.UpsertDirectoryUser(ctx, UpsertDirectoryUserParams{UserID: userID, Provider: ProviderID, Subject: subject(id), Now: s.now()}); err != nil {
			return err
		}
		maps, err := q.ListMappings(ctx, ProviderID)
		if err != nil {
			return err
		}
		for _, m := range maps {
			if err := setMember(ctx, tx, m.GroupID, userID, slices.Contains(groups, m.ExternalID), "sso"); err != nil {
				return err
			}
		}
		return nil
	})
}

// setMember adds a user to a mapped group or removes the user, and writes
// one audit entry for a change.
func setMember(ctx context.Context, tx pgx.Tx, groupID int64, userID string, member bool, source string) error {
	q := New(tx)
	var n int64
	var err error
	action := "group.member_add"
	if member {
		n, err = q.AddMember(ctx, AddMemberParams{GroupID: groupID, UserID: userID})
	} else {
		action = "group.member_remove"
		n, err = q.RemoveMember(ctx, RemoveMemberParams{GroupID: groupID, UserID: userID})
	}
	if err != nil || n == 0 {
		return err
	}
	return audit.Write(ctx, tx, audit.Entry{Action: action, Target: fmt.Sprintf("group:%d", groupID),
		Details: map[string]any{"user": userID, "source": source}})
}

// AdminStatus clears the sync mark of a user after an admin changes the
// state of the user. A disable by an admin then stays.
func (s *Service) AdminStatus(ctx context.Context, userID string) error {
	return New(s.Pool).SetDisabledBySync(ctx, SetDisabledBySyncParams{UserID: userID, DisabledBySync: false})
}

// directoryProvider returns the directory of the configuration.
func (s *Service) directoryProvider(e Effective) (directory.Provider, error) {
	tokenURL, err := entra.TokenURLFromIssuer(e.Issuer)
	if err != nil {
		return nil, err
	}
	return directory.NewEntra(s.Outbound, directory.EntraOptions{GraphURL: e.GraphURL, TokenURL: tokenURL,
		ClientID: e.ClientID, ClientSecret: e.ClientSecret})
}
