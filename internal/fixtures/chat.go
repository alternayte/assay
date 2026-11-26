package fixtures

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The chat corpus: a Slack workspace under /slack/api and a Microsoft Teams
// tenant in the Graph API of the fake Entra ID tenant. Message times count
// back from the time the corpus loads, so that retention tests see the same
// ages on every day.

// SlackTeam is the workspace ID of the fake Slack workspace.
const SlackTeam = "T0ACME"

// The Slack channels of the corpus. The bot is not in random.
const (
	SlackPayments = "C0PAYMENTS"
	SlackPlatform = "C0PLATFORM"
	SlackRandom   = "C0RANDOM"
)

// The Teams teams and channels of the corpus. Leads is a private channel.
const (
	TeamsPayments      = "7e2a0000-0000-4000-c000-000000000001"
	TeamsPlatform      = "7e2a0000-0000-4000-c000-000000000002"
	TeamsDesign        = "19:payments-design@thread.tacv2"
	TeamsLeads         = "19:payments-leads@thread.tacv2"
	TeamsPlatformChats = "19:platform-general@thread.tacv2"
)

type slackCorpus struct {
	Team struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
		Bot  string `json:"bot"`
	} `json:"team"`
	Users []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		RealName    string `json:"realName"`
		DisplayName string `json:"displayName"`
		Bot         bool   `json:"bot"`
	} `json:"users"`
	Channels []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Member  bool   `json:"member"`
		Threads []struct {
			Messages []struct {
				User     string  `json:"user"`
				Bot      string  `json:"bot"`
				Subtype  string  `json:"subtype"`
				HoursAgo float64 `json:"hoursAgo"`
				Text     string  `json:"text"`
			} `json:"messages"`
		} `json:"threads"`
	} `json:"channels"`
}

// SlackMessage is one message of the fake workspace.
type SlackMessage struct {
	TS, ThreadTS, User, Bot, Subtype, Text string
}

type slackChannel struct {
	id, name string
	member   bool
	// threads holds the messages of each thread by root ts, the root first.
	threads map[string][]*SlackMessage
}

// slackState is the fake Slack workspace.
type slackState struct {
	corpus   slackCorpus
	channels []*slackChannel
	seq      int
}

func loadJSON(name string, out any) {
	b, err := corpus.ReadFile("corpus/chat/" + name)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		panic(fmt.Sprintf("fixtures: %s: %v", name, err))
	}
}

// ago returns a time before base. The sequence keeps equal times apart.
func ago(base time.Time, hours float64, seq int) time.Time {
	return base.Add(-time.Duration(hours * float64(time.Hour))).Truncate(time.Second).Add(time.Duration(seq) * time.Microsecond)
}

func slackTS(t time.Time) string { return fmt.Sprintf("%d.%06d", t.Unix(), t.Nanosecond()/1000) }

func loadSlack(base time.Time) *slackState {
	st := &slackState{}
	loadJSON("slack.json", &st.corpus)
	for _, c := range st.corpus.Channels {
		ch := &slackChannel{id: c.ID, name: c.Name, member: c.Member, threads: map[string][]*SlackMessage{}}
		for _, th := range c.Threads {
			var msgs []*SlackMessage
			for _, m := range th.Messages {
				st.seq++
				ts := slackTS(ago(base, m.HoursAgo, st.seq))
				msg := &SlackMessage{TS: ts, User: m.User, Bot: m.Bot, Subtype: m.Subtype, Text: m.Text}
				if len(msgs) > 0 {
					msg.ThreadTS = msgs[0].TS
				}
				msgs = append(msgs, msg)
			}
			if len(msgs) > 1 {
				msgs[0].ThreadTS = msgs[0].TS
			}
			ch.threads[msgs[0].TS] = msgs
		}
		st.channels = append(st.channels, ch)
	}
	return st
}

func (st *slackState) channel(id string) *slackChannel {
	for _, c := range st.channels {
		if c.id == id {
			return c
		}
	}
	return nil
}

// SlackThreads returns the root ts of each thread of a channel, oldest
// first.
func (s *Sources) SlackThreads(channel string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.slack.channel(channel)
	if c == nil {
		return nil
	}
	var out []string
	for ts := range c.threads {
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool { return tsLess(out[i], out[j]) })
	return out
}

