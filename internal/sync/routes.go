package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/settings"
)

// GroupRef is a group that a connection is shared with.
type GroupRef struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Builtin     bool      `json:"builtin"`
	ConfirmedAt time.Time `json:"confirmedAt"`
}

// ConnectionRecord is a connection as admins see it: configuration and
// health. The token never leaves the server.
type ConnectionRecord struct {
	ID                   int64          `json:"id"`
	Kind                 string         `json:"kind" enum:"registry,confluence,jira,github,azdo"`
	Name                 string         `json:"name"`
	BaseURL              string         `json:"baseUrl"`
	HasToken             bool           `json:"hasToken"`
	Scope                map[string]any `json:"scope"`
	Options              map[string]any `json:"options"`
	IncludeGlobs         []string       `json:"includeGlobs"`
	ExcludeGlobs         []string       `json:"excludeGlobs"`
	SyncMinutes          int32          `json:"syncMinutes"`
	ReconcileHours       int32          `json:"reconcileHours"`
	RateConcurrency      int32          `json:"rateConcurrency"`
	RatePerSecond        float64        `json:"ratePerSecond"`
	OwnerID              *string        `json:"ownerId"`
	OwnerEmail           string         `json:"ownerEmail"`
	Status               string         `json:"status" enum:"active,paused,resuming"`
	StatusReason         string         `json:"statusReason"`
	DefaultDomainID      *int64         `json:"defaultDomainId"`
	Visibility           []GroupRef     `json:"visibility"`
	Items                int64          `json:"items"`
	LastSyncAt           *time.Time     `json:"lastSyncAt"`
	NextSyncAt           *time.Time     `json:"nextSyncAt"`
	LastReconcileAt      *time.Time     `json:"lastReconcileAt"`
	PermissionsCheckedAt *time.Time     `json:"permissionsCheckedAt"`
	LastError            string         `json:"lastError"`
	CreatedAt            time.Time      `json:"createdAt"`
}

