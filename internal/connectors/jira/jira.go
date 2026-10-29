// Package jira reads Jira Data Center through REST v2 with a personal
// access token. Issue fields become field passages and deterministic
// claims.
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
}

// New returns a connector. scope is the JSON scope of the connection.
func New(c *connectors.Client, scope json.RawMessage, rules connectors.Rules, limits connectors.Limits) (*Connector, error) {
	var s Scope
	if len(scope) > 0 {
		if err := json.Unmarshal(scope, &s); err != nil {
			return nil, fmt.Errorf("jira: the scope is not valid: %w", err)
		}
	}
	return &Connector{c: c, scope: s, rules: rules, limits: limits}, nil
}

const timeLayout = "2006-01-02T15:04:05.000-0700"

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

func (k *Connector) jql() string {
	var parts []string
	if len(k.scope.Projects) > 0 {
		q := make([]string, len(k.scope.Projects))
		for i, p := range k.scope.Projects {
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
	Total  int `json:"total"`
	Issues []struct {
		Key    string `json:"key"`
		Fields struct {
			Updated string `json:"updated"`
		} `json:"fields"`
	} `json:"issues"`
}

// Validate checks the token and counts the issues in scope.
func (k *Connector) Validate(ctx context.Context) (connectors.Validation, error) {
	var u struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}
	if err := k.c.GetJSON(ctx, "/rest/api/2/myself", nil, &u); err != nil {
		return connectors.Validation{}, err
	}
	var p searchPage
	if err := k.c.GetJSON(ctx, "/rest/api/2/search", url.Values{"jql": {k.jql()}, "maxResults": {"0"}}, &p); err != nil {
		return connectors.Validation{}, err
	}
	name := u.DisplayName
	if name == "" {
		name = u.Name
	}
	return connectors.Validation{User: name, Sample: p.Total}, nil
}

// Scopes lists the projects.
func (k *Connector) Scopes(ctx context.Context) ([]connectors.Scope, error) {
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

func (k *Connector) search(ctx context.Context, jql, fields string, fn func(key, updated string) error) error {
	const limit = 100
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

// Changed lists the issues updated at or after since. JQL dates use the
// time zone of the token user, so the query starts Overlap earlier.
func (k *Connector) Changed(ctx context.Context, since time.Time, fn func(connectors.Ref) error) error {
	jql := k.jql()
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

// ListIDs lists all issue keys in scope.
func (k *Connector) ListIDs(ctx context.Context, fn func(string) error) error {
	return k.search(ctx, k.jql(), "key", func(key, _ string) error { return fn(key) })
}

type user struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
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
	Summary     string    `json:"summary"`
	Description string    `json:"description"`
	IssueType   named     `json:"issuetype"`
	Status      named     `json:"status"`
	Priority    *named    `json:"priority"`
	Assignee    *user     `json:"assignee"`
	Reporter    *user     `json:"reporter"`
	Labels      []string  `json:"labels"`
	Components  []named   `json:"components"`
	FixVersions []named   `json:"fixVersions"`
	DueDate     string    `json:"duedate"`
	Parent      *issueRef `json:"parent"`
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
	if err := k.c.GetJSON(ctx, "/rest/api/2/issue/"+url.PathEscape(key), q, &raw); err != nil {
		return nil, err
	}
	var cs struct {
		Comments []struct {
			Author  *user  `json:"author"`
			Created string `json:"created"`
			Body    string `json:"body"`
		} `json:"comments"`
	}
	cq := url.Values{"orderBy": {"-created"}, "maxResults": {"50"}, "startAt": {"0"}}
	if err := k.c.GetJSON(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"/comment", cq, &cs); err != nil {
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
	if d := strings.TrimSpace(f.Description); d != "" {
		b2.parts = append(b2.parts, "## Description", ToMarkdown(d))
	}
	if len(cs.Comments) > 0 {
		b2.parts = append(b2.parts, "## Comments")
		for i := len(cs.Comments) - 1; i >= 0; i-- {
			c := cs.Comments[i]
			b2.parts = append(b2.parts, fmt.Sprintf("### %s, %s", escape(c.Author.display()), parseTime(c.Created).UTC().Format("2006-01-02 15:04")),
				ToMarkdown(c.Body))
		}
	}

	host := k.c.Host()
	subject := content.EntityRef{Type: k.typeOf(f.IssueType.Name), Key: issueKey(host, raw.Key), Name: f.Summary, Aliases: []string{raw.Key}}
	it := &content.Item{
		ExternalID: raw.Key, Kind: "issue", Title: raw.Key + " " + f.Summary,
		URL:    k.c.URL("/browse/"+raw.Key, nil),
		Author: f.Reporter.display(), Created: parseTime(f.Created), Updated: parseTime(f.Updated),
		Body:   strings.Join(b2.parts, "\n\n"),
		Entity: &subject,
		Metadata: map[string]any{"key": raw.Key, "type": f.IssueType.Name, "status": f.Status.Name,
			"assignee": f.Assignee.display(), "reporter": f.Reporter.display(), "labels": orEmpty(f.Labels),
			"components": orEmpty(names(f.Components)), "fixVersions": orEmpty(names(f.FixVersions)), "due": f.DueDate, "epic": epic},
	}
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
	if f.Assignee != nil && f.Assignee.Name != "" {
		fact("assigned_to", &content.EntityRef{Type: "person", Key: "jira:" + host + ":user:" + f.Assignee.Name, Name: f.Assignee.display(),
			Aliases: []string{f.Assignee.Name}}, "", "Assignee: "+f.Assignee.display())
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