func tsLess(a, b string) bool {
	fa, _ := strconv.ParseFloat(a, 64)
	fb, _ := strconv.ParseFloat(b, 64)
	return fa < fb
}

// AddSlackReply adds a reply of a user to a thread now and returns its ts.
func (s *Sources) AddSlackReply(channel, rootTS, user, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.slack.channel(channel)
	if c == nil || c.threads[rootTS] == nil {
		return ""
	}
	s.slack.seq++
	ts := slackTS(s.now().Truncate(time.Second).Add(time.Duration(s.slack.seq) * time.Microsecond))
	msgs := c.threads[rootTS]
	msgs[0].ThreadTS = rootTS
	c.threads[rootTS] = append(msgs, &SlackMessage{TS: ts, ThreadTS: rootTS, User: user, Text: text})
	return ts
}

// DeleteSlackThread removes a thread from a channel.
func (s *Sources) DeleteSlackThread(channel, rootTS string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.slack.channel(channel); c != nil {
		delete(c.threads, rootTS)
	}
}

func slackError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": code})
}

// slackPage returns one page of list by the cursor of the request: the
// offset of the page.
func slackPage[T any](q url.Values, list []T, size int) ([]T, string) {
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < size {
		size = n
	}
	off, _ := strconv.Atoi(q.Get("cursor"))
	off = min(max(off, 0), len(list))
	end := min(off+size, len(list))
	next := ""
	if end < len(list) {
		next = strconv.Itoa(end)
	}
	return list[off:end], next
}

// slackPageSize is the most messages of one fake page, so that paging runs.
const slackPageSize = 3

// serveSlack serves the Slack Web API methods that Assay reads. Like Slack,
// it answers 200 with ok false for a bad token.
func (s *Sources) serveSlack(w http.ResponseWriter, r *http.Request, authorized bool) {
	if r.Method != http.MethodGet {
		slackError(w, "method_not_supported_for_channel_type")
		return
	}
	if !authorized {
		slackError(w, "invalid_auth")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.slack
	q := r.URL.Query()
	switch strings.TrimPrefix(r.URL.Path, "/slack/api/") {
	case "auth.test":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": st.corpus.Team.URL, "team": st.corpus.Team.Name,
			"user": st.corpus.Team.Bot, "team_id": st.corpus.Team.ID, "user_id": "U0ASSAY", "bot_id": "B0ASSAY"})
	case "conversations.list":
		if q.Get("types") != "public_channel" {
			slackError(w, "invalid_types")
			return
		}
		var all []map[string]any
		for _, c := range st.channels {
			all = append(all, map[string]any{"id": c.id, "name": c.name, "is_member": c.member, "is_archived": false, "is_private": false})
		}
		page, next := slackPage(q, all, 2)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": page, "response_metadata": map[string]string{"next_cursor": next}})
	case "conversations.info":
		c := st.channel(q.Get("channel"))
		if c == nil {
			slackError(w, "channel_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": map[string]any{"id": c.id, "name": c.name, "is_member": c.member}})
	case "users.info":
		for _, u := range st.corpus.Users {
			if u.ID == q.Get("user") {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": map[string]any{"id": u.ID, "name": u.Name, "real_name": u.RealName,
					"is_bot": u.Bot, "profile": map[string]any{"real_name": u.RealName, "display_name": u.DisplayName}}})
				return
			}
		}
		slackError(w, "user_not_found")
	case "conversations.history":
		c := st.channel(q.Get("channel"))
		if c == nil {
			slackError(w, "channel_not_found")
			return
		}
		if !c.member {
			slackError(w, "not_in_channel")
			return
		}
		oldest, _ := strconv.ParseFloat(q.Get("oldest"), 64)
		var roots []map[string]any
		for ts, msgs := range c.threads {
			if f, _ := strconv.ParseFloat(ts, 64); q.Get("oldest") != "" && f < oldest {
				continue
			}
			m := slackJSON(msgs[0])
			if len(msgs) > 1 {
				m["reply_count"] = len(msgs) - 1
				m["latest_reply"] = msgs[len(msgs)-1].TS
			}
			roots = append(roots, m)
		}
		// Slack lists the newest message first.
		sort.Slice(roots, func(i, j int) bool { return tsLess(roots[j]["ts"].(string), roots[i]["ts"].(string)) })
		page, next := slackPage(q, roots, slackPageSize)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": orEmptyAny(page), "has_more": next != "",
			"response_metadata": map[string]string{"next_cursor": next}})
	case "conversations.replies":
		c := st.channel(q.Get("channel"))
		if c == nil {
			slackError(w, "channel_not_found")
			return
		}
		if !c.member {
			slackError(w, "not_in_channel")
			return
		}
		msgs := c.threads[q.Get("ts")]
		if msgs == nil {
			slackError(w, "thread_not_found")
			return
		}
		var all []map[string]any
		for i, m := range msgs {
			j := slackJSON(m)
			if i == 0 && len(msgs) > 1 {
				j["reply_count"] = len(msgs) - 1
				j["latest_reply"] = msgs[len(msgs)-1].TS
			}
			all = append(all, j)
		}
		// Each page starts with the root, as Slack answers.
		page, next := slackPage(q, all[1:], slackPageSize-1)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": append([]map[string]any{all[0]}, page...), "has_more": next != "",
			"response_metadata": map[string]string{"next_cursor": next}})
	default:
		slackError(w, "unknown_method")
	}
}

