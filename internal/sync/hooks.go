package sync

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"golang.org/x/time/rate"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/tree"
)

// HookPath is the start of the webhook URLs. The route is outside /api/v1
// and takes no session and no access token: the token in the URL and the
// signature authenticate the source.
const HookPath = "/hooks"

// HookMaxBytes is the largest webhook body. A larger body gets 413, and
// polling reads the change.
const HookMaxBytes = 1 << 20

// hookRate is the most webhooks per second of one connection.
const hookRate = 10

// hookBatch is the most item IDs of one sync_items job.
const hookBatch = 100

// webhookKind reports whether a connection can receive webhooks: GitHub,
// Azure DevOps, and Jira and Confluence Data Center. Confluence Cloud has
// no admin webhooks, and Jira Cloud signs only the webhooks of apps.
func webhookKind(kind, baseURL string, options []byte) bool {
	switch kind {
	case "github", "azdo":
		return true
	case "jira", "confluence":
		return deploymentOf(kind, baseURL, options) != deployCloud
	}
	return false
}

func webhookSecretName(id int64) string { return fmt.Sprintf("connection:%d:webhook", id) }

// hookLimiter limits the webhooks of each connection in this process.
type hookLimiter struct {
	mu gosync.Mutex
	m  map[int64]*rate.Limiter
}

func (l *hookLimiter) allow(id int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[int64]*rate.Limiter{}
	}
	lim, ok := l.m[id]
	if !ok {
		lim = rate.NewLimiter(hookRate, hookRate)
		l.m[id] = lim
	}
	return lim.Allow()
}

// WebhookRecord is the webhook state of a connection. The URL and the
// secret are shown only once, when they are made.
type WebhookRecord struct {
	Public         bool       `json:"public" doc:"False when ASSAY_BASE_URL is a localhost or private address, which a source system cannot reach."`
	HTTPS          bool       `json:"https" doc:"True when ASSAY_BASE_URL uses HTTPS. Azure DevOps sends its Basic credentials only over HTTPS."`
	RotatedAt      *time.Time `json:"rotatedAt" doc:"When the URL and the secret were made. Null before the first time."`
	Received       int64      `json:"received" doc:"The accepted webhooks since the URL was first made."`
	LastReceivedAt *time.Time `json:"lastReceivedAt" doc:"The time of the last accepted webhook."`
}

// WebhookSecret is a new webhook URL with its secret.
type WebhookSecret struct {
	URL      string `json:"url" doc:"The URL to register in the source. It holds the token. Assay shows it only now."`
	Secret   string `json:"secret" doc:"The signing secret, or the Basic password for Azure DevOps. Assay shows it only now."`
	Username string `json:"username,omitempty" doc:"The Basic user name for Azure DevOps."`
}

// publicURL reports whether a source system outside the host can reach a
// base URL: not localhost, a single-label name or a private address.
func publicURL(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	return strings.Contains(host, ".")
}

// webhookRecords returns the webhook state of the connections that can
// receive webhooks.
func (s *Service) webhookRecords(ctx context.Context) (map[int64]WebhookRecord, error) {
	rows, err := New(s.Pool).ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]WebhookRecord, len(rows))
	for _, r := range rows {
		out[r.ConnectionID] = WebhookRecord{RotatedAt: &r.RotatedAt, Received: r.Received, LastReceivedAt: r.LastReceivedAt}
	}
	return out, nil
}

// webhookOf is the webhook state of one connection record.
func (s *Service) webhookOf(kind, baseURL string, options []byte, stored map[int64]WebhookRecord, id int64) *WebhookRecord {
	if !webhookKind(kind, baseURL, options) {
		return nil
	}
	w := stored[id]
	w.Public, w.HTTPS = publicURL(s.BaseURL), strings.HasPrefix(s.BaseURL, "https://")
	return &w
}