// RunRecord is one sync run.
type RunRecord struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind" enum:"sync,reconcile"`
	Trigger    string     `json:"trigger" enum:"schedule,manual"`
	Status     string     `json:"status" enum:"queued,running,succeeded,failed"`
	Seen       int32      `json:"seen"`
	Created    int32      `json:"created"`
	Updated    int32      `json:"updated"`
	Unchanged  int32      `json:"unchanged"`
	Deleted    int32      `json:"deleted"`
	Skipped    []Skip     `json:"skipped"`
	Error      string     `json:"error"`
	QueuedAt   time.Time  `json:"queuedAt"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// sourceBody is the source part of a connection: what the test and the
// scope pickers need.
type sourceBody struct {
	Kind         string         `json:"kind" enum:"confluence,jira,github,azdo"`
	BaseURL      string         `json:"baseUrl" format:"uri" doc:"For Confluence and Jira, the URL before /rest, such as https://wiki.acme.test. For GitHub, https://api.github.com or https://<host>/api/v3. For Azure DevOps Server, the collection URL."`
	Token        string         `json:"token,omitempty" maxLength:"4096" doc:"A read-only personal access token. Write-only. Empty keeps the stored token."`
	ConnectionID *int64         `json:"connectionId,omitempty" doc:"Use the stored token of this connection when token is empty."`
	Scope        map[string]any `json:"scope,omitempty"`
	Options      map[string]any `json:"options,omitempty"`
	IncludeGlobs []string       `json:"includeGlobs,omitempty" doc:"Repository files to read. Empty uses the default globs."`
	ExcludeGlobs []string       `json:"excludeGlobs,omitempty"`
}

type connectionBody struct {
	Kind            string         `json:"kind" enum:"confluence,jira,github,azdo"`
	BaseURL         string         `json:"baseUrl" format:"uri"`
	Token           string         `json:"token,omitempty" maxLength:"4096" doc:"A read-only personal access token. Write-only. Empty keeps the stored token."`
	Scope           map[string]any `json:"scope,omitempty"`
	Options         map[string]any `json:"options,omitempty"`
	Name            string         `json:"name" minLength:"1" maxLength:"100"`
	IncludeGlobs    []string       `json:"includeGlobs,omitempty"`
	ExcludeGlobs    []string       `json:"excludeGlobs,omitempty"`
	SyncMinutes     int32          `json:"syncMinutes" minimum:"5" maximum:"10080" default:"30"`
	ReconcileHours  int32          `json:"reconcileHours" minimum:"1" maximum:"720" default:"24"`
	RateConcurrency int32          `json:"rateConcurrency" minimum:"1" maximum:"32" default:"4"`
	RatePerSecond   float64        `json:"ratePerSecond" minimum:"0.1" maximum:"100" default:"5"`
	DefaultDomainID *int64         `json:"defaultDomainId,omitempty"`
}

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return api.Problem(http.StatusUnprocessableEntity, "invalid_url", "The base URL must be an http or https URL.")
	}
	return nil
}

func rawJSON(m map[string]any) []byte {
	if m == nil {
		return []byte("{}")
	}
	b, _ := json.Marshal(m)
	return b
}

func object(b []byte) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	return m
}

func orStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// resolve returns the source of a request body, with the stored token when
// the body has none and names the stored kind and base URL.
func (s *Service) resolve(ctx context.Context, b sourceBody) (source, error) {
	if err := checkURL(b.BaseURL); err != nil {
		return source{}, err
	}
	src := source{Kind: b.Kind, BaseURL: strings.TrimRight(b.BaseURL, "/"), Token: b.Token, Scope: rawJSON(b.Scope),
		Options: rawJSON(b.Options), Include: b.IncludeGlobs, Exclude: b.ExcludeGlobs, Concurrency: 4, PerSecond: 5}
	if src.Token == "" && b.ConnectionID != nil {
		c, err := New(s.Pool).GetConnection(ctx, *b.ConnectionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return source{}, api.ErrNotFound
		}
		if err != nil {
			return source{}, err
		}
		stored, err := s.sourceOf(ctx, c)
		if err != nil {
			return source{}, err
		}
		// The stored token goes only to the host that it belongs to (SI-04).
		if stored.Kind == src.Kind && stored.BaseURL == src.BaseURL {
			src.Token = stored.Token
		}
	}
	if src.Token == "" {
		return source{}, api.Problem(http.StatusUnprocessableEntity, "token_required", "Enter a personal access token.")
	}
	return src, nil
}

func sourceProblem(err error) error {
	var e *api.Error
	if errors.As(err, &e) {
		return err
	}
	return api.Problem(http.StatusBadGateway, "source_error", sourceError(err))
}

// Register adds the connection routes.
func (s *Service) Register(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}
	tags := []string{"connections"}

	type listOut struct {
		Body struct {
			Connections  []ConnectionRecord `json:"connections"`
			Kinds        []string           `json:"kinds" doc:"The kinds that an admin can create."`
			DefaultGlobs []string           `json:"defaultGlobs" doc:"The include globs of a new repository connection."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listConnections", Method: http.MethodGet, Path: "/api/v1/connections", Tags: tags,
		Summary: "List connections with their health", Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*listOut, error) {
		recs, err := s.records(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		rules, err := settings.Read[RulesConfig](ctx, s.Pool, Rules)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listOut{}
		out.Body.Connections = recs
		out.Body.Kinds = Kinds
		out.Body.DefaultGlobs = rules.DefaultGlobs
		return out, nil
	})

	type idIn struct {
		ID int64 `path:"id"`
	}
	type oneOut struct{ Body ConnectionRecord }
	huma.Register(a, huma.Operation{
		OperationID: "getConnection", Method: http.MethodGet, Path: "/api/v1/connections/{id}", Tags: tags,
		Summary: "Get a connection with its health", Middlewares: admins,
	}, func(ctx context.Context, in *idIn) (*oneOut, error) {
		rec, err := s.record(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		return &oneOut{Body: *rec}, nil
	})

	type testOut struct{ Body connectors.Validation }
	huma.Register(a, huma.Operation{
		OperationID: "testConnection", Method: http.MethodPost, Path: "/api/v1/connections/test", Tags: tags,
		Summary: "Test a source", Description: "Shows the source user of the token and the number of items in scope.", Middlewares: admins,
	}, func(ctx context.Context, in *struct{ Body sourceBody }) (*testOut, error) {
		src, err := s.resolve(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		k, err := s.open(ctx, src)
		if err != nil {
			return nil, sourceProblem(err)
		}
		v, err := k.Validate(ctx)
		if err != nil {
			return nil, sourceProblem(err)
		}
		return &testOut{Body: v}, nil
	})

	type scopesOut struct {
		Body struct {
			Scopes []connectors.Scope `json:"scopes"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listConnectionScopes", Method: http.MethodPost, Path: "/api/v1/connections/scopes", Tags: tags,
		Summary: "List the scope options of a source", Description: "Spaces for Confluence, projects for Jira, repositories for GitHub and Azure DevOps Server.", Middlewares: admins,
	}, func(ctx context.Context, in *struct{ Body sourceBody }) (*scopesOut, error) {
		src, err := s.resolve(ctx, in.Body)
		if err != nil {
			return nil, err
		}
		k, err := s.open(ctx, src)
		if err != nil {
			return nil, sourceProblem(err)
		}
		scopes, err := k.Scopes(ctx)
		if err != nil {
			return nil, sourceProblem(err)
		}
		out := &scopesOut{}
		out.Body.Scopes = scopes
		return out, nil
	})

	type idOut struct {
		Body struct {
			ID int64 `json:"id"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "createConnection", Method: http.MethodPost, Path: "/api/v1/connections", Tags: tags,
		Summary: "Create a connection", Description: "A new connection is visible only to its owner, the admin who creates it.",
		DefaultStatus: http.StatusCreated, Middlewares: admins,
	}, func(ctx context.Context, in *struct{ Body connectionBody }) (*idOut, error) {
		b := in.Body
		if err := checkURL(b.BaseURL); err != nil {
			return nil, err
		}
		if b.Token == "" {
			return nil, api.Problem(http.StatusUnprocessableEntity, "token_required", "Enter a personal access token.")
		}
		out := &idOut{}
		id, err := s.create(ctx, api.Caller(ctx).User.ID, b)
		out.Body.ID = id
		return out, wrap(err)
	})

	type updateIn struct {
		ID   int64 `path:"id"`
		Body connectionBody
	}
	huma.Register(a, huma.Operation{
		OperationID: "updateConnection", Method: http.MethodPut, Path: "/api/v1/connections/{id}", Tags: tags,
		Summary: "Change a connection", Description: "The kind cannot change. An empty token keeps the stored token, and then the base URL cannot change.",
		DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *updateIn) (*struct{}, error) {
		b := in.Body
		if err := checkURL(b.BaseURL); err != nil {
			return nil, err
		}
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			q := New(tx)
			cur, err := q.GetConnection(ctx, in.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return api.ErrNotFound
			}
			if err != nil {
				return err
			}
			if cur.Kind == "registry" {
				return registryProblem()
			}
			if cur.Kind != b.Kind {
				return api.Problem(http.StatusUnprocessableEntity, "kind_fixed", "The kind of a connection cannot change.")
			}
			// The stored token goes only to the host that it belongs to (SI-04).
			if b.Token == "" && cur.BaseUrl != strings.TrimRight(b.BaseURL, "/") {
				return api.Problem(http.StatusUnprocessableEntity, "token_required", "Enter the token again for the new URL.")
			}
			if _, err := q.UpdateConnection(ctx, UpdateConnectionParams{ID: in.ID, Name: strings.TrimSpace(b.Name),
				BaseUrl: strings.TrimRight(b.BaseURL, "/"), Scope: rawJSON(b.Scope), Options: rawJSON(b.Options),
				IncludeGlobs: orStrings(b.IncludeGlobs), ExcludeGlobs: orStrings(b.ExcludeGlobs), SyncMinutes: b.SyncMinutes,
				ReconcileHours: b.ReconcileHours, RateConcurrency: b.RateConcurrency, RatePerSecond: b.RatePerSecond,
				DefaultDomainID: b.DefaultDomainID}); err != nil {
				return dbError(err)
			}
			if b.Token != "" {
				if err := s.setToken(ctx, tx, in.ID, b.Token); err != nil {
					return err
				}
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "connections.update", Target: fmt.Sprintf("connection:%d", in.ID),
				Details: map[string]any{"name": b.Name, "baseUrl": b.BaseURL, "scope": b.Scope, "tokenChanged": b.Token != ""}})
		})
		return nil, wrap(err)
	})

	type visibilityIn struct {
		ID   int64 `path:"id"`
		Body struct {
			GroupIDs []int64 `json:"groupIds" doc:"The groups that can read the content, in addition to the owner."`
			Confirm  bool    `json:"confirm" doc:"Confirms that all members of the added groups can read this scope in the source system."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "setConnectionVisibility", Method: http.MethodPut, Path: "/api/v1/connections/{id}/visibility", Tags: tags,
		Summary: "Share a connection with groups", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *visibilityIn) (*struct{}, error) {
		return nil, wrap(s.setVisibility(ctx, in.ID, in.Body.GroupIDs, in.Body.Confirm, api.Caller(ctx).User.ID))
	})

	type runOut struct {
		Body struct {
			RunID int64 `json:"runId"`
		}
	}
	for _, kind := range []string{"sync", "reconcile"} {
		summary := "Start a sync now"
		if kind == "reconcile" {
			summary = "Start a reconciliation now"
		}
		huma.Register(a, huma.Operation{
			OperationID: kind + "Connection", Method: http.MethodPost, Path: "/api/v1/connections/{id}/" + kind, Tags: tags,
			Summary: summary, DefaultStatus: http.StatusAccepted, Middlewares: admins,
		}, func(ctx context.Context, in *idIn) (*runOut, error) {
			c, err := New(s.Pool).GetConnection(ctx, in.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, api.ErrNotFound
			}
			if err != nil {
				return nil, api.Internal(err)
			}
			if c.Kind == "registry" {
				return nil, registryProblem()
			}
			if c.Status == "paused" {
				return nil, api.Problem(http.StatusConflict, "paused", "The connection is paused. Resume it first.")
			}
			id, err := s.Queue(ctx, in.ID, kind, "manual")
			if errors.Is(err, ErrRunActive) {
				return nil, api.Problem(http.StatusConflict, "run_active", "A run of this kind is already queued or running.")
			}
			if err != nil {
				return nil, api.Internal(err)
			}
			out := &runOut{}
			out.Body.RunID = id
			return out, nil
		})
	}

	type pauseIn struct {
		ID   int64 `path:"id"`
		Body struct {
			Paused bool `json:"paused" doc:"True pauses the connection and hides its content. False resumes it: the content shows again after the next successful reconciliation."`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "pauseConnection", Method: http.MethodPut, Path: "/api/v1/connections/{id}/paused", Tags: tags,
		Summary: "Pause or resume a connection", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *pauseIn) (*struct{}, error) {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			q := New(tx)
			c, err := q.GetConnection(ctx, in.ID)
			if errors.Is(err, pgx.ErrNoRows) {
				return api.ErrNotFound
			}
			if err != nil {
				return err
			}
			if c.Kind == "registry" {
				return registryProblem()
			}
			status, reason, action := "paused", "An admin paused the connection.", "connections.pause"
			if !in.Body.Paused {
				if c.Status != "paused" {
					return nil
				}
				status, reason, action = "resuming", "The content shows again after the next successful reconciliation.", "connections.resume"
			}
			if _, err := q.SetStatus(ctx, SetStatusParams{ID: in.ID, Status: status, Reason: reason}); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: action, Target: fmt.Sprintf("connection:%d", in.ID)})
		})
		if err != nil {
			return nil, wrap(err)
		}
		if !in.Body.Paused {
			if _, err := s.Queue(ctx, in.ID, "reconcile", "manual"); err != nil && !errors.Is(err, ErrRunActive) {
				return nil, api.Internal(err)
			}
		}
		return nil, nil
	})

	huma.Register(a, huma.Operation{
		OperationID: "purgeConnection", Method: http.MethodDelete, Path: "/api/v1/connections/{id}", Tags: tags,
		Summary: "Purge a connection", Description: "Deletes the connection and all its data. The registry cannot be purged.",
		DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		return nil, wrap(s.Purge(ctx, in.ID))
	})

	type runsOut struct {
		Body struct {
			Runs []RunRecord `json:"runs"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listConnectionRuns", Method: http.MethodGet, Path: "/api/v1/connections/{id}/runs", Tags: tags,
		Summary: "List the sync runs of a connection", Middlewares: admins,
	}, func(ctx context.Context, in *idIn) (*runsOut, error) {
		rows, err := New(s.Pool).ListRuns(ctx, ListRunsParams{ConnectionID: in.ID, Lim: 50})
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &runsOut{}
		out.Body.Runs = []RunRecord{}
		for _, r := range rows {
			rec := RunRecord{ID: r.ID, Kind: r.Kind, Trigger: r.Trigger, Status: r.Status, Seen: r.Seen, Created: r.Created,
				Updated: r.Updated, Unchanged: r.Unchanged, Deleted: r.Deleted, Error: r.Error, QueuedAt: r.QueuedAt,
				StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Skipped: []Skip{}}
			_ = json.Unmarshal(r.Skipped, &rec.Skipped)
			out.Body.Runs = append(out.Body.Runs, rec)
		}
		return out, nil
	})
}