func slackJSON(m *SlackMessage) map[string]any {
	out := map[string]any{"type": "message", "ts": m.TS, "user": m.User, "text": m.Text}
	if m.ThreadTS != "" {
		out["thread_ts"] = m.ThreadTS
	}
	if m.Bot != "" {
		out["bot_id"] = m.Bot
	}
	if m.Subtype != "" {
		out["subtype"] = m.Subtype
	}
	return out
}

func orEmptyAny(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

type teamsCorpus struct {
	Teams []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Channels []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Membership string `json:"membership"`
			Threads    []struct {
				Messages []struct {
					User     string  `json:"user"`
					Name     string  `json:"name"`
					App      string  `json:"app"`
					Type     string  `json:"type"`
					HoursAgo float64 `json:"hoursAgo"`
					HTML     string  `json:"html"`
					Deleted  bool    `json:"deleted"`
				} `json:"messages"`
			} `json:"threads"`
		} `json:"channels"`
	} `json:"teams"`
}

// TeamsMessage is one channel message of the fake tenant.
type TeamsMessage struct {
	ID, ReplyTo, User, Name, App, Type, HTML string
	Created, Modified                        time.Time
	Deleted                                  bool
	// version is the change counter of the last change, for delta reads.
	version int
}

type teamsChannel struct {
	id, name, membership string
	threads              map[string][]*TeamsMessage
}

type teamsTeam struct {
	id, name string
	channels []*teamsChannel
}

type teamsState struct {
	teams   []*teamsTeam
	version int
}

func loadTeams(base time.Time) *teamsState {
	var c teamsCorpus
	loadJSON("teams.json", &c)
	st := &teamsState{}
	seq := 0
	for _, t := range c.Teams {
		team := &teamsTeam{id: t.ID, name: t.Name}
		for _, ch := range t.Channels {
			channel := &teamsChannel{id: ch.ID, name: ch.Name, membership: ch.Membership, threads: map[string][]*TeamsMessage{}}
			for _, th := range ch.Threads {
				var msgs []*TeamsMessage
				for _, m := range th.Messages {
					seq++
					at := ago(base, m.HoursAgo, seq*1000)
					typ := m.Type
					if typ == "" {
						typ = "message"
					}
					msg := &TeamsMessage{ID: strconv.FormatInt(at.UnixMilli(), 10), User: m.User, Name: m.Name, App: m.App, Type: typ,
						HTML: m.HTML, Created: at, Modified: at, Deleted: m.Deleted}
					if len(msgs) > 0 {
						msg.ReplyTo = msgs[0].ID
					}
					msgs = append(msgs, msg)
				}
				channel.threads[msgs[0].ID] = msgs
			}
			team.channels = append(team.channels, channel)
		}
		st.teams = append(st.teams, team)
	}
	return st
}

func (st *teamsState) channel(team, channel string) (*teamsTeam, *teamsChannel) {
	for _, t := range st.teams {
		if t.id != team {
			continue
		}
		for _, c := range t.channels {
			if c.id == channel {
				return t, c
			}
		}
		return t, nil
	}
	return nil, nil
}

