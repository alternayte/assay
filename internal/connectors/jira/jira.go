// Package jira reads Jira Data Center through REST v2 with a personal
// access token, and Jira Cloud through REST v3 with an API token. Issue
// fields become field passages and deterministic claims.
package jira

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/content"
)

// Scope selects the issues of a connection.
type Scope struct {
	Projects []string `json:"projects" doc:"Project keys."`
	JQL      string   `json:"jql,omitempty" doc:"Optional JQL that narrows the projects."`
}

// Connector reads one Jira connection.
type Connector struct {
	c      *connectors.Client
	scope  Scope
	rules  connectors.Rules
	limits connectors.Limits
	// cloud reads Jira Cloud: REST v3, Atlassian Document Format and
	// account IDs.
	cloud bool
}

// New returns a connector for Jira Data Center. scope is the JSON scope of
// the connection.
func New(c *connectors.Client, scope json.RawMessage, rules connectors.Rules, limits connectors.Limits) (*Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("jira: the scope is not valid: %w", err)
		}
	}
	return &Connector{c: c, scope: s, rules: rules, limits: limits}, nil
}

// NewCloud returns a connector for Jira Cloud. Its client sends Basic
// authentication with the email and the API token.
func NewCloud(c *connectors.Client, scope json.RawMessage, rules connectors.Rules, limits connectors.Limits) (*Connector, error) {
	k, err := New(c, scope, rules, limits)
	if err != nil {
		return nil, err
	}
	k.cloud = true
	return k, nil
}

// api is the path of the REST API.
func (k *Connector) api() string {
	if k.cloud {
		return "/rest/api/3"
	}
	return "/rest/api/2"
}

const timeLayout = "2006-01-02T15:04:05.000-0700"

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

// jql is the query of the issues in scope. projects narrows it to the
// project of one part; nil means the projects of the scope.
func (k *Connector) jql(projects []string) string {
	if projects == nil {
		projects = k.scope.Projects
	}
	var parts []string
	if len(projects) > 0 {
		q := make([]string, len(projects))
		for i, p := range projects {
			q[i] = strconv.Quote(p)
		}
		parts = append(parts, "project in ("+strings.Join(q, ",")+")")
	}
	if j := strings.TrimSpace(k.scope.JQL); j != "" {
		parts = append(parts, "("+j+")")
	}
	return strings.Join(parts, " AND ")
}

type searchPage struct {
	Total int `json:"total"`
	// NextPageToken pages the search of Jira Cloud. It is empty on the
	// last page.
	NextPageToken string `json:"nextPageToken"`
	Issues        []struct {
		Key    string `json:"key"`
		Fields struct {
			Updated string `json:"updated"`
		} `json:"fields"`
	} `json:"issues"`
}

// Validate checks the token and counts the issues in scope. The search of
// Jira Cloud gives no total, so on Cloud it counts one page of the search.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	var u user
	if err := k.c.GetJSON(ctx, k.api()+"/myself", nil, &u); err != nil {
		return connectors.Validation{}, err
	}
	if k.cloud {
		var p searchPage
		q := url.Values{"jql": {k.jql(nil)}, "maxResults": {strconv.Itoa(searchLimit)}, "fields": {"key"}}
		if err := k.c.GetJSON(ctx, "/rest/api/3/search/jql", q, &p); err != nil {
			return connectors.Validation{}, err
		}
		return connectors.Validation{User: u.display(), Sample: len(p.Issues), More: p.NextPageToken != ""}, nil
	}
	var p searchPage
	if err := k.c.GetJSON(ctx, "/rest/api/2/search", url.Values{"jql": {k.jql(nil)}, "maxResults": {"0"}}, &p); err != nil {
		return connectors.Validation{}, err
	}
	return connectors.Validation{User: u.display(), Sample: p.Total}, nil
}

// Scopes lists the projects.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
	if k.cloud {
		return k.cloudProjects(ctx)
	}
	var ps []struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if err := k.c.GetJSON(ctx, "/rest/api/2/project", nil, &ps); err != nil {
		return nil, err
	}
	out := []connectors.Scope{}
	for _, p := range ps {
		out = append(out, connectors.Scope{ID: p.Key, Name: p.Name})
	}
	return out, nil
}