func registryProblem() error {
	return api.Problem(http.StatusConflict, "registry", "The registry is built in. It cannot be changed, paused or purged.")
}

// create inserts a connection owned by owner, visible only to the owner.
func (s *Service) create(ctx context.Context, owner string, b connectionBody) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = New(tx).InsertConnection(ctx, InsertConnectionParams{Kind: b.Kind, Name: strings.TrimSpace(b.Name),
			BaseUrl: strings.TrimRight(b.BaseURL, "/"), Scope: rawJSON(b.Scope), Options: rawJSON(b.Options),
			IncludeGlobs: orStrings(b.IncludeGlobs), ExcludeGlobs: orStrings(b.ExcludeGlobs), SyncMinutes: b.SyncMinutes,
			ReconcileHours: b.ReconcileHours, RateConcurrency: b.RateConcurrency, RatePerSecond: b.RatePerSecond,
			OwnerID: &owner, DefaultDomainID: b.DefaultDomainID})
		if err != nil {
			return dbError(err)
		}
		if err := s.setToken(ctx, tx, id, b.Token); err != nil {
			return err
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "connections.create", Target: fmt.Sprintf("connection:%d", id),
			Details: map[string]any{"name": b.Name, "kind": b.Kind, "baseUrl": b.BaseURL, "scope": b.Scope}})
	})
	return id, err
}

