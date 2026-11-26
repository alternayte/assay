// Package teams reads the threads of Microsoft Teams channels through
// Microsoft Graph with app-only access: the customer's own Entra app with the
// application permissions ChannelMessage.Read.All, Team.ReadBasic.All and
// Channel.ReadBasic.All. The access token comes from the entra token client,
// the one POST to the token endpoint of the tenant. Every Graph read goes
// through the GET-only connector client. Each thread is an item and each
// message a passage.
package teams

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	gosync "sync"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/chat"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/connectors/entra"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/outbound"
)

// pageSize is the page size of a message listing. Graph allows at most 50.
const pageSize = 50

// deltaOverlap widens the start of the first delta read, so that a message
// sent during the first listing is read again.
const deltaOverlap = 5 * time.Minute

// Scope selects the teams and the channels of a connection.
type Scope struct {
	// Teams are the team IDs whose channels the scope picker lists.
	Teams []string `json:"teams,omitempty"`
	// Channels are the channels to read, as <team ID>/<channel ID>.
	Channels []string `json:"channels,omitempty"`
}

// Options are the options of a connection. The client secret of the app is
// the token of the connection.
type Options struct {
	Tenant     string `json:"tenant"`
	ClientID   string `json:"clientId"`
	RetainDays int    `json:"retainDays,omitempty"`
}