// randomText returns n random bytes as URL-safe base64.
func randomText(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// RotateWebhook makes a new webhook URL and secret for a connection. The
// old URL then returns 404.
func (s *Service) RotateWebhook(ctx context.Context, id int64) (*WebhookSecret, error) {
	out := &WebhookSecret{}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := New(tx)
		c, err := q.GetConnection(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return api.ErrNotFound
		}
		if err != nil {
			return err
		}
		if !webhookKind(c.Kind, c.BaseUrl, c.Options) {
			return api.Problem(http.StatusUnprocessableEntity, "webhook_unsupported",
				"Only GitHub, Azure DevOps, and Jira and Confluence Data Center connections receive webhooks.")
		}
		token := randomText(32)
		out.Secret = randomText(32)
		if c.Kind == "azdo" {
			b := make([]byte, 4)
			_, _ = rand.Read(b)
			out.Username = "assay-" + hex.EncodeToString(b)
		}
		sid, err := s.Secrets.Put(ctx, tx, webhookSecretName(id), []byte(out.Secret))
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(token))
		if err := q.PutWebhook(ctx, PutWebhookParams{ConnectionID: id, TokenHash: sum[:], Username: out.Username, SecretID: &sid,
			Now: s.now()}); err != nil {
			return err
		}
		out.URL = strings.TrimRight(s.BaseURL, "/") + HookPath + "/" + strconv.FormatInt(id, 10) + "/" + token
		return audit.Write(ctx, tx, audit.Entry{Action: "connections.webhook_rotate", Target: fmt.Sprintf("connection:%d", id)})
	})
	if err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

// registerWebhookAdmin adds the route that makes a webhook URL.
func (s *Service) registerWebhookAdmin(a huma.API, tags []string) {
	type in struct {
		ID int64 `path:"id"`
	}
	type out struct{ Body WebhookSecret }
	huma.Register(a, huma.Operation{
		OperationID: "rotateConnectionWebhook", Method: http.MethodPost, Path: "/api/v1/connections/{id}/webhook", Tags: tags,
		Summary:     "Make a new webhook URL and secret",
		Description: "Makes the webhook URL and secret of a connection, or replaces them. The old URL then returns 404. The response is the only place that shows them.",
		Middlewares: huma.Middlewares{api.Require(auth.RoleAdmin)},
	}, func(ctx context.Context, i *in) (*out, error) {
		sec, err := s.RotateWebhook(ctx, i.ID)
		if err != nil {
			return nil, err
		}
		return &out{Body: *sec}, nil
	})
}

// errHookSignature is the answer to a missing or bad signature. It has no
// detail.
var errHookSignature = api.Problem(http.StatusUnauthorized, "unauthorized", "The request is not authorized.")

// errHookNotFound is the answer to an unknown connection or token.
var errHookNotFound = api.Problem(http.StatusNotFound, "not_found", "The object does not exist.")

type hookIn struct {
	ID            string `path:"id" maxLength:"20"`
	Token         string `path:"token" maxLength:"128"`
	GitHubEvent   string `header:"X-GitHub-Event"`
	Signature256  string `header:"X-Hub-Signature-256"`
	Signature     string `header:"X-Hub-Signature"`
	Authorization string `header:"Authorization"`
	RawBody       []byte
}

// RegisterHooks adds the webhook route of the source systems. The API that
// it takes runs no session middleware: the route is not a user route.
func (s *Service) RegisterHooks(a huma.API) {
	huma.Register(a, huma.Operation{
		OperationID: "receiveWebhook", Method: http.MethodPost, Path: HookPath + "/{id}/{token}", Tags: []string{"webhooks"},
		Summary:       "Receive a webhook of a source system",
		DefaultStatus: http.StatusAccepted, MaxBodyBytes: HookMaxBytes,
	}, func(ctx context.Context, in *hookIn) (*struct{}, error) {
		return nil, s.receive(ctx, in)
	})
}

