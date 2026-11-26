// Package slack reads the threads of public Slack channels through the Web
// API with the bot token of an internal app that the customer creates in
// its own workspace, with the scopes channels:read, channels:history and
// users:read. An internal app is exempt from the limit of one history
// request per minute for apps outside the Marketplace. Every call is a GET
// with the token as Bearer. Each thread is an item and each message a
// passage.
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/chat"
	"github.com/alternayte/assay/internal/content"
)

// APIURL is the Slack Web API.
const APIURL = "https://slack.com/api"

// DefaultPerSecond and DefaultConcurrency are the default rate of a Slack
// connection: Tier 3 methods allow about 50 requests per minute.
const (
	DefaultPerSecond   = 0.8
	DefaultConcurrency = 2
)

// lookback is how far before the cursor a sync reads the history.
// conversations.history does not return an old root that got a new reply;
// reconciliation reads the whole history.
const lookback = 30 * 24 * time.Hour

// pageSize is the page size of the history and replies calls.
const pageSize = 200

// Scope selects the channels of a connection.
type Scope struct {
	Channels []string `json:"channels,omitempty" doc:"Public channel IDs where the bot is a member."`
}

// ParseScope reads the scope of a connection.
func ParseScope(raw json.RawMessage) (Scope, error) {
	var s Scope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, fmt.Errorf("slack: the scope is not valid: %w", err)
		}
	}
	for _, c := range s.Channels {
		if c == "" || strings.ContainsAny(c, "/?#&") {
			return s, fmt.Errorf("slack: %q is not a channel ID", c)
		}
	}
	return s, nil
}

// Connector reads the channels of one connection.
type Connector struct {
	c     *connectors.Client
	scope Scope
	keep  chat.Retention

	mu       gosync.Mutex
	auth     *authInfo
	users    map[string]user
	channels map[string]string
}

// New returns a connector. scope and options are the JSON scope and options
// of the connection. now is the clock of the retention.
func New(c *connectors.Client, scope, options json.RawMessage, now func() time.Time) (*Connector, error) {
	s, err := ParseScope(scope)
	if err != nil {
		return nil, err
	}
	days, err := chat.ParseRetainDays(options)
	if err != nil {
		return nil, err
	}
	return &Connector{c: c, scope: s, keep: chat.Retention{Days: days, Now: now}, users: map[string]user{}, channels: map[string]string{}}, nil
}

// answer is the envelope of every Web API answer.
type answer struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error"`
	Metadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// refused are the errors of a token that cannot read: sync pauses the
// connection.
var refused = []string{"invalid_auth", "not_authed", "account_inactive", "token_revoked", "token_expired", "missing_scope",
	"no_permission", "not_allowed_token_type", "ekm_access_denied"}

// call sends GET /<method> and decodes the answer into out, which embeds
// answer. Slack answers 200 with ok false for an error.
func (k *Connector) call(ctx context.Context, method string, q url.Values, out any, env func() answer) error {
	if err := k.c.GetJSON(ctx, "/"+method, q, out); err != nil {
		return err
	}
	a := env()
	if a.OK {
		return nil
	}
	switch {
	case slices.Contains(refused, a.Error):
		return fmt.Errorf("%w: Slack answered %s for %s", connectors.ErrUnauthorized, a.Error, method)
	case a.Error == "channel_not_found" || a.Error == "thread_not_found" || a.Error == "message_not_found" || a.Error == "user_not_found":
		return connectors.ErrNotFound
	case a.Error == "not_in_channel":
		return fmt.Errorf("slack: the bot is not in channel %s. Invite the bot to the channel", q.Get("channel"))
	}
	return fmt.Errorf("slack: Slack answered %s for %s", a.Error, method)
}

type authInfo struct {
	answer
	URL    string `json:"url"`
	Team   string `json:"team"`
	User   string `json:"user"`
	TeamID string `json:"team_id"`
}

