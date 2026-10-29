// Package sync owns connections and their sync runs: incremental sync,
// reconciliation, pause, resume and purge. Every run reads through a
// connector and writes through the content package.
package sync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/azdo"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/connectors/github"
	"github.com/alternayte/assay/internal/connectors/jira"
	"github.com/alternayte/assay/internal/connectors/tree"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/secrets"
	"github.com/alternayte/assay/internal/settings"
)

// RulesConfig is the connector rules setting.
type RulesConfig struct {
	InitiativeTypes []string `json:"initiativeTypes" doc:"Jira issue types that become initiatives."`
	BlockLinkType   string   `json:"blockLinkType" doc:"The Jira link type that creates blocks claims."`
	DecisionLabels  []string `json:"decisionLabels" doc:"Confluence labels that make a page a decision."`
	GlossaryLabels  []string `json:"glossaryLabels" doc:"Confluence labels of glossary pages."`
	DefaultGlobs    []string `json:"defaultGlobs" doc:"Include globs of a new repository connection."`
	MaxFileKB       int      `json:"maxFileKb" minimum:"1" doc:"Items larger than this are skipped."`
	MaxFilesPerRepo int      `json:"maxFilesPerRepo" minimum:"1"`
}

// Rules is the definition of the connector rules setting.
var Rules = settings.Define("connectors", "Rules that connectors use to read sources.",
	RulesConfig{InitiativeTypes: []string{"Epic", "Initiative"}, BlockLinkType: "Blocks", DecisionLabels: []string{"decision", "adr"},
		GlossaryLabels: []string{"glossary"}, DefaultGlobs: tree.DefaultGlobs, MaxFileKB: 512, MaxFilesPerRepo: 2000},
	func(c RulesConfig) error {
		if c.MaxFileKB < 1 || c.MaxFilesPerRepo < 1 {
			return errors.New("the limits must be 1 or more")
		}
		if c.BlockLinkType == "" {
			return errors.New("set the block link type")
		}
		return nil
	})

// Metrics are the sync metrics.
type Metrics struct {
	Runs  *prometheus.CounterVec
	Items *prometheus.CounterVec
}

// NewMetrics returns the sync metrics.
func NewMetrics() *Metrics {
	return &Metrics{
		Runs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "assay_sync_runs_total",
			Help: "Finished sync runs by kind and status."}, []string{"kind", "status"}),
		Items: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "assay_sync_items_total",
			Help: "Items that sync runs handled, by outcome."}, []string{"outcome"}),
	}
}

// Collectors returns the metrics to register.
func (m *Metrics) Collectors() []prometheus.Collector { return []prometheus.Collector{m.Runs, m.Items} }

// Service runs connections.
type Service struct {
	Pool     *pgxpool.Pool
	Secrets  *secrets.Store
	Outbound outbound.Settings
	Log      *slog.Logger
	Now      func() time.Time
	Metrics  *Metrics
	// River inserts the run jobs. It is set after the client exists.
	River *river.Client[pgx.Tx]
	// Backoff is the first retry delay of the connector clients. Zero
	// means 1 second. Tests set it.
	Backoff time.Duration
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Kinds are the connection kinds that an admin can create.
var Kinds = []string{"confluence", "jira", "github", "azdo"}

// source is what a connector needs to read a source.
type source struct {
	Kind        string
	BaseURL     string
	Token       string
	Scope       json.RawMessage
	Options     json.RawMessage
	Include     []string
	Exclude     []string
	Concurrency int
	PerSecond   float64
}

// open returns the connector of a source.
func (s *Service) open(ctx context.Context, src source) (connectors.Connector, error) {
	rules, err := settings.Read[RulesConfig](ctx, s.Pool, Rules)
	if err != nil {
		return nil, err
	}
	authorization := "Bearer " + src.Token
	if src.Kind == "azdo" {
		// Azure DevOps Server takes a personal access token as the
		// password of Basic authentication.
		authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+src.Token))
	}
	c, err := connectors.NewClient(s.Outbound, connectors.ClientOptions{BaseURL: src.BaseURL, Authorization: authorization,
		Concurrency: src.Concurrency, PerSecond: src.PerSecond})
	if err != nil {
		return nil, err
	}
	if s.Backoff > 0 {
		c.Backoff = s.Backoff
	}
	r := connectors.Rules{InitiativeTypes: rules.InitiativeTypes, BlockLinkType: rules.BlockLinkType,
		DecisionLabels: rules.DecisionLabels, GlossaryLabels: rules.GlossaryLabels}
	limits := connectors.Limits{MaxItemBytes: rules.MaxFileKB << 10, MaxFilesPerRepo: rules.MaxFilesPerRepo}
	switch src.Kind {
	case "confluence":
		return confluence.New(c, src.Scope, r, limits)
	case "jira":
		return jira.New(c, src.Scope, r, limits)
	}
	globs := tree.Globs{Include: src.Include, Exclude: src.Exclude}
	if len(globs.Include) == 0 {
		globs.Include = rules.DefaultGlobs
	}
	switch src.Kind {
	case "github":
		return github.New(c, src.Scope, globs, limits)
	case "azdo":
		return azdo.New(c, src.Scope, src.Options, globs, limits)
	}
	return nil, fmt.Errorf("sync: connections of kind %s cannot sync", src.Kind)
}