// receive checks a webhook and queues a read of the items that it names.
// The body is never stored: it only names items, and Fetch reads each of
// them from the source.
func (s *Service) receive(ctx context.Context, in *hookIn) error {
	id, err := strconv.ParseInt(in.ID, 10, 64)
	if err != nil {
		return errHookNotFound
	}
	q := New(s.Pool)
	w, err := q.WebhookOf(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errHookNotFound
	}
	if err != nil {
		return api.Internal(err)
	}
	sum := sha256.Sum256([]byte(in.Token))
	if subtle.ConstantTimeCompare(sum[:], w.TokenHash) != 1 || !webhookKind(w.Kind, w.BaseUrl, w.Options) || w.SecretID == nil {
		return errHookNotFound
	}
	if !s.hooks.allow(id) {
		return api.Problem(http.StatusTooManyRequests, "rate_limited", "Too many requests. Wait and send again.")
	}
	secret, err := s.Secrets.GetByID(ctx, s.Pool, *w.SecretID)
	if err != nil {
		return api.Internal(err)
	}
	if !signed(w.Kind, w.Username, secret, in) {
		return errHookSignature
	}
	if err := q.CountWebhook(ctx, CountWebhookParams{ConnectionID: id, Now: ptr(s.now())}); err != nil {
		return api.Internal(err)
	}
	if w.Status == "paused" {
		return nil
	}
	ids, repos, err := hookItems(w.Kind, in.GitHubEvent, in.RawBody)
	if err != nil {
		return api.Problem(http.StatusBadRequest, "bad_request", "The body is not valid JSON.")
	}
	var jobs []river.InsertManyParams
	for chunk := range slices.Chunk(ids, hookBatch) {
		jobs = append(jobs, river.InsertManyParams{Args: ItemsArgs{ConnectionID: id, IDs: chunk}})
	}
	if len(repos) > 0 {
		jobs = append(jobs, river.InsertManyParams{Args: ItemsArgs{ConnectionID: id, IDs: []string{}, Repos: repos}})
	}
	if len(jobs) == 0 {
		return nil
	}
	if _, err := s.River.InsertMany(ctx, jobs); err != nil {
		return api.Internal(err)
	}
	return nil
}

// signed checks the signature of a webhook over the raw body, in constant
// time: X-Hub-Signature-256 for GitHub, X-Hub-Signature with or without
// the sha256= prefix for Jira and Confluence Data Center, and the Basic
// credentials for Azure DevOps, whose service hooks do not sign.
func signed(kind, username string, secret []byte, in *hookIn) bool {
	switch kind {
	case "azdo":
		raw, ok := strings.CutPrefix(in.Authorization, "Basic ")
		if !ok {
			return false
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return false
		}
		user, pass, _ := strings.Cut(string(b), ":")
		u := subtle.ConstantTimeCompare([]byte(user), []byte(username))
		p := subtle.ConstantTimeCompare([]byte(pass), secret)
		return u&p == 1
	case "github":
		sig, ok := strings.CutPrefix(in.Signature256, "sha256=")
		return ok && macEqual(secret, in.RawBody, sig)
	case "jira", "confluence":
		return in.Signature != "" && macEqual(secret, in.RawBody, strings.TrimPrefix(in.Signature, "sha256="))
	}
	return false
}

// macEqual reports whether sig is the hex HMAC-SHA256 of body.
func macEqual(secret, body []byte, sig string) bool {
	got, err := hex.DecodeString(strings.TrimSpace(sig))
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}

// hookItems maps the event of a webhook body to the item IDs that it names,
// and to the repositories of a push whose files the job compares. Other
// events name nothing. Only identifiers are read from the body.
func hookItems(kind, githubEvent string, body []byte) (ids, repos []string, err error) {
	switch kind {
	case "github":
		ids, repos, err = githubItems(githubEvent, body)
	case "azdo":
		ids, repos, err = azdoItems(body)
	case "jira":
		ids, err = jiraItems(body)
	case "confluence":
		ids, err = confluenceItems(body)
	}
	slices.Sort(ids)
	return slices.Compact(ids), repos, err
}

type githubRepo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
}

func githubItems(event string, body []byte) ([]string, []string, error) {
	switch event {
	case "push":
		var p struct {
			Ref        string     `json:"ref"`
			Forced     bool       `json:"forced"`
			Repository githubRepo `json:"repository"`
			Commits    []struct {
				Added    []string `json:"added"`
				Removed  []string `json:"removed"`
				Modified []string `json:"modified"`
			} `json:"commits"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, nil, err
		}
		repo := p.Repository.FullName
		if repo == "" || p.Repository.DefaultBranch == "" || p.Ref != "refs/heads/"+p.Repository.DefaultBranch {
			return nil, nil, nil
		}
		// A forced push can drop commits that the body does not list.
		if p.Forced || len(p.Commits) == 0 {
			return nil, []string{repo}, nil
		}
		var ids []string
		for _, c := range p.Commits {
			for _, list := range [][]string{c.Added, c.Removed, c.Modified} {
				for _, f := range list {
					if f != "" {
						ids = append(ids, tree.ItemID(repo, f))
					}
				}
			}
		}
		return ids, nil, nil
	case "pull_request", "issues":
		var p struct {
			Repository  githubRepo `json:"repository"`
			PullRequest struct {
				Number int `json:"number"`
			} `json:"pull_request"`
			Issue struct {
				Number int `json:"number"`
			} `json:"issue"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, nil, err
		}
		kind, n := connectors.KindPullRequest, p.PullRequest.Number
		if event == "issues" {
			kind, n = connectors.KindIssue, p.Issue.Number
		}
		if p.Repository.FullName == "" || n <= 0 {
			return nil, nil, nil
		}
		return []string{connectors.HistoryID(p.Repository.FullName, kind, strconv.Itoa(n))}, nil, nil
	}
	return nil, nil, nil
}