// cloudProjects pages the project search of Jira Cloud.
func (k *Connector) cloudProjects(ctx context.Context) ([]connectors.Scope, error) {
	out := []connectors.Scope{}
	for start := 0; ; {
		var p struct {
			Values []struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"values"`
			IsLast bool `json:"isLast"`
		}
		q := url.Values{"startAt": {strconv.Itoa(start)}, "maxResults": {"50"}}
		if err := k.c.GetJSON(ctx, "/rest/api/3/project/search", q, &p); err != nil {
			return nil, err
		}
		for _, v := range p.Values {
			out = append(out, connectors.Scope{ID: v.Key, Name: v.Name})
		}
		if p.IsLast || len(p.Values) == 0 {
			return out, nil
		}
		start += len(p.Values)
	}
}

// Parts are the projects of the scope, one part per project. A scope
// without projects reads every project of the token, narrowed by the JQL.
func (k *Connector) Parts(ctx context.Context) ([]connectors.Part, error) {
	keys := k.scope.Projects
	if len(keys) == 0 {
		projects, err := k.Scopes(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			keys = append(keys, p.ID)
		}
	}
	out := make([]connectors.Part, 0, len(keys))
	for _, key := range keys {
		out = append(out, connectors.Part{Key: key})
	}
	return out, nil
}

// searchLimit is the page size of a search.
const searchLimit = 100

func (k *Connector) search(ctx context.Context, jql, fields string, fn func(key, updated string) error) error {
	if k.cloud {
		return k.searchJQL(ctx, jql, fields, fn)
	}
	const limit = searchLimit
	for start := 0; ; start += limit {
		var p searchPage
		q := url.Values{"jql": {jql}, "startAt": {strconv.Itoa(start)}, "maxResults": {strconv.Itoa(limit)}, "fields": {fields}}
		if err := k.c.GetJSON(ctx, "/rest/api/2/search", q, &p); err != nil {
			return err
		}
		for _, is := range p.Issues {
			if err := fn(is.Key, is.Fields.Updated); err != nil {
				return err
			}
		}
		if len(p.Issues) < limit || start+limit >= p.Total {
			return nil
		}
	}
}

// searchJQL pages the search of Jira Cloud by its next page token. Jira
// Cloud removed the search with startAt.
func (k *Connector) searchJQL(ctx context.Context, jql, fields string, fn func(key, updated string) error) error {
	token := ""
	for {
		var p searchPage
		q := url.Values{"jql": {jql}, "maxResults": {strconv.Itoa(searchLimit)}, "fields": {fields}}
		if token != "" {
			q.Set("nextPageToken", token)
		}
		if err := k.c.GetJSON(ctx, "/rest/api/3/search/jql", q, &p); err != nil {
			return err
		}
		for _, is := range p.Issues {
			if err := fn(is.Key, is.Fields.Updated); err != nil {
				return err
			}
		}
		if p.NextPageToken == "" || p.NextPageToken == token || len(p.Issues) == 0 {
			return nil
		}
		token = p.NextPageToken
	}
}

// Changed lists the issues of a project updated at or after since. JQL
// dates use the time zone of the token user, so the query starts Overlap
// earlier.
func (k *Connector) Changed(ctx context.Context, part connectors.Part, since time.Time, fn func(connectors.Ref) error) error {
	jql := k.jql([]string{part.Key})
	if !since.IsZero() {
		cond := fmt.Sprintf(`updated >= "%s"`, since.Add(-connectors.Overlap).UTC().Format("2006/01/02 15:04"))
		if jql == "" {
			jql = cond
		} else {
			jql += " AND " + cond
		}
	}
	jql += " ORDER BY updated ASC"
	return k.search(ctx, jql, "updated", func(key, updated string) error {
		return fn(connectors.Ref{ID: key, Updated: parseTime(updated)})
	})
}

// ListIDs lists all issue keys of a project in scope.
func (k *Connector) ListIDs(ctx context.Context, part connectors.Part, fn func(string) error) error {
	return k.search(ctx, k.jql([]string{part.Key}), "key", func(key, _ string) error { return fn(key) })
}

// user is a Jira user: a name on Data Center, an account ID on Cloud.
// Cloud gives the email only when the privacy settings of the user allow
// it.
type user struct {
	Name        string `json:"name"`
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	Email       string `json:"emailAddress"`
}

func (u *user) display() string {
	if u == nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Name
}

// person is the person entity of a user, or nil for a user without a name
// or account ID. Data Center people keep the user name as an alias, and
// Cloud people the display name. The email is entity metadata: a switch to
// Cloud matches people by it.
func (k *Connector) person(u *user) *content.EntityRef {
	if u == nil {
		return nil
	}
	id, alias := u.Name, u.Name
	if k.cloud {
		id, alias = u.AccountID, u.DisplayName
	}
	if id == "" {
		return nil
	}
	p := &content.EntityRef{Type: "person", Key: PersonKey(k.c.Host(), id), Name: u.display(), Aliases: []string{alias}}
	if e := strings.TrimSpace(u.Email); e != "" {
		p.Metadata = map[string]any{"email": strings.ToLower(e)}
	}
	return p
}

// PersonKey is the key of a Jira person: the user name on Data Center, the
// account ID on Cloud.
func PersonKey(host, id string) string { return "jira:" + host + ":user:" + id }

type named struct {
	Name string `json:"name"`
}

type issueRef struct {
	Key    string `json:"key"`
	Fields struct {
		Summary   string `json:"summary"`
		IssueType named  `json:"issuetype"`
	} `json:"fields"`
}

type issueJSON struct {
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
	Names  map[string]string          `json:"names"`
}

type fields struct {
	Summary string `json:"summary"`
	// Description is wiki markup on Data Center and Atlassian Document
	// Format on Cloud.
	Description json.RawMessage `json:"description"`
	IssueType   named           `json:"issuetype"`
	Status      named           `json:"status"`
	Priority    *named          `json:"priority"`
	Assignee    *user           `json:"assignee"`
	Reporter    *user           `json:"reporter"`
	Labels      []string        `json:"labels"`
	Components  []named         `json:"components"`
	FixVersions []named         `json:"fixVersions"`
	DueDate     string          `json:"duedate"`
	Parent      *issueRef       `json:"parent"`
	IssueLinks  []struct {
		Type struct {
			Name    string `json:"name"`
			Inward  string `json:"inward"`
			Outward string `json:"outward"`
		} `json:"type"`
		OutwardIssue *issueRef `json:"outwardIssue"`
		InwardIssue  *issueRef `json:"inwardIssue"`
	} `json:"issuelinks"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

var fieldList = "summary,description,issuetype,status,priority,assignee,reporter,labels,components,fixVersions,duedate,parent,issuelinks,created,updated"

// Fetch reads one issue and its newest 50 comments.
func (k *Connector) Fetch(ctx context.Context, key string) (*content.Item, error) {
	var raw issueJSON
	q := url.Values{"fields": {fieldList + ",*navigable"}, "expand": {"names"}}
	if err := k.c.GetJSON(ctx, k.api()+"/issue/"+url.PathEscape(key), q, &raw); err != nil {
		return nil, err
	}
	var cs struct {
		Comments []struct {
			Author  *user           `json:"author"`
			Created string          `json:"created"`
			Body    json.RawMessage `json:"body"`
		} `json:"comments"`
	}
	cq := url.Values{"orderBy": {"-created"}, "maxResults": {"50"}, "startAt": {"0"}}
	if err := k.c.GetJSON(ctx, k.api()+"/issue/"+url.PathEscape(key)+"/comment", cq, &cs); err != nil {
		return nil, err
	}
	var f fields
	b, _ := json.Marshal(raw.Fields)
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("jira: issue %s: %w", key, err)
	}
	epic := ""
	for id, name := range raw.Names {
		if name == "Epic Link" {
			_ = json.Unmarshal(raw.Fields[id], &epic)
		}
	}
	size := len(f.Description)
	for _, c := range cs.Comments {
		size += len(c.Body)
	}
	if k.limits.MaxItemBytes > 0 && size > k.limits.MaxItemBytes {
		return nil, &connectors.SkipError{Reason: "too_large"}
	}
	description, err := k.markdown(f.Description)
	if err != nil {
		return nil, fmt.Errorf("jira: issue %s: %w", key, err)
	}

	b2 := &body{}
	b2.field("Type", f.IssueType.Name)
	b2.field("Status", f.Status.Name)
	if f.Priority != nil {
		b2.field("Priority", f.Priority.Name)
	}
	b2.field("Assignee", f.Assignee.display())
	b2.field("Reporter", f.Reporter.display())
	b2.field("Labels", strings.Join(f.Labels, ", "))
	b2.field("Components", joinNames(f.Components))
	b2.field("Fix versions", joinNames(f.FixVersions))
	b2.field("Due date", f.DueDate)
	if f.Parent != nil {
		b2.field("Parent", strings.TrimSpace(f.Parent.Key+" "+f.Parent.Fields.Summary))
	}
	b2.field("Epic", epic)
	for _, l := range f.IssueLinks {
		switch {
		case l.OutwardIssue != nil:
			b2.field(capital(l.Type.Outward), strings.TrimSpace(l.OutwardIssue.Key+" "+l.OutwardIssue.Fields.Summary))
		case l.InwardIssue != nil:
			b2.field(capital(l.Type.Inward), strings.TrimSpace(l.InwardIssue.Key+" "+l.InwardIssue.Fields.Summary))
		}
	}
	if description != "" {
		b2.parts = append(b2.parts, "## Description", description)
	}
	if len(cs.Comments) > 0 {
		b2.parts = append(b2.parts, "## Comments")
		for i := len(cs.Comments) - 1; i >= 0; i-- {
			c := cs.Comments[i]
			text, err := k.markdown(c.Body)
			if err != nil {
				return nil, fmt.Errorf("jira: a comment of issue %s: %w", key, err)
			}
			b2.parts = append(b2.parts, fmt.Sprintf("### %s, %s", escape(c.Author.display()), parseTime(c.Created).UTC().Format("2006-01-02 15:04")),
				text)
		}
	}

	host := k.c.Host()
	subject := content.EntityRef{Type: k.typeOf(f.IssueType.Name), Key: issueKey(host, raw.Key), Name: f.Summary, Aliases: []string{raw.Key}}
	it := &content.Item{
		ExternalID: raw.Key, Kind: "issue", Title: raw.Key + " " + f.Summary,
		URL:    k.c.Link("/browse/"+raw.Key, nil),
		Author: f.Reporter.display(), Created: parseTime(f.Created), Updated: parseTime(f.Updated),
		Body:   strings.Join(b2.parts, "\n\n"),
		Entity: &subject,
		Metadata: map[string]any{"key": raw.Key, "type": f.IssueType.Name, "status": f.Status.Name,
			"assignee": f.Assignee.display(), "reporter": f.Reporter.display(), "labels": orEmpty(f.Labels),
			"components": orEmpty(names(f.Components)), "fixVersions": orEmpty(names(f.FixVersions)), "due": f.DueDate, "epic": epic},
	}
	it.Entities = connectors.Acronyms(it.Body, "jira", host)
	if f.Priority != nil {
		it.Metadata["priority"] = f.Priority.Name
	}
	if f.Parent != nil {
		it.Metadata["parent"] = f.Parent.Key
	}

	fact := func(pred string, obj *content.EntityRef, text, quote string) {
		it.Facts = append(it.Facts, content.Fact{Subject: subject, Predicate: pred, Object: obj, Text: text, Quote: quote})
	}
	if f.Status.Name != "" {
		fact("status", nil, f.Status.Name, "Status: "+f.Status.Name)
	}
	if p := k.person(f.Assignee); p != nil {
		fact("assigned_to", p, "", "Assignee: "+f.Assignee.display())
	}
	if f.DueDate != "" {
		fact("target_date", nil, f.DueDate, "Due date: "+f.DueDate)
	}
	for _, l := range f.IssueLinks {
		if l.OutwardIssue != nil && strings.EqualFold(l.Type.Name, k.rules.BlockLinkType) {
			o := l.OutwardIssue
			fact("blocks", &content.EntityRef{Type: k.typeOf(o.Fields.IssueType.Name), Key: issueKey(host, o.Key), Name: o.Fields.Summary,
				Aliases: []string{o.Key}}, "", capital(l.Type.Outward)+": "+o.Key)
		}
	}
	switch {
	case epic != "" && slices.Contains(k.rules.InitiativeTypes, "Epic"):
		fact("part_of", &content.EntityRef{Type: "initiative", Key: issueKey(host, epic), Aliases: []string{epic}}, "", "Epic: "+epic)
	case f.Parent != nil && slices.Contains(k.rules.InitiativeTypes, f.Parent.Fields.IssueType.Name):
		fact("part_of", &content.EntityRef{Type: "initiative", Key: issueKey(host, f.Parent.Key), Name: f.Parent.Fields.Summary,
			Aliases: []string{f.Parent.Key}}, "", "Parent: "+f.Parent.Key)
	}
	return it, nil
}

// markdown converts a description or a comment body: wiki markup on Data
// Center, Atlassian Document Format on Cloud. Null is empty.
func (k *Connector) markdown(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if k.cloud {
		return ADFToMarkdown(raw)
	}
	var wiki string
	if err := json.Unmarshal(raw, &wiki); err != nil {
		return "", err
	}
	if strings.TrimSpace(wiki) == "" {
		return "", nil
	}
	return ToMarkdown(strings.TrimSpace(wiki)), nil
}

func (k *Connector) typeOf(issueType string) string {
	if slices.Contains(k.rules.InitiativeTypes, issueType) {
		return "initiative"
	}
	return "issue"
}

func issueKey(host, key string) string { return "jira:" + host + ":" + key }

// body collects the field paragraphs and sections of an issue.
type body struct{ parts []string }

// field adds one field as its own paragraph, so that each field is one
// passage and keeps its hash when another field changes.
func (b *body) field(label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	b.parts = append(b.parts, "**"+label+":** "+escape(value))
}

var special = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`, "#", `\#`)

func escape(s string) string { return special.Replace(s) }

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func names(ns []named) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.Name)
	}
	return out
}

func joinNames(ns []named) string { return strings.Join(names(ns), ", ") }

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