// ParseOptions reads and checks the options of a connection.
func ParseOptions(raw json.RawMessage) (Options, error) {
	var o Options
	days, err := chat.ParseRetainDays(raw)
	if err != nil {
		return o, err
	}
	var v struct {
		Tenant   string `json:"tenant"`
		ClientID string `json:"clientId"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil {
			return o, fmt.Errorf("teams: the options are not valid: %w", err)
		}
	}
	o = Options{Tenant: strings.TrimSpace(v.Tenant), ClientID: strings.TrimSpace(v.ClientID), RetainDays: days}
	if o.Tenant == "" || o.ClientID == "" {
		return o, errors.New("teams: enter the tenant and the client ID of the app")
	}
	if strings.ContainsAny(o.Tenant, "/?#") {
		return o, errors.New("teams: the tenant is a tenant ID or a domain, such as acme.onmicrosoft.com")
	}
	return o, nil
}

// ParseScope reads the scope of a connection.
func ParseScope(raw json.RawMessage) (Scope, error) {
	var s Scope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("teams: the scope is not valid: %w", err)
		}
	}
	for _, c := range s.Channels {
		team, channel, ok := strings.Cut(c, "/")
		if !ok || team == "" || channel == "" || strings.Contains(channel, "/") {
			return s, fmt.Errorf("teams: the channel %q is not <team ID>/<channel ID>", c)
		}
	}
	return s, nil
}

// Authorizer returns the Authorization function of the Graph client of a
// connection: a cached app-only token of the tenant. A refusal of the token
// endpoint is a refused token, which pauses the connection.
func Authorizer(s outbound.Settings, authority, graphURL, secret string, o Options) (func(context.Context) (string, error), error) {
	scope, err := entra.ScopeFor(graphURL)
	if err != nil {
		return nil, err
	}
	ts, err := entra.NewTokenSource(s, entra.Credentials{TokenURL: entra.TokenURL(authority, o.Tenant), ClientID: o.ClientID,
		ClientSecret: secret, Scope: scope})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (string, error) {
		a, err := ts.Authorization(ctx)
		if errors.Is(err, entra.ErrRefused) {
			return "", fmt.Errorf("%w: %w", connectors.ErrUnauthorized, err)
		}
		return a, err
	}, nil
}

// Connector reads the channels of one connection.
type Connector struct {
	c       *connectors.Client
	scope   Scope
	options Options
	keep    chat.Retention
	now     func() time.Time

	mu       gosync.Mutex
	channels map[string]string // channel name by <team>/<channel>
}

// New returns a connector. scope and options are the JSON scope and options
// of the connection. now is the clock of the retention.
func New(c *connectors.Client, scope, options json.RawMessage, now func() time.Time) (*Connector, error) {
	s, err := ParseScope(scope)
	if err != nil {
		return nil, err
	}
	o, err := ParseOptions(options)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Connector{c: c, scope: s, options: o, keep: chat.Retention{Days: o.RetainDays, Now: now}, now: now,
		channels: map[string]string{}}, nil
}

type page[T any] struct {
	Value     []T    `json:"value"`
	NextLink  string `json:"@odata.nextLink"`
	DeltaLink string `json:"@odata.deltaLink"`
}

// pages reads a Graph collection from path, or from a link of Graph when
// link is set, and calls fn for each page. It returns the delta link of the
// last page, if any.
func pages[T any](ctx context.Context, c *connectors.Client, path string, q url.Values, link string, fn func([]T) error) (string, error) {
	for {
		var p page[T]
		if link == "" {
			if err := c.GetJSON(ctx, path, q, &p); err != nil {
				return "", err
			}
		} else {
			r, err := c.Send(ctx, http.MethodGet, link, "application/json", 0)
			if err != nil {
				return "", err
			}
			if err := json.Unmarshal(r.Body, &p); err != nil {
				return "", fmt.Errorf("teams: the answer for %s is not valid JSON: %w", link, err)
			}
		}
		if err := fn(p.Value); err != nil {
			return "", err
		}
		if p.NextLink == "" {
			return p.DeltaLink, nil
		}
		link = p.NextLink
	}
}

type named struct {
	ID             string `json:"id"`
	DisplayName    string `json:"displayName"`
	MembershipType string `json:"membershipType"`
}

func (k *Connector) teams(ctx context.Context) ([]named, error) {
	var out []named
	_, err := pages(ctx, k.c, "/teams", url.Values{"$select": {"id,displayName"}}, "", func(ts []named) error {
		out = append(out, ts...)
		return nil
	})
	return out, err
}

// Validate checks the app registration and counts the teams that the app
// reads.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	ts, err := k.teams(ctx)
	if err != nil {
		return connectors.Validation{}, err
	}
	return connectors.Validation{User: k.options.ClientID, Sample: len(ts)}, nil
}

// Scopes lists the teams. When the scope names teams, it lists the standard
// channels of those teams instead, with the IDs <team ID>/<channel ID>.
// Private and shared channels are not read.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
	ts, err := k.teams(ctx)
	if err != nil {
		return nil, err
	}
	if len(k.scope.Teams) == 0 {
		out := make([]connectors.Scope, 0, len(ts))
		for _, t := range ts {
			out = append(out, connectors.Scope{ID: t.ID, Name: t.DisplayName})
		}
		return out, nil
	}
	var out []connectors.Scope
	for _, t := range ts {
		if !slices.Contains(k.scope.Teams, t.ID) {
			continue
		}
		_, err := pages(ctx, k.c, "/teams/"+url.PathEscape(t.ID)+"/channels", nil, "", func(cs []named) error {
			for _, c := range cs {
				if c.MembershipType == "" || c.MembershipType == "standard" {
					out = append(out, connectors.Scope{ID: t.ID + "/" + c.ID, Name: t.DisplayName + " / " + c.DisplayName})
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return orEmpty(out), nil
}

// Parts are the selected channels.
func (k *Connector) Parts(context.Context) ([]connectors.Part, error) {
	out := make([]connectors.Part, 0, len(k.scope.Channels))
	for _, c := range k.scope.Channels {
		out = append(out, connectors.Part{Key: c})
	}
	return out, nil
}

// channelPath is the Graph path of a channel part.
func channelPath(part string) string {
	team, channel, _ := strings.Cut(part, "/")
	return "/teams/" + url.PathEscape(team) + "/channels/" + url.PathEscape(channel)
}

// channelName returns the display name of a channel, read once per run.
func (k *Connector) channelName(ctx context.Context, part string) (string, error) {
	k.mu.Lock()
	name, ok := k.channels[part]
	k.mu.Unlock()
	if ok {
		return name, nil
	}
	var c named
	if err := k.c.GetJSON(ctx, channelPath(part), url.Values{"$select": {"id,displayName"}}, &c); err != nil {
		return "", err
	}
	k.mu.Lock()
	k.channels[part] = c.DisplayName
	k.mu.Unlock()
	return c.DisplayName, nil
}

type identity struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type message struct {
	ID          string     `json:"id"`
	ReplyToID   string     `json:"replyToId"`
	MessageType string     `json:"messageType"`
	Created     time.Time  `json:"createdDateTime"`
	Deleted     *time.Time `json:"deletedDateTime"`
	Body        struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	From *struct {
		User        *identity `json:"user"`
		Application *identity `json:"application"`
	} `json:"from"`
	WebURL      string    `json:"webUrl"`
	Replies     []message `json:"replies"`
	RepliesNext string    `json:"replies@odata.nextLink"`
	Removed     *struct {
		Reason string `json:"reason"`
	} `json:"@removed"`
}

// newest is the time of the newest reply, or of the root without replies.
func newest(root message, replies []message) time.Time {
	t := root.Created
	for _, r := range replies {
		if r.Created.After(t) {
			t = r.Created
		}
	}
	return t.UTC()
}

func revision(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

var (
	atTag     = regexp.MustCompile(`(?is)<at\b[^>]*>(.*?)</at>`)
	emojiTag  = regexp.MustCompile(`(?is)<emoji\b[^>]*?\balt="([^"]*)"[^>]*>(?:\s*</emoji>)?`)
	attachTag = regexp.MustCompile(`(?is)<attachment\b[^>]*>(?:\s*</attachment>)?`)
)