type azdoRepo struct {
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	Project       struct {
		Name string `json:"name"`
	} `json:"project"`
}

func (r azdoRepo) id() string {
	if r.Name == "" || r.Project.Name == "" {
		return ""
	}
	return r.Project.Name + "/" + r.Name
}

func azdoItems(body []byte) ([]string, []string, error) {
	var p struct {
		EventType string          `json:"eventType"`
		Resource  json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, nil, err
	}
	if len(p.Resource) == 0 {
		return nil, nil, nil
	}
	switch {
	case p.EventType == "git.push":
		// A push body names no paths. The job compares the files of the
		// repository with the stored revisions.
		var r struct {
			RefUpdates []struct {
				Name string `json:"name"`
			} `json:"refUpdates"`
			Repository azdoRepo `json:"repository"`
		}
		if err := json.Unmarshal(p.Resource, &r); err != nil {
			return nil, nil, err
		}
		id := r.Repository.id()
		branch := r.Repository.DefaultBranch
		if id == "" || !slices.ContainsFunc(r.RefUpdates, func(u struct {
			Name string `json:"name"`
		}) bool {
			return branch == "" || u.Name == branch
		}) {
			return nil, nil, nil
		}
		return nil, []string{id}, nil
	case strings.HasPrefix(p.EventType, "git.pullrequest."):
		var r struct {
			PullRequestID int      `json:"pullRequestId"`
			Repository    azdoRepo `json:"repository"`
		}
		if err := json.Unmarshal(p.Resource, &r); err != nil {
			return nil, nil, err
		}
		if r.Repository.id() == "" || r.PullRequestID <= 0 {
			return nil, nil, nil
		}
		return []string{connectors.HistoryID(r.Repository.id(), connectors.KindPullRequest, strconv.Itoa(r.PullRequestID))}, nil, nil
	case strings.HasPrefix(p.EventType, "workitem."):
		// workitem.updated has the work item in workItemId and the project
		// in the revision; the other events have both on the resource.
		var r struct {
			ID         int            `json:"id"`
			WorkItemID int            `json:"workItemId"`
			Fields     map[string]any `json:"fields"`
			Revision   struct {
				Fields map[string]any `json:"fields"`
			} `json:"revision"`
		}
		if err := json.Unmarshal(p.Resource, &r); err != nil {
			return nil, nil, err
		}
		n := r.ID
		if r.WorkItemID > 0 {
			n = r.WorkItemID
		}
		project, _ := r.Revision.Fields["System.TeamProject"].(string)
		if project == "" {
			project, _ = r.Fields["System.TeamProject"].(string)
		}
		if project == "" || n <= 0 {
			return nil, nil, nil
		}
		return []string{connectors.HistoryID(project, connectors.KindWorkItem, strconv.Itoa(n))}, nil, nil
	}
	return nil, nil, nil
}

func jiraItems(body []byte) ([]string, error) {
	var p struct {
		WebhookEvent string `json:"webhookEvent"`
		Issue        struct {
			Key string `json:"key"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(p.WebhookEvent, "jira:issue_") && !strings.HasPrefix(p.WebhookEvent, "comment_") {
		return nil, nil
	}
	if p.Issue.Key == "" || strings.ContainsAny(p.Issue.Key, "/?# ") {
		return nil, nil
	}
	return []string{p.Issue.Key}, nil
}

func confluenceItems(body []byte) ([]string, error) {
	var p struct {
		Event string `json:"event"`
		Page  struct {
			ID json.Number `json:"id"`
		} `json:"page"`
	}
	d := json.NewDecoder(strings.NewReader(string(body)))
	d.UseNumber()
	if err := d.Decode(&p); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(p.Event, "page_") {
		return nil, nil
	}
	id := p.Page.ID.String()
	if id == "" || strings.Trim(id, "0123456789") != "" {
		return nil, nil
	}
	return []string{id}, nil
}