func (s *Service) setToken(ctx context.Context, tx pgx.Tx, id int64, token string) error {
	sid, err := s.Secrets.Put(ctx, tx, secretName(id), []byte(token))
	if err != nil {
		return err
	}
	return New(tx).SetConnectionSecret(ctx, SetConnectionSecretParams{ID: id, SecretID: &sid})
}

// setVisibility shares a connection with groups. Adding a group needs the
// confirmation (REQ-VIS-003).
// setVisibility sets the groups of a connection. by confirms the new groups.
func (s *Service) setVisibility(ctx context.Context, id int64, groupIDs []int64, confirm bool, by string) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		c, err := q.GetConnection(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		if err != nil {
			return err
		}
		if c.Kind == "registry" {
			return registryProblem()
		}
		groupIDs = slices.Compact(slices.Sorted(slices.Values(groupIDs)))
		if n, err := q.GroupsExist(ctx, groupIDs); err != nil {
			return err
		} else if int(n) != len(groupIDs) {
			return api.Problem(http.StatusUnprocessableEntity, "unknown_group", "A group does not exist.")
		}
		vis, err := q.ListVisibility(ctx)
		if err != nil {
			return err
		}
		current := map[int64]bool{}
		for _, v := range vis {
			if v.ConnectionID == id {
				current[v.GroupID] = true
			}
		}
		var added []int64
		for _, g := range groupIDs {
			if !current[g] {
				added = append(added, g)
			}
		}
		if len(added) > 0 && !confirm {
			return api.Problem(http.StatusUnprocessableEntity, "confirmation_required",
				"Confirm that all members of these groups can read this scope in the source system.")
		}
		removed, err := q.RemoveVisibility(ctx, RemoveVisibilityParams{ConnectionID: id, Keep: orEmptyIDs(groupIDs)})
		if err != nil {
			return err
		}
		now := s.now()
		for _, g := range added {
			if _, err := q.AddVisibility(ctx, AddVisibilityParams{ConnectionID: id, GroupID: g, ConfirmedBy: &by, ConfirmedAt: now}); err != nil {
				return err
			}
		}
		if len(added) == 0 && len(removed) == 0 {
			return nil
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "connections.visibility", Target: fmt.Sprintf("connection:%d", id),
			Details: map[string]any{"added": orEmptyIDs(added), "removed": orEmptyIDs(removed),
				"confirmation": "All members of these groups can read this scope in the source system."}})
	})
}