// Markdown converts the body of a message to normalized Markdown. User and
// channel mentions become the display name, emoji their text, and
// attachments are dropped. Links keep only http, https and mailto targets.
func Markdown(contentType, body string) (string, error) {
	if !strings.EqualFold(contentType, "html") {
		return strings.TrimSpace(body), nil
	}
	body = atTag.ReplaceAllString(body, "$1")
	body = emojiTag.ReplaceAllString(body, "$1")
	body = attachTag.ReplaceAllString(body, "")
	md, err := confluence.ToMarkdown(body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(md), nil
}

// kept returns the message as a chat message, and false for a system event
// such as a join or leave, a bot message, a deleted message or a message
// without text.
func kept(m message) (chat.Message, bool) {
	if m.MessageType != "message" || m.Deleted != nil || m.Removed != nil || m.From == nil || m.From.User == nil ||
		m.From.Application != nil || m.From.User.ID == "" {
		return chat.Message{}, false
	}
	md, err := Markdown(m.Body.ContentType, m.Body.Content)
	if err != nil || content.Plain(md) == "" {
		return chat.Message{}, false
	}
	return chat.Message{AuthorID: m.From.User.ID, Author: m.From.User.DisplayName, Sent: m.Created.UTC(), Markdown: md}, true
}

// thread builds the item of a root message and its replies. It returns nil
// when no message of the thread is kept.
func (k *Connector) thread(part, channel string, root message, replies []message) *content.Item {
	var ms []chat.Message
	if m, ok := kept(root); ok {
		ms = append(ms, m)
	}
	sorted := slices.Clone(replies)
	slices.SortStableFunc(sorted, func(a, b message) int { return a.Created.Compare(b.Created) })
	for _, r := range sorted {
		if m, ok := kept(r); ok {
			ms = append(ms, m)
		}
	}
	t := chat.Thread{ExternalID: part + "/" + root.ID, Channel: channel, URL: root.WebURL, Revision: revision(newest(root, replies)),
		PersonPrefix: "teams:" + strings.ToLower(k.options.Tenant) + ":user:", Messages: ms}
	return t.Item()
}

// replies returns all replies of a root: those that the listing carries,
// and the rest through the next link of the replies.
func (k *Connector) replies(ctx context.Context, root message) ([]message, error) {
	out := slices.Clone(root.Replies)
	if root.RepliesNext == "" {
		return out, nil
	}
	_, err := pages(ctx, k.c, "", nil, root.RepliesNext, func(rs []message) error {
		out = append(out, rs...)
		return nil
	})
	return out, err
}

// list calls fn for each thread of a channel that the retention keeps,
// with its item. It lists the roots 50 at a time with up to 200 replies
// each, and follows the next link of the replies of a longer thread.
func (k *Connector) list(ctx context.Context, part string, fn func(connectors.Ref) error) error {
	channel, err := k.channelName(ctx, part)
	if err != nil {
		return err
	}
	q := url.Values{"$top": {fmt.Sprint(pageSize)}, "$expand": {"replies"}}
	_, err = pages(ctx, k.c, channelPath(part)+"/messages", q, "", func(roots []message) error {
		for _, root := range roots {
			rs, err := k.replies(ctx, root)
			if err != nil {
				return err
			}
			at := newest(root, rs)
			if !k.keep.Keep(at) {
				continue
			}
			it := k.thread(part, channel, root, rs)
			if it == nil {
				continue
			}
			it.Part = part
			if err := fn(connectors.Ref{ID: it.ExternalID, Updated: at, Version: it.Revision, Item: it}); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// Changed lists the threads of a channel whose newest reply is at or after
// since. Sync reads Teams through Delta.
func (k *Connector) Changed(ctx context.Context, part connectors.Part, since time.Time, fn func(connectors.Ref) error) error {
	return k.list(ctx, part.Key, func(r connectors.Ref) error {
		if r.Updated.Before(since) {
			return nil
		}
		return fn(r)
	})
}

// Delta reads the changes of a channel. The first sync lists every thread,
// because a delta read returns only the last 8 months, and then starts a
// delta read from the time of the listing. Later syncs follow the stored
// delta link and fetch each thread whose root or reply changed. An expired
// delta link starts over with a listing. The cursor is the delta link.
func (k *Connector) Delta(ctx context.Context, part connectors.Part, cursor string, fn func(connectors.Ref) error) (string, error) {
	if cursor != "" {
		next, err := k.follow(ctx, part.Key, "", nil, cursor, fn)
		var se *connectors.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusGone {
			return next, err
		}
	}
	start := k.now().Add(-deltaOverlap).UTC()
	if err := k.list(ctx, part.Key, fn); err != nil {
		return "", err
	}
	q := url.Values{"$filter": {"lastModifiedDateTime gt " + start.Format("2006-01-02T15:04:05.000Z")}}
	return k.follow(ctx, part.Key, channelPath(part.Key)+"/messages/delta", q, "", fn)
}

// follow reads a delta collection and calls fn once for each thread with a
// changed root or reply. It returns the next delta link.
func (k *Connector) follow(ctx context.Context, part, path string, q url.Values, link string, fn func(connectors.Ref) error) (string, error) {
	seen := map[string]bool{}
	return pages(ctx, k.c, path, q, link, func(ms []message) error {
		for _, m := range ms {
			root := m.ID
			if m.ReplyToID != "" {
				root = m.ReplyToID
			}
			if root == "" || seen[root] {
				continue
			}
			seen[root] = true
			if err := fn(connectors.Ref{ID: part + "/" + root}); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListIDs lists the threads of a channel that the retention keeps.
func (k *Connector) ListIDs(ctx context.Context, part connectors.Part, fn func(id string) error) error {
	return k.list(ctx, part.Key, func(r connectors.Ref) error { return fn(r.ID) })
}

// ListRefs lists the threads of a channel that the retention keeps, with
// their revisions and items.
func (k *Connector) ListRefs(ctx context.Context, part connectors.Part, fn func(connectors.Ref) error) error {
	return k.list(ctx, part.Key, fn)
}

// Fetch reads one thread: the root message and all its replies. A deleted
// thread, or one without kept messages, is gone. A thread whose newest
// message is older than the retention is skipped.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	team, channel, rootID, ok := chat.Split(id)
	if !ok {
		return nil, connectors.ErrNotFound
	}
	part := team + "/" + channel
	var root message
	if err := k.c.GetJSON(ctx, channelPath(part)+"/messages/"+url.PathEscape(rootID), nil, &root); err != nil {
		return nil, err
	}
	var rs []message
	_, err := pages(ctx, k.c, channelPath(part)+"/messages/"+url.PathEscape(rootID)+"/replies", url.Values{"$top": {fmt.Sprint(pageSize)}}, "",
		func(page []message) error {
			rs = append(rs, page...)
			return nil
		})
	if err != nil {
		return nil, err
	}
	if !k.keep.Keep(newest(root, rs)) {
		return nil, &connectors.SkipError{Reason: "retention"}
	}
	name, err := k.channelName(ctx, part)
	if err != nil {
		return nil, err
	}
	it := k.thread(part, name, root, rs)
	if it == nil {
		return nil, connectors.ErrNotFound
	}
	it.Part = part
	return it, nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