// whoami returns the workspace of the token, read once.
func (k *Connector) whoami(ctx context.Context) (*authInfo, error) {
	k.mu.Lock()
	a := k.auth
	k.mu.Unlock()
	if a != nil {
		return a, nil
	}
	var out authInfo
	if err := k.call(ctx, "auth.test", nil, &out, func() answer { return out.answer }); err != nil {
		return nil, err
	}
	if out.TeamID == "" {
		return nil, errors.New("slack: auth.test returned no team_id")
	}
	if !strings.HasSuffix(out.URL, "/") {
		out.URL += "/"
	}
	k.mu.Lock()
	k.auth = &out
	k.mu.Unlock()
	return &out, nil
}

type channel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsMember   bool   `json:"is_member"`
	IsArchived bool   `json:"is_archived"`
	IsPrivate  bool   `json:"is_private"`
}

// memberChannels lists the public channels where the bot is a member.
func (k *Connector) memberChannels(ctx context.Context) ([]channel, error) {
	var out []channel
	q := url.Values{"types": {"public_channel"}, "exclude_archived": {"true"}, "limit": {strconv.Itoa(pageSize)}}
	for {
		var page struct {
			answer
			Channels []channel `json:"channels"`
		}
		if err := k.call(ctx, "conversations.list", q, &page, func() answer { return page.answer }); err != nil {
			return nil, err
		}
		for _, c := range page.Channels {
			if c.IsMember && !c.IsArchived && !c.IsPrivate {
				out = append(out, c)
			}
		}
		if page.Metadata.NextCursor == "" {
			return out, nil
		}
		q.Set("cursor", page.Metadata.NextCursor)
	}
}

// Validate checks the token and counts the public channels where the bot
// is a member.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	a, err := k.whoami(ctx)
	if err != nil {
		return connectors.Validation{}, err
	}
	cs, err := k.memberChannels(ctx)
	if err != nil {
		return connectors.Validation{}, err
	}
	return connectors.Validation{User: a.User, Sample: len(cs)}, nil
}

// Scopes lists the public channels where the bot is a member. Joining a
// channel is a write: an admin invites the bot.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
	cs, err := k.memberChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]connectors.Scope, 0, len(cs))
	for _, c := range cs {
		out = append(out, connectors.Scope{ID: c.ID, Name: "#" + c.Name})
	}
	return out, nil
}

// Parts are the selected channels.
func (k *Connector) Parts(context.Context) ([]connectors.Part, error) {
	out := make([]connectors.Part, 0, len(k.scope.Channels))
	for _, c := range k.scope.Channels {
		out = append(out, connectors.Part{Key: c})
	}
	return out, nil
}

// channelName returns #<name> of a channel, read once per run.
func (k *Connector) channelName(ctx context.Context, id string) (string, error) {
	k.mu.Lock()
	name, ok := k.channels[id]
	k.mu.Unlock()
	if ok {
		return name, nil
	}
	var out struct {
		answer
		Channel channel `json:"channel"`
	}
	if err := k.call(ctx, "conversations.info", url.Values{"channel": {id}}, &out, func() answer { return out.answer }); err != nil {
		return "", err
	}
	name = "#" + out.Channel.Name
	k.mu.Lock()
	k.channels[id] = name
	k.mu.Unlock()
	return name, nil
}

type user struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	IsBot    bool   `json:"is_bot"`
	Profile  struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

// display is the name that people see, and the other names.
func (u user) display() (string, []string) {
	var names []string
	for _, n := range []string{u.Profile.RealName, u.RealName, u.Profile.DisplayName, u.Name} {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return u.ID, nil
	}
	return names[0], names[1:]
}

// user returns the user of an ID with users.info, read once per run:
// messages carry only user IDs.
func (k *Connector) user(ctx context.Context, id string) (user, error) {
	k.mu.Lock()
	u, ok := k.users[id]
	k.mu.Unlock()
	if ok {
		return u, nil
	}
	var out struct {
		answer
		User user `json:"user"`
	}
	err := k.call(ctx, "users.info", url.Values{"user": {id}}, &out, func() answer { return out.answer })
	if errors.Is(err, connectors.ErrNotFound) {
		out.User, err = user{ID: id}, nil
	}
	if err != nil {
		return user{}, err
	}
	k.mu.Lock()
	k.users[id] = out.User
	k.mu.Unlock()
	return out.User, nil
}