// Purge deletes a connection and all its data in one transaction, with the
// entities that have no other source, and writes an audit entry. The
// registry cannot be purged.
func (s *Service) Purge(ctx context.Context, id int64) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		c, err := q.GetConnection(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		if err != nil {
			return err
		}
		if c.Kind == "registry" {
			return registryProblem()
		}
		ents, err := q.ConnectionEntities(ctx, id)
		if err != nil {
			return err
		}
		if _, err := q.DeleteConnection(ctx, id); err != nil {
			return err
		}
		if _, err := q.DeleteSourcelessEntities(ctx, append([]int64{}, ents...)); err != nil {
			return err
		}
		if c.SecretID != nil {
			if err := s.Secrets.Delete(ctx, tx, secretName(id)); err != nil {
				return err
			}
		}
		return audit.Write(ctx, tx, audit.Entry{Action: "connections.purge", Target: fmt.Sprintf("connection:%d", id),
			Details: map[string]any{"name": c.Name, "kind": c.Kind}})
	})
}

func (s *Service) records(ctx context.Context) ([]ConnectionRecord, error) {
	q := New(s.Pool)
	rows, err := q.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	vis, err := q.ListVisibility(ctx)
	if err != nil {
		return nil, err
	}
	out := []ConnectionRecord{}
	for _, r := range rows {
		rec := ConnectionRecord{ID: r.ID, Kind: r.Kind, Name: r.Name, BaseURL: r.BaseUrl, HasToken: r.SecretID != nil,
			Scope: object(r.Scope), Options: object(r.Options), IncludeGlobs: orStrings(r.IncludeGlobs), ExcludeGlobs: orStrings(r.ExcludeGlobs),
			SyncMinutes: r.SyncMinutes, ReconcileHours: r.ReconcileHours, RateConcurrency: r.RateConcurrency, RatePerSecond: r.RatePerSecond,
			OwnerID: r.OwnerID, OwnerEmail: r.OwnerEmail, Status: r.Status, StatusReason: r.StatusReason, DefaultDomainID: r.DefaultDomainID,
			Items: r.Items, LastSyncAt: r.LastSyncAt, LastReconcileAt: r.LastReconcileAt, PermissionsCheckedAt: r.PermissionsCheckedAt,
			LastError: r.LastError, CreatedAt: r.CreatedAt, Visibility: []GroupRef{}}
		if r.Kind != "registry" && r.Status != "paused" {
			next := s.now()
			if r.LastSyncAt != nil {
				next = r.LastSyncAt.Add(time.Duration(r.SyncMinutes) * time.Minute)
			}
			rec.NextSyncAt = &next
		}
		for _, v := range vis {
			if v.ConnectionID == r.ID {
				rec.Visibility = append(rec.Visibility, GroupRef{ID: v.GroupID, Name: v.Name, Builtin: v.Builtin, ConfirmedAt: v.ConfirmedAt})
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

func (s *Service) record(ctx context.Context, id int64) (*ConnectionRecord, error) {
	recs, err := s.records(ctx)
	if err != nil {
		return nil, api.Internal(err)
	}
	for i := range recs {
		if recs[i].ID == id {
			return &recs[i], nil
		}
	}
	return nil, api.ErrNotFound
}

func orEmptyIDs(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

func dbError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return api.Problem(http.StatusConflict, "name_taken", "A connection with this name exists.")
		case "23503":
			return api.Problem(http.StatusUnprocessableEntity, "unknown_domain", "The domain does not exist.")
		}
	}
	return err
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	var e *api.Error
	if errors.As(err, &e) {
		return e
	}
	return api.Internal(err)
}