// TeamsThreads returns the root IDs of the threads of a channel, oldest
// first.
func (d *Identity) TeamsThreads(team, channel string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, c := d.chat.channel(team, channel)
	if c == nil {
		return nil
	}
	var out []string
	for id := range c.threads {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return c.threads[out[i]][0].Created.Before(c.threads[out[j]][0].Created) })
	return out
}

// AddTeamsReply adds a reply of a directory user to a thread now and
// returns its ID.
func (d *Identity) AddTeamsReply(team, channel, rootID, userID, html string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, c := d.chat.channel(team, channel)
	if c == nil || c.threads[rootID] == nil {
		return ""
	}
	u := d.users[userID]
	name := ""
	if u != nil {
		name = u.Name
	}
	now := d.Now().UTC()
	d.chat.version++
	m := &TeamsMessage{ID: strconv.FormatInt(now.UnixMilli(), 10), ReplyTo: rootID, User: userID, Name: name, Type: "message", HTML: html,
		Created: now, Modified: now, version: d.chat.version}
	c.threads[rootID] = append(c.threads[rootID], m)
	return m.ID
}

// teamsPageSize and teamsReplies are the page sizes of the fake Graph, so
// that the next links of messages and of replies run.
const (
	teamsPageSize = 2
	teamsReplies  = 2
)

func graphTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (d *Identity) teamsMessageJSON(team, channel string, m *TeamsMessage) map[string]any {
	out := map[string]any{"id": m.ID, "replyToId": nil, "messageType": m.Type, "createdDateTime": graphTime(m.Created),
		"lastModifiedDateTime": graphTime(m.Modified), "deletedDateTime": nil, "subject": nil,
		"body":   map[string]string{"contentType": "html", "content": m.HTML},
		"webUrl": "https://teams.acme.test/l/message/" + url.PathEscape(channel) + "/" + m.ID + "?groupId=" + team}
	if m.ReplyTo != "" {
		out["replyToId"] = m.ReplyTo
	}
	if m.Deleted {
		out["deletedDateTime"] = graphTime(m.Modified)
		out["body"] = map[string]string{"contentType": "html", "content": ""}
	}
	switch {
	case m.App != "":
		out["from"] = map[string]any{"user": nil, "application": map[string]string{"id": "app-1", "displayName": m.App}}
	case m.User != "":
		out["from"] = map[string]any{"user": map[string]string{"id": m.User, "displayName": m.Name}, "application": nil}
	default:
		out["from"] = nil
	}
	return out
}

