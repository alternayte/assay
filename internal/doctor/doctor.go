// Package doctor checks the installation: the database, the secret key, the
// proxy and CA settings, and the reachability and TLS of each endpoint and
// connection. assay doctor and the Diagnostics page show the same checks.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/secrets"
)

// Check is the result of one check.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason" doc:"What the check found."`
}

// Doctor runs the checks.
type Doctor struct {
	Pool     *pgxpool.Pool
	Secrets  *secrets.Store
	Models   *models.Gateway
	Outbound outbound.Settings
}

// Run runs every check. It never stops at the first failure.
func (d *Doctor) Run(ctx context.Context) []Check {
	var out []Check
	add := func(name string, err error, ok string) {
		if err != nil {
			out = append(out, Check{Name: name, Reason: err.Error()})
			return
		}
		out = append(out, Check{Name: name, OK: true, Reason: ok})
	}

	var version int
	var versionText string
	err := d.Pool.QueryRow(ctx, "SELECT current_setting('server_version_num')::int, current_setting('server_version')").Scan(&version, &versionText)
	if err == nil && version < 160000 {
		err = fmt.Errorf("PostgreSQL %s is too old. Use PostgreSQL 16 or later", versionText)
	}
	add("Database version", err, "PostgreSQL "+versionText)

	var missing []string
	rows, err := d.Pool.Query(ctx, `SELECT e FROM unnest(ARRAY['vector','pg_trgm','unaccent']) AS e
		WHERE NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = e)`)
	if err == nil {
		for rows.Next() {
			var e string
			if err = rows.Scan(&e); err != nil {
				break
			}
			missing = append(missing, e)
		}
		rows.Close()
	}
	if err == nil && len(missing) > 0 {
		err = fmt.Errorf("missing extensions: %s", strings.Join(missing, ", "))
	}
	add("Database extensions", err, "vector, pg_trgm and unaccent are installed")

	bad, n, err := d.Secrets.Check(ctx)
	if err == nil && len(bad) > 0 {
		err = fmt.Errorf("%d of %d secrets do not decrypt with ASSAY_SECRET_KEY: %s", len(bad), n, strings.Join(bad, ", "))
	}
	add("Secret key", err, fmt.Sprintf("all %d secrets decrypt", n))

	add("Proxy", checkProxy(d.Outbound), describeProxy(d.Outbound))

	_, err = d.Outbound.RootCAs()
	caText := "the system certificates only"
	if d.Outbound.ExtraCAPEM != "" || d.Outbound.ExtraCAFile != "" {
		caText = "the system certificates and the extra CA certificates"
	}
	add("CA certificates", err, caText)

	eps, err := d.Models.Endpoints(ctx)
	if err != nil {
		add("Model endpoints", err, "")
	}
	for _, e := range eps {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		n, err := d.Models.Reach(cctx, e.ID)
		cancel()
		add("Endpoint "+e.Name, err, fmt.Sprintf("%s answers and lists %d models", e.BaseURL, n))
	}
	return out
}

func checkProxy(s outbound.Settings) error {
	for name, v := range map[string]string{"HTTP_PROXY": s.HTTPProxy, "HTTPS_PROXY": s.HTTPSProxy} {
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil || u.Host == "" {
			return fmt.Errorf("%s is not a valid URL", name)
		}
	}
	return nil
}

func describeProxy(s outbound.Settings) string {
	if s.HTTPProxy == "" && s.HTTPSProxy == "" {
		return "no proxy"
	}
	strip := func(v string) string {
		u, err := url.Parse(v)
		if err != nil {
			return "set"
		}
		u.User = nil
		return u.String()
	}
	parts := []string{}
	if s.HTTPSProxy != "" {
		parts = append(parts, "HTTPS through "+strip(s.HTTPSProxy))
	}
	if s.HTTPProxy != "" {
		parts = append(parts, "HTTP through "+strip(s.HTTPProxy))
	}
	if s.NoProxy != "" {
		parts = append(parts, "no proxy for "+s.NoProxy)
	}
	return strings.Join(parts, "; ")
}

// Failed reports whether a check failed.
func Failed(checks []Check) bool {
	for _, c := range checks {
		if !c.OK {
			return true
		}
	}
	return false
}

// Register adds the diagnostics route.
func (d *Doctor) Register(a huma.API) {
	type out struct {
		Body struct {
			Checks []Check `json:"checks"`
			OK     bool    `json:"ok"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "runDiagnostics", Method: http.MethodPost, Path: "/api/v1/diagnostics", Tags: []string{"diagnostics"},
		Summary: "Run the installation checks", Middlewares: huma.Middlewares{api.Require(auth.RoleAdmin)},
	}, func(ctx context.Context, _ *struct{}) (*out, error) {
		checks := d.Run(ctx)
		if ctx.Err() != nil {
			return nil, api.Internal(errors.New("diagnostics: the request ended"))
		}
		o := &out{}
		o.Body.Checks = checks
		o.Body.OK = !Failed(checks)
		return o, nil
	})
}
