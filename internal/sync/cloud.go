package sync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/connectors/jira"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/visibility"
)

// The deployments of a Jira or Confluence connection.
const (
	deployDC    = "dc"
	deployCloud = "cloud"
)

// DefaultAtlassianAPI is the API host of scoped Atlassian Cloud tokens.
const DefaultAtlassianAPI = "https://api.atlassian.com"

// Rate defaults of a connection. Atlassian Cloud allows bursts of 100
// requests per second per endpoint for API tokens.
const (
	defaultPerSecond      = 5
	defaultCloudPerSecond = 10
)

// atlassian reports a kind with a deployment: Data Center or Cloud.
func atlassian(kind string) bool { return kind == "jira" || kind == "confluence" }

// cloudOptions are the options of an Atlassian connection.
type cloudOptions struct {
	Deployment string `json:"deployment"`
	Email      string `json:"email"`
	Scoped     bool   `json:"scoped"`
	CloudID    string `json:"cloudId"`
}

func cloudOptionsOf(raw []byte) cloudOptions {
	var o cloudOptions
	_ = json.Unmarshal(raw, &o)
	return o
}

// deploymentOf is the deployment of an Atlassian connection. A site URL
// that ends in .atlassian.net is Cloud when the options name none.
func deploymentOf(kind, baseURL string, options []byte) string {
	if !atlassian(kind) {
		return ""
	}
	if d := cloudOptionsOf(options).Deployment; d == deployDC || d == deployCloud {
		return d
	}
	if u, err := url.Parse(baseURL); err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".atlassian.net") {
		return deployCloud
	}
	return deployDC
}

func hostOf(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// atlassianOptions completes the options of an Atlassian connection before
// a test or a save: the deployment, and for Cloud the email and the cloud
// ID of a scoped token, which Assay reads from the tenant info of the site.
// Other kinds keep their options.
func (s *Service) atlassianOptions(ctx context.Context, kind, baseURL string, options map[string]any) (map[string]any, error) {
	if !atlassian(kind) {
		return options, nil
	}
	out := map[string]any{}
	for k, v := range options {
		out[k] = v
	}
	if d, _ := out["deployment"].(string); d != "" && d != deployDC && d != deployCloud {
		return nil, api.Problem(http.StatusUnprocessableEntity, "invalid_deployment", "The deployment must be dc or cloud.")
	}
	out["deployment"] = deploymentOf(kind, baseURL, rawJSON(out))
	if out["deployment"] == deployDC {
		delete(out, "email")
		delete(out, "scoped")
		delete(out, "cloudId")
		return out, nil
	}
	email, _ := out["email"].(string)
	if email = strings.TrimSpace(email); email == "" || !strings.Contains(email, "@") {
		return nil, api.Problem(http.StatusUnprocessableEntity, "email_required", "Enter the email of the Atlassian account of the API token.")
	}
	out["email"] = email
	scoped, _ := out["scoped"].(bool)
	out["scoped"] = scoped
	delete(out, "cloudId")
	if scoped {
		id, err := s.cloudID(ctx, baseURL)
		if err != nil {
			return nil, api.Problem(http.StatusBadGateway, "cloud_id",
				fmt.Sprintf("Assay cannot read the cloud ID from %s/_edge/tenant_info: %s", strings.TrimRight(baseURL, "/"), sourceError(err)))
		}
		out["cloudId"] = id
	}
	return out, nil
}

// cloudID reads the cloud ID of an Atlassian Cloud site from its tenant
// info. The request carries no token.
func (s *Service) cloudID(ctx context.Context, site string) (string, error) {
	c, err := connectors.NewClient(s.Outbound, connectors.ClientOptions{BaseURL: site, Concurrency: 1, PerSecond: defaultCloudPerSecond})
	if err != nil {
		return "", err
	}
	if s.Backoff > 0 {
		c.Backoff = s.Backoff
	}
	var info struct {
		CloudID string `json:"cloudId"`
	}
	if err := c.GetJSON(ctx, "/_edge/tenant_info", nil, &info); err != nil {
		return "", err
	}
	if strings.TrimSpace(info.CloudID) == "" {
		return "", errors.New("the tenant info has no cloud ID")
	}
	return info.CloudID, nil
}

// DefaultAtlassianMedia is the media host of Atlassian Cloud downloads.
const DefaultAtlassianMedia = "https://api.media.atlassian.com"

func (s *Service) atlassianMedia() string {
	if s.AtlassianMedia != "" {
		return strings.TrimRight(s.AtlassianMedia, "/")
	}
	return DefaultAtlassianMedia
}

func (s *Service) atlassianAPI() string {
	if s.AtlassianAPI != "" {
		return strings.TrimRight(s.AtlassianAPI, "/")
	}
	return DefaultAtlassianAPI
}

// openCloud returns the connector of an Atlassian Cloud source. It sends
// Basic authentication with the email and the API token. A scoped token
// reads through the API host, and item links keep the site URL.
func (s *Service) openCloud(ctx context.Context, src source, r connectors.Rules, limits connectors.Limits) (connectors.Connector, error) {
	o := cloudOptionsOf(src.Options)
	if strings.TrimSpace(o.Email) == "" {
		return nil, errors.New("sync: the Cloud connection has no email")
	}
	opts := connectors.ClientOptions{BaseURL: src.BaseURL,
		Authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(o.Email)+":"+src.Token)),
		Concurrency:   src.Concurrency, PerSecond: src.PerSecond, Pool: s.Pool, ConnectionID: src.ConnectionID}
	if src.Kind == "confluence" {
		// A download of an attachment redirects to the media host. The
		// client follows that redirect and sends no credentials there.
		opts.AlsoURLs = []string{s.atlassianMedia()}
	}
	if o.Scoped {
		id := o.CloudID
		if id == "" {
			var err error
			if id, err = s.cloudID(ctx, src.BaseURL); err != nil {
				return nil, err
			}
		}
		opts.BaseURL, opts.SiteURL = s.atlassianAPI()+"/ex/"+src.Kind+"/"+url.PathEscape(id), src.BaseURL
	}
	c, err := connectors.NewClient(s.Outbound, opts)
	if err != nil {
		return nil, err
	}
	if s.Backoff > 0 {
		c.Backoff = s.Backoff
	}
	if src.Kind == "jira" {
		return jira.NewCloud(c, src.Scope, r, limits)
	}
	return confluence.NewCloud(c, src.Scope, r, limits)
}