type message struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	User        string `json:"user"`
	BotID       string `json:"bot_id"`
	Text        string `json:"text"`
	ReplyCount  int    `json:"reply_count"`
	LatestReply string `json:"latest_reply"`
}

// keptSubtypes are the message subtypes that hold what a person wrote.
// Bot messages, join and leave events and other events are dropped.
var keptSubtypes = []string{"", "thread_broadcast", "file_share", "me_message"}

func (m message) person() bool {
	return m.User != "" && m.BotID == "" && slices.Contains(keptSubtypes, m.Subtype)
}

// root reports whether a history message starts a thread. A reply that is
// also sent to the channel is not a root.
func (m message) root() bool { return m.ThreadTS == "" || m.ThreadTS == m.TS }

// revision is the newest reply time, or the root time without replies.
func (m message) revision() string {
	if m.LatestReply != "" {
		return m.LatestReply
	}
	return m.TS
}

// Time parses a Slack timestamp, such as 1715000000.123456.
func Time(ts string) time.Time {
	sec, frac, _ := strings.Cut(ts, ".")
	s, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}
	}
	frac = (frac + "000000")[:6]
	us, _ := strconv.ParseInt(frac, 10, 64)
	return time.Unix(s, us*1000).UTC()
}

// TS formats a time as a Slack timestamp.
func TS(t time.Time) string { return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000) }

// Permalink is the link of a message without an extra request.
func Permalink(workspaceURL, channel, ts string) string {
	return strings.TrimRight(workspaceURL, "/") + "/archives/" + channel + "/p" + strings.Replace(ts, ".", "", 1)
}

// history calls fn for each page of messages of a channel newer than
// oldest, or of the whole history when oldest is empty.
func (k *Connector) history(ctx context.Context, ch, oldest string, fn func([]message) error) error {
	q := url.Values{"channel": {ch}, "limit": {strconv.Itoa(pageSize)}}
	if oldest != "" {
		q.Set("oldest", oldest)
		q.Set("inclusive", "true")
	}
	for {
		var page struct {
			answer
			Messages []message `json:"messages"`
		}
		if err := k.call(ctx, "conversations.history", q, &page, func() answer { return page.answer }); err != nil {
			return err
		}
		if err := fn(page.Messages); err != nil {
			return err
		}
		if page.Metadata.NextCursor == "" {
			return nil
		}
		q.Set("cursor", page.Metadata.NextCursor)
	}
}

