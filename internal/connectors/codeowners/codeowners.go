// Package codeowners reads CODEOWNERS files. Only the default rule, the
// last rule with the pattern *, creates owned_by facts for the repository.
// Path rules are ignored.
package codeowners

import (
	"strings"

	"github.com/alternayte/assay/internal/content"
)

// Source is the evidence source type of CODEOWNERS facts.
const Source = "codeowners"

// Facts returns the owned_by facts of the default rule. provider and host
// qualify the owner keys, such as github and github.com.
func Facts(file []byte, repo content.EntityRef, provider, host string) []content.Fact {
	var rule string
	var owners []string
	for _, l := range strings.Split(strings.ReplaceAll(string(file), "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if i := strings.Index(l, " #"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		f := strings.Fields(l)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") || f[0] != "*" {
			continue
		}
		// A later default rule replaces an earlier one, also when it has
		// no owners.
		rule, owners = l, f[1:]
	}
	var out []content.Fact
	for _, o := range owners {
		obj := owner(o, provider, host)
		if obj == nil {
			continue
		}
		out = append(out, content.Fact{Subject: repo, Predicate: "owned_by", Object: obj, Quote: rule, Source: Source})
	}
	return out
}

// owner maps @org/team to a team, @user to a person and an email address
// to a person.
func owner(o, provider, host string) *content.EntityRef {
	prefix := provider + ":" + host + ":"
	switch {
	case strings.HasPrefix(o, "@") && strings.Contains(o, "/"):
		team := strings.ToLower(o[1:])
		return &content.EntityRef{Type: "team", Key: prefix + "team:" + team, Name: team[strings.Index(team, "/")+1:], Aliases: []string{o}}
	case strings.HasPrefix(o, "@") && len(o) > 1:
		user := strings.ToLower(o[1:])
		return &content.EntityRef{Type: "person", Key: prefix + "user:" + user, Name: user, Aliases: []string{o}}
	case strings.Contains(o, "@"):
		email := strings.ToLower(o)
		return &content.EntityRef{Type: "person", Key: prefix + "email:" + email, Name: email, Aliases: []string{email}}
	}
	return nil
}