func secretName(id int64) string { return fmt.Sprintf("connection:%d:token", id) }

// sourceOf returns the source of a stored connection with its token.
func (s *Service) sourceOf(ctx context.Context, c Connection) (source, error) {
	src := source{Kind: c.Kind, BaseURL: c.BaseUrl, Scope: c.Scope, Options: c.Options, Include: c.IncludeGlobs, Exclude: c.ExcludeGlobs,
		Concurrency: int(c.RateConcurrency), PerSecond: c.RatePerSecond}
	if c.SecretID != nil {
		tok, err := s.Secrets.GetByID(ctx, s.Pool, *c.SecretID)
		if err != nil {
			return src, fmt.Errorf("sync: read the token of the connection: %w", err)
		}
		src.Token = string(tok)
	}
	return src, nil
}

// sourceError turns a connector error into a message for people. It never
// contains the token.
func sourceError(err error) string {
	var se *connectors.StatusError
	var skip *connectors.SkipError
	switch {
	case errors.Is(err, connectors.ErrUnauthorized) && errors.As(err, &se):
		return fmt.Sprintf("The source refused the token with status %d. Check the token and its permissions.", se.Status)
	case errors.Is(err, connectors.ErrNotFound):
		return "The source returned 404. Check the base URL."
	case errors.As(err, &se):
		return fmt.Sprintf("The source answered %d (%s).", se.Status, http.StatusText(se.Status))
	case errors.Is(err, errPaused):
		return "The connection is paused. Resume it to sync."
	case errors.As(err, &skip):
		return "Skipped: " + skip.Reason
	case errors.Is(err, outbound.ErrForeignHost):
		return "The source sent a redirect to another host. Assay does not follow it."
	}
	return err.Error()
}

// Reach is the result of a reachability check of one connection.
type Reach struct {
	Name    string
	BaseURL string
	Result  connectors.Validation
	Err     error
}

// ReachAll tests each connection except the registry: reachability, TLS
// and the token.
func (s *Service) ReachAll(ctx context.Context) ([]Reach, error) {
	rows, err := New(s.Pool).ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	var out []Reach
	for _, r := range rows {
		if r.Kind == "registry" {
			continue
		}
		c := Connection{ID: r.ID, Kind: r.Kind, BaseUrl: r.BaseUrl, SecretID: r.SecretID, Scope: r.Scope, Options: r.Options,
			IncludeGlobs: r.IncludeGlobs, ExcludeGlobs: r.ExcludeGlobs,
			RateConcurrency: r.RateConcurrency, RatePerSecond: r.RatePerSecond}
		res := Reach{Name: r.Name, BaseURL: r.BaseUrl}
		src, err := s.sourceOf(ctx, c)
		if err == nil {
			var k connectors.Connector
			if k, err = s.open(ctx, src); err == nil {
				cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				res.Result, err = k.Validate(cctx)
				cancel()
			}
		}
		if err != nil {
			res.Err = errors.New(sourceError(err))
		}
		out = append(out, res)
	}
	return out, nil
}