// scan reads the roots of a channel newer than oldest that the retention
// keeps, and calls fn for each, oldest reply time first. A root without
// replies comes with its item, so that it needs no replies call.
func (k *Connector) scan(ctx context.Context, ch, oldest string, fn func(connectors.Ref) error) error {
	var roots []message
	err := k.history(ctx, ch, oldest, func(ms []message) error {
		for _, m := range ms {
			if !m.root() || (!m.person() && m.ReplyCount == 0) {
				continue
			}
			if !k.keep.Keep(Time(m.revision())) {
				continue
			}
			roots = append(roots, m)
		}
		return nil
	})
	if err != nil {
		return err
	}
	slices.SortStableFunc(roots, func(a, b message) int { return Time(a.revision()).Compare(Time(b.revision())) })
	a, err := k.whoami(ctx)
	if err != nil {
		return err
	}
	for _, m := range roots {
		r := connectors.Ref{ID: a.TeamID + "/" + ch + "/" + m.TS, Updated: Time(m.revision()), Version: m.revision()}
		if m.ReplyCount == 0 {
			it, err := k.thread(ctx, ch, m, []message{m})
			if err != nil {
				return err
			}
			if it == nil {
				continue
			}
			r.Item = it
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

// Changed lists the threads of a channel with a root at most 30 days
// before since, oldest reply first. Sync stores those whose newest reply is
// not the stored revision, and calls conversations.replies only for them.
func (k *Connector) Changed(ctx context.Context, part connectors.Part, since time.Time, fn func(connectors.Ref) error) error {
	oldest := ""
	if !since.IsZero() {
		oldest = TS(since.Add(-lookback))
	}
	return k.scan(ctx, part.Key, oldest, func(r connectors.Ref) error {
		if r.Updated.Before(since) {
			return nil
		}
		return fn(r)
	})
}

// ListIDs lists the threads of the whole history that the retention keeps.
func (k *Connector) ListIDs(ctx context.Context, part connectors.Part, fn func(id string) error) error {
	return k.scan(ctx, part.Key, "", func(r connectors.Ref) error { return fn(r.ID) })
}

// ListRefs lists the threads of the whole history that the retention keeps,
// with their revisions. Reconciliation then fetches the threads that sync
// missed, such as an old thread with a new reply.
func (k *Connector) ListRefs(ctx context.Context, part connectors.Part, fn func(connectors.Ref) error) error {
	return k.scan(ctx, part.Key, "", fn)
}

// Fetch reads one thread with conversations.replies.
func (k *Connector) Fetch(ctx context.Context, id string) (*content.Item, error) {
	team, ch, ts, ok := chat.Split(id)
	if !ok {
		return nil, connectors.ErrNotFound
	}
	a, err := k.whoami(ctx)
	if err != nil {
		return nil, err
	}
	if team != a.TeamID {
		return nil, connectors.ErrNotFound
	}
	q := url.Values{"channel": {ch}, "ts": {ts}, "limit": {strconv.Itoa(pageSize)}}
	var ms []message
	for {
		var page struct {
			answer
			Messages []message `json:"messages"`
		}
		if err := k.call(ctx, "conversations.replies", q, &page, func() answer { return page.answer }); err != nil {
			return nil, err
		}
		for _, m := range page.Messages {
			// Later pages start with the root again.
			if m.TS == ts && len(ms) > 0 {
				continue
			}
			ms = append(ms, m)
		}
		if page.Metadata.NextCursor == "" {
			break
		}
		q.Set("cursor", page.Metadata.NextCursor)
	}
	if len(ms) == 0 || ms[0].TS != ts {
		return nil, connectors.ErrNotFound
	}
	root := ms[0]
	if root.LatestReply == "" {
		for _, m := range ms[1:] {
			if Time(m.TS).After(Time(root.LatestReply)) {
				root.LatestReply = m.TS
			}
		}
	}
	if !k.keep.Keep(Time(root.revision())) {
		return nil, &connectors.SkipError{Reason: "retention"}
	}
	ms[0] = root
	it, err := k.thread(ctx, ch, root, ms)
	if err != nil {
		return nil, err
	}
	if it == nil {
		return nil, connectors.ErrNotFound
	}
	return it, nil
}

// thread builds the item of a root and its messages, the root first. It
// returns nil when no message is kept.
func (k *Connector) thread(ctx context.Context, ch string, root message, ms []message) (*content.Item, error) {
	a, err := k.whoami(ctx)
	if err != nil {
		return nil, err
	}
	name, err := k.channelName(ctx, ch)
	if err != nil {
		return nil, err
	}
	conv := converter{
		user: func(id string) string {
			u, err := k.user(ctx, id)
			if err != nil {
				return id
			}
			n, _ := u.display()
			return n
		},
		channel: func(id string) string {
			n, err := k.channelName(ctx, id)
			if err != nil {
				return id
			}
			return n
		},
	}
	var kept []chat.Message
	for _, m := range ms {
		if !m.person() {
			continue
		}
		md := conv.markdown(m.Text)
		if content.Plain(md) == "" {
			continue
		}
		u, err := k.user(ctx, m.User)
		if err != nil {
			return nil, err
		}
		if u.IsBot {
			continue
		}
		display, aliases := u.display()
		kept = append(kept, chat.Message{AuthorID: m.User, Author: display, Aliases: aliases, Sent: Time(m.TS), Markdown: md})
	}
	t := chat.Thread{ExternalID: a.TeamID + "/" + ch + "/" + root.TS, Channel: name, URL: Permalink(a.URL, ch, root.TS),
		Revision: root.revision(), PersonPrefix: "slack:" + a.TeamID + ":user:", Messages: kept}
	it := t.Item()
	if it != nil {
		it.Part = ch
	}
	return it, nil
}
