package web

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Agent is the product token of Assay in robots.txt and in its User-Agent.
const Agent = "Assay"

// maxCrawlDelay bounds the Crawl-delay that a site can ask for, so that a
// run ends.
const maxCrawlDelay = time.Minute

// robots holds the rules of robots.txt for the Assay group, or for the *
// group when no group names Assay (RFC 9309).
type robots struct {
	rules []rule
	delay time.Duration
}

type rule struct {
	allow   bool
	length  int
	pattern *regexp.Regexp
}

type group struct {
	agents []string
	rules  []rule
	delay  time.Duration
}

// parseRobots reads robots.txt. Lines that it does not know are ignored.
func parseRobots(b []byte) *robots {
	var groups []*group
	var cur *group
	inRules := false
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if cur == nil || inRules {
				cur = &group{}
				groups = append(groups, cur)
				inRules = false
			}
			agent, _, _ := strings.Cut(strings.ToLower(value), "/")
			cur.agents = append(cur.agents, strings.TrimSpace(agent))
		case "allow", "disallow":
			if cur == nil {
				continue
			}
			inRules = true
			if value == "" {
				continue
			}
			if r, ok := compile(value, key == "allow"); ok {
				cur.rules = append(cur.rules, r)
			}
		case "crawl-delay":
			if cur == nil {
				continue
			}
			inRules = true
			if s, err := strconv.ParseFloat(value, 64); err == nil && s > 0 {
				cur.delay = min(time.Duration(s*float64(time.Second)), maxCrawlDelay)
			}
		}
	}
	want := strings.ToLower(Agent)
	if !named(groups, want) {
		want = "*"
	}
	out := &robots{}
	for _, g := range groups {
		if slices.Contains(g.agents, want) {
			out.rules = append(out.rules, g.rules...)
			out.delay = max(out.delay, g.delay)
		}
	}
	return out
}

// named reports whether a group names the agent.
func named(groups []*group, agent string) bool {
	return slices.ContainsFunc(groups, func(g *group) bool { return slices.Contains(g.agents, agent) })
}

// compile turns a path pattern into a regular expression: * matches any
// characters and a final $ ends the path.
func compile(p string, allow bool) (rule, bool) {
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "*") {
		return rule{}, false
	}
	end := strings.HasSuffix(p, "$")
	body := strings.TrimSuffix(p, "$")
	expr := "^" + strings.ReplaceAll(regexp.QuoteMeta(body), `\*`, ".*")
	if end {
		expr += "$"
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return rule{}, false
	}
	return rule{allow: allow, length: len(p), pattern: re}, true
}

// allowed reports whether Assay may read a path. The longest matching rule
// wins, and allow wins a tie. robots.txt itself is always allowed.
func (r *robots) allowed(path string) bool {
	if r == nil || path == "/robots.txt" {
		return true
	}
	if path == "" {
		path = "/"
	}
	best, allow := -1, true
	for _, ru := range r.rules {
		if !ru.pattern.MatchString(path) {
			continue
		}
		if ru.length > best || (ru.length == best && ru.allow) {
			best, allow = ru.length, ru.allow
		}
	}
	return allow
}