// Rewrite is the key rewrite of the first sync after a switch to Cloud.
type Rewrite struct {
	From      string `json:"from" doc:"The Data Center host."`
	To        string `json:"to" doc:"The Cloud host."`
	Items     int32  `json:"items" doc:"Stored items that matched a Cloud item."`
	Entities  int32  `json:"entities" doc:"Entity keys that got the Cloud host."`
	People    int32  `json:"people" doc:"Data Center people that matched a Cloud account by email."`
	Unmatched int32  `json:"unmatched" doc:"Cloud people that matched no person. Each is a new person entity."`
}

// rewriteCounts are the counts of one part.
type rewriteCounts struct {
	Items     int32 `json:"items"`
	People    int32 `json:"people"`
	Unmatched int32 `json:"unmatched"`
}

func (c *rewriteCounts) add(o *rewriteCounts) {
	if o == nil {
		return
	}
	c.Items += o.Items
	c.People += o.People
	c.Unmatched += o.Unmatched
}

func rewriteOf(raw []byte) *Rewrite {
	var r Rewrite
	if json.Unmarshal(raw, &r) != nil || r.From == "" {
		return nil
	}
	return &r
}

// errSwitching fails a reconciliation before the first sync after a switch:
// it would tombstone the pages whose IDs the sync still rewrites.
var errSwitching = errors.New("sync: the connection switches to Cloud")

// system is the visible set of all connections. A key rewrite merges
// entities as the system.
func (s *Service) system(ctx context.Context, tx pgx.Tx) (visibility.Set, error) {
	ids, err := New(tx).AllConnectionIDs(ctx)
	return visibility.Set(ids), err
}

// rekey gives an entity a new key. When the key exists, the entity merges
// with the entity of the key.
func (s *Service) rekey(ctx context.Context, tx pgx.Tx, id int64, oldKey, newKey string) error {
	q := New(tx)
	other, err := q.EntityIDByKey(ctx, newKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.SetEntityKey(ctx, SetEntityKeyParams{ID: id, Key: newKey})
	}
	if err != nil {
		return err
	}
	if other == id {
		return nil
	}
	if s.Entities == nil {
		return errors.New("sync: the key rewrite needs the entity service to merge")
	}
	vis, err := s.system(ctx, tx)
	if err != nil {
		return err
	}
	_, _, err = s.Entities.MergeIDsTx(ctx, tx, vis, []int64{id, other}, entities.MergeRequest{Keys: []string{oldKey, newKey}, Auto: true})
	if errors.Is(err, entities.ErrAlreadyOne) {
		return nil
	}
	return err
}