// graphPage writes one page of list. A $skiptoken is the offset of the
// page.
func graphPage(w http.ResponseWriter, q url.Values, link string, list []map[string]any, size int, extra map[string]any) {
	if n, err := strconv.Atoi(q.Get("$top")); err == nil && n > 0 && n < size {
		size = n
	}
	off, _ := strconv.Atoi(q.Get("$skiptoken"))
	off = min(max(off, 0), len(list))
	end := min(off+size, len(list))
	out := map[string]any{"value": orEmptyAny(list[off:end])}
	if end < len(list) {
		next := url.Values{}
		for k, v := range q {
			if k != "$skiptoken" {
				next[k] = v
			}
		}
		next.Set("$skiptoken", strconv.Itoa(end))
		out["@odata.nextLink"] = link + "?" + next.Encode()
	}
	for k, v := range extra {
		if end >= len(list) {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// teams serves the Teams reads of Graph. The lock is held.
func (d *Identity) teams(w http.ResponseWriter, r *http.Request, base, path string) bool {
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if parts[0] != "teams" {
		return false
	}
	notFound := func() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "NotFound", "message": "No such object."}})
	}
	if len(parts) == 1 {
		var list []map[string]any
		for _, t := range d.chat.teams {
			list = append(list, map[string]any{"id": t.id, "displayName": t.name})
		}
		graphPage(w, q, base+"/teams", list, teamsPageSize, nil)
		return true
	}
	team, ch := d.chat.channel(parts[1], "")
	if len(parts) > 3 {
		team, ch = d.chat.channel(parts[1], parts[3])
	}
	if team == nil {
		notFound()
		return true
	}
	switch {
	case len(parts) == 2:
		writeJSON(w, http.StatusOK, map[string]any{"id": team.id, "displayName": team.name})
		return true
	case len(parts) == 3 && parts[2] == "channels":
		var list []map[string]any
		for _, c := range team.channels {
			list = append(list, map[string]any{"id": c.id, "displayName": c.name, "membershipType": c.membership})
		}
		graphPage(w, q, base+path, list, 50, nil)
		return true
	case len(parts) < 4 || parts[2] != "channels" || ch == nil:
		notFound()
		return true
	}
	if ch.membership != "standard" {
		// App-only access does not read private channels here.
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "Forbidden"}})
		return true
	}
	link := base + "/teams/" + team.id + "/channels/" + url.PathEscape(ch.id)
	roots := make([]string, 0, len(ch.threads))
	for id := range ch.threads {
		roots = append(roots, id)
	}
	// Graph lists the newest root first.
	sort.Slice(roots, func(i, j int) bool { return ch.threads[roots[i]][0].Created.After(ch.threads[roots[j]][0].Created) })
	switch {
	case len(parts) == 4:
		writeJSON(w, http.StatusOK, map[string]any{"id": ch.id, "displayName": ch.name, "membershipType": ch.membership})
	case len(parts) == 5 && parts[4] == "messages":
		expand := q.Get("$expand") == "replies"
		var list []map[string]any
		for _, id := range roots {
			msgs := ch.threads[id]
			m := d.teamsMessageJSON(team.id, ch.id, msgs[0])
			if expand {
				var reps []map[string]any
				for _, rep := range msgs[1:min(len(msgs), 1+teamsReplies)] {
					reps = append(reps, d.teamsMessageJSON(team.id, ch.id, rep))
				}
				m["replies"] = orEmptyAny(reps)
				if len(msgs)-1 > teamsReplies {
					m["replies@odata.nextLink"] = link + "/messages/" + id + "/replies?$skiptoken=" + strconv.Itoa(teamsReplies)
				}
			}
			list = append(list, m)
		}
		graphPage(w, q, link+"/messages", list, teamsPageSize, nil)
	case len(parts) == 6 && parts[4] == "messages" && parts[5] == "delta":
		d.teamsDelta(w, q, link, team.id, ch)
	case len(parts) == 6 && parts[4] == "messages":
		msgs := ch.threads[parts[5]]
		if msgs == nil {
			notFound()
			return true
		}
		writeJSON(w, http.StatusOK, d.teamsMessageJSON(team.id, ch.id, msgs[0]))
	case len(parts) == 7 && parts[4] == "messages" && parts[6] == "replies":
		msgs := ch.threads[parts[5]]
		if msgs == nil {
			notFound()
			return true
		}
		var list []map[string]any
		for _, rep := range msgs[1:] {
			list = append(list, d.teamsMessageJSON(team.id, ch.id, rep))
		}
		graphPage(w, q, link+"/messages/"+parts[5]+"/replies", list, teamsPageSize, nil)
	default:
		notFound()
	}
	return true
}

// teamsDelta serves /messages/delta. A $deltatoken is the change counter of
// the read. A first read takes $filter=lastModifiedDateTime gt <time>.
func (d *Identity) teamsDelta(w http.ResponseWriter, q url.Values, link, team string, ch *teamsChannel) {
	since := -1
	var after time.Time
	switch {
	case q.Get("$deltatoken") != "":
		n, err := strconv.Atoi(q.Get("$deltatoken"))
		if err != nil || n > d.chat.version {
			writeJSON(w, http.StatusGone, map[string]any{"error": map[string]string{"code": "SyncStateNotFound"}})
			return
		}
		since = n
	case q.Get("$filter") != "":
		v, ok := strings.CutPrefix(q.Get("$filter"), "lastModifiedDateTime gt ")
		t, err := time.Parse(time.RFC3339, v)
		if !ok || err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "BadRequest"}})
			return
		}
		after = t
	}
	var list []map[string]any
	for _, msgs := range ch.threads {
		for _, m := range msgs {
			if (since >= 0 && m.version > since) || (since < 0 && m.Modified.After(after)) {
				list = append(list, d.teamsMessageJSON(team, ch.id, m))
			}
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i]["id"].(string) < list[j]["id"].(string) })
	q2 := url.Values{}
	for k, v := range q {
		q2[k] = slices.Clone(v)
	}
	graphPage(w, q2, link+"/messages/delta", list, teamsPageSize,
		map[string]any{"@odata.deltaLink": link + "/messages/delta?$deltatoken=" + strconv.Itoa(d.chat.version)})
}