// rewriteHosts gives every entity key of a connection that starts with
// <kind>:<from>: the host to, in tx. Term keys hold the host in lower case.
// It returns the number of rewritten keys.
func (s *Service) rewriteHosts(ctx context.Context, tx pgx.Tx, conn Connection, from, to string) (int32, error) {
	prefix := conn.Kind + ":" + from + ":"
	rows, err := New(tx).PrefixEntities(ctx, PrefixEntitiesParams{Prefix: prefix, ConnectionID: conn.ID})
	if err != nil {
		return 0, err
	}
	var n int32
	for _, e := range rows {
		host, rest := e.Key[len(conn.Kind)+1:len(prefix)-1], e.Key[len(prefix):]
		next := to
		if host != from {
			next = strings.ToLower(to)
		}
		key := conn.Kind + ":" + next + ":" + rest
		if key == e.Key {
			continue
		}
		if err := s.rekey(ctx, tx, e.ID, e.Key, key); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// rewriteBatch matches a batch of listed Cloud items with the stored items
// and people of the connection, in one transaction, before sync stores the
// batch. A Jira item keeps its key. A Confluence page with an unknown ID
// takes over the stored page with its space and title whose ID is not in
// Cloud, with its decision entity. A Cloud person takes over the Data
// Center person with its email.
func (s *Service) rewriteBatch(ctx context.Context, conn Connection, rw *Rewrite, part string, refs []connectors.Ref, cloudIDs []string,
	out *rewriteCounts) error {
	var c rewriteCounts
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		c = rewriteCounts{}
		q := New(tx)
		if err := q.LockRewrite(ctx, conn.ID); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, r := range refs {
			if _, err := q.ItemByExternalID(ctx, ItemByExternalIDParams{ConnectionID: conn.ID, ExternalID: r.ID}); err == nil {
				c.Items++
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			} else if conn.Kind == "confluence" && r.Item != nil {
				parent, _ := r.Item.Metadata["parentTitle"].(string)
				m, err := q.MatchPage(ctx, MatchPageParams{ConnectionID: conn.ID, Part: part, Title: r.Item.Title, CloudIds: cloudIDs,
					Attachment: strings.HasPrefix(r.ID, "att:"), ParentTitle: parent})
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				if err := q.SetExternalID(ctx, SetExternalIDParams{ID: m.ID, ExternalID: r.ID}); err != nil {
					return err
				}
				oldKey := "confluence:" + rw.To + ":" + m.ExternalID
				if id, err := q.EntityIDByKey(ctx, oldKey); err == nil {
					if err := s.rekey(ctx, tx, id, oldKey, "confluence:"+rw.To+":"+r.ID); err != nil {
						return err
					}
				} else if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				c.Items++
			}
			if conn.Kind != "jira" || r.Item == nil {
				continue
			}
			for _, p := range people(r.Item) {
				if seen[p.Key] {
					continue
				}
				seen[p.Key] = true
				if err := s.matchPerson(ctx, tx, conn, rw, p, &c); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil {
		out.add(&c)
	}
	return err
}

// matchPerson gives the Data Center person with the email of a Cloud person
// the key of the Cloud account. A Cloud person that is already stored needs
// nothing; one with no email or no match is unmatched, and the store makes
// it a new person.
func (s *Service) matchPerson(ctx context.Context, tx pgx.Tx, conn Connection, rw *Rewrite, p content.EntityRef, c *rewriteCounts) error {
	q := New(tx)
	if _, err := q.EntityIDByKey(ctx, p.Key); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	email, _ := p.Metadata["email"].(string)
	if email == "" {
		c.Unmatched++
		return nil
	}
	id, err := q.PersonByEmail(ctx, PersonByEmailParams{Email: email, Key: p.Key, Prefix: jira.PersonKey(rw.To, ""), ConnectionID: conn.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		c.Unmatched++
		return nil
	}
	if err != nil {
		return err
	}
	if err := q.SetEntityKey(ctx, SetEntityKeyParams{ID: id, Key: p.Key}); err != nil {
		return err
	}
	c.People++
	return nil
}

// people are the person entities that an item names.
func people(it *content.Item) []content.EntityRef {
	var out []content.EntityRef
	add := func(e *content.EntityRef) {
		if e != nil && e.Type == "person" {
			out = append(out, *e)
		}
	}
	add(it.Entity)
	for i := range it.Entities {
		add(&it.Entities[i])
	}
	for i := range it.Facts {
		add(&it.Facts[i].Subject)
		add(it.Facts[i].Object)
	}
	return out
}

// switchBody is a switch of a Data Center connection to Cloud.
type switchBody struct {
	BaseURL string `json:"baseUrl" format:"uri" doc:"The Cloud site URL, such as https://acme.atlassian.net."`
	Email   string `json:"email" minLength:"3" maxLength:"320" doc:"The email of the Atlassian account of the API token."`
	Token   string `json:"token" minLength:"1" maxLength:"4096" doc:"An API token of the account. Write-only."`
	Scoped  bool   `json:"scoped,omitempty" doc:"True for a scoped API token, which reads through api.atlassian.com."`
}

// registerSwitch adds the switch of a Data Center connection to Cloud.
func (s *Service) registerSwitch(a huma.API) {
	type switchIn struct {
		ID   int64 `path:"id"`
		Body switchBody
	}
	type switchOut struct {
		Body struct {
			RunID int64 `json:"runId" doc:"The full sync that rewrites the keys."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "switchConnectionToCloud", Method: http.MethodPost, Path: "/api/v1/connections/{id}/cloud", Tags: []string{"connections"},
		Summary: "Switch a Data Center connection to Cloud",
		Description: "Validates the Cloud site, email and API token, saves the old host as previousHost and queues a full sync. " +
			"The sync rewrites the keys of the items and entities of the connection, so claims, evidence, reviews and pages stay. " +
			"A reconciliation after it tombstones the items that matched nothing.",
		DefaultStatus: http.StatusAccepted, Middlewares: huma.Middlewares{api.Require(auth.RoleAdmin)},
	}, func(ctx context.Context, in *switchIn) (*switchOut, error) {
		id, err := s.switchToCloud(ctx, in.ID, in.Body)
		if err != nil {
			return nil, wrap(err)
		}
		out := &switchOut{}
		out.Body.RunID = id
		return out, nil
	})
}

// switchToCloud validates the Cloud source of a Data Center connection,
// saves it with the old host and queues the full sync that rewrites the
// keys.
func (s *Service) switchToCloud(ctx context.Context, id int64, b switchBody) (int64, error) {
	if err := checkURL(b.BaseURL); err != nil {
		return 0, err
	}
	cur, err := New(s.Pool).GetConnection(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, api.ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if !atlassian(cur.Kind) {
		return 0, api.Problem(http.StatusUnprocessableEntity, "not_atlassian", "Only a Jira or Confluence connection can switch to Cloud.")
	}
	if deploymentOf(cur.Kind, cur.BaseUrl, cur.Options) != deployDC {
		return 0, api.Problem(http.StatusConflict, "already_cloud", "The connection reads Cloud already.")
	}
	if cur.Status == "paused" {
		return 0, api.Problem(http.StatusConflict, "paused", "The connection is paused. Resume it first.")
	}
	site := strings.TrimRight(b.BaseURL, "/")
	opts := object(cur.Options)
	opts["deployment"], opts["email"], opts["scoped"] = deployCloud, strings.TrimSpace(b.Email), b.Scoped
	opts, err = s.atlassianOptions(ctx, cur.Kind, site, opts)
	if err != nil {
		return 0, err
	}
	from := hostOf(cur.BaseUrl)
	opts["previousHost"] = from
	src := source{Kind: cur.Kind, BaseURL: site, Token: b.Token, Scope: cur.Scope, Options: rawJSON(opts),
		Concurrency: int(cur.RateConcurrency), PerSecond: cur.RatePerSecond}
	k, err := s.open(ctx, src)
	if err != nil {
		return 0, sourceProblem(err)
	}
	if _, err := k.Validate(ctx); err != nil {
		return 0, sourceProblem(err)
	}
	var runID int64
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		q := New(tx)
		for _, kind := range []string{"sync", "reconcile"} {
			if _, err := q.ActiveRun(ctx, ActiveRunParams{ConnectionID: id, Kind: kind}); err == nil {
				return api.Problem(http.StatusConflict, "run_active", "A run is in the queue or running. Switch after it ends.")
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if n, err := q.SwitchConnection(ctx, SwitchConnectionParams{ID: id, BaseUrl: site, Options: rawJSON(opts), RewriteFrom: from}); err != nil {
			return err
		} else if n == 0 {
			return api.ErrNotFound
		}
		if err := s.setToken(ctx, tx, id, b.Token); err != nil {
			return err
		}
		// A full sync lists every item, so that each stored item meets
		// its Cloud item.
		if err := q.ClearCursors(ctx, id); err != nil {
			return err
		}
		var err error
		runID, err = s.queueTx(ctx, tx, id, "sync", "manual")
		return err
	})
	return runID, err
}
