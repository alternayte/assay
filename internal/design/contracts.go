package design

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/connectors/contracts"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/pages"
	"github.com/alternayte/assay/internal/visibility"
)

// Contracts section of a repository page. The page computes it from the
// claims for each reader. It has no version and is never pinned.
const (
	KindContracts  = "contracts"
	contractsSlug  = "contracts"
	contractsTitle = "Contracts"
	maxContracts   = 200
)

// likeEscape escapes the wildcards of a LIKE prefix.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(s)
}

// protocolOf is the protocol of an operation key of a repository.
func protocolOf(repoKey, opKey string) string {
	rest := strings.TrimPrefix(opKey, "operation:"+repoKey+":")
	switch {
	case strings.HasPrefix(rest, "graphql:"):
		return "graphql"
	case strings.Contains(rest, " "):
		return "http"
	}
	return "grpc"
}

// specAPIs returns the spec api of the repository for each protocol: the
// first api in key order with an operation of that protocol.
func (s *Service) specAPIs(ctx context.Context, in *repoInput) (map[string]content.EntityRef, error) {
	rows, err := New(s.Pool).SpecOperations(ctx, SpecOperationsParams{Visible: []int64{in.connectionID},
		Prefix: likeEscape("api:"+in.key+":") + "%", Inferred: likeEscape("api:"+in.key+":inferred:") + "%"})
	if err != nil {
		return nil, err
	}
	out := map[string]content.EntityRef{}
	for _, r := range rows {
		p := protocolOf(in.key, r.OperationKey)
		if _, ok := out[p]; !ok {
			out[p] = content.EntityRef{Type: "api", Key: r.ApiKey, Name: r.ApiName}
		}
	}
	return out, nil
}

var protocolNames = map[string]string{"http": "HTTP", "grpc": "gRPC", "graphql": "GraphQL"}

// marked is a passage that a digest marker names.
type marked struct {
	item int64
	row  ItemPassagesRow
}

// inferred turns the contracts of a digest answer into facts. A contract
// is kept only when its quote is in the passage that it names, as in
// extraction.
func inferred(in *repoInput, ans digestAnswer, byID map[string]marked) []content.Inferred {
	repo := content.EntityRef{Type: "repository", Key: in.key, Name: in.name, Aliases: []string{in.repo}}
	var out []content.Inferred
	add := func(id, quote string, f content.Fact) bool {
		m, ok := byID[strings.ToUpper(strings.Trim(strings.TrimSpace(id), "[]"))]
		q := content.Normalize(quote)
		if !ok || q == "" || !strings.Contains(m.row.Norm, q) {
			return false
		}
		f.Quote, f.Source = quote, content.InferredSource
		out = append(out, content.Inferred{Fact: f, ItemID: m.item, PassageHash: m.row.Hash})
		return true
	}
	for _, o := range ans.Operations {
		method, target := strings.TrimSpace(o.Method), strings.TrimSpace(o.Target)
		if method == "" || target == "" {
			continue
		}
		var op content.EntityRef
		switch o.Protocol {
		case "http":
			op = contracts.HTTPOperation(repo, method, target)
		case "grpc":
			op = contracts.RPCOperation(repo, target, method)
		case "graphql":
			op = contracts.GraphQLOperation(repo, method, target)
		default:
			continue
		}
		api, ok := in.specAPIs[o.Protocol]
		if !ok {
			api = content.EntityRef{Type: "api", Key: "api:" + in.key + ":inferred:" + o.Protocol, Name: in.name + " " + protocolNames[o.Protocol] + " API"}
		}
		if !add(o.Passage, o.Quote, content.Fact{Subject: api, Predicate: "has_operation", Object: &op}) {
			continue
		}
		if !ok {
			add(o.Passage, o.Quote, content.Fact{Subject: repo, Predicate: "provides_api", Object: &api})
		}
	}
	for _, e := range ans.Events {
		channel, msg := strings.TrimSpace(e.Channel), strings.TrimSpace(e.Message)
		if channel == "" || msg == "" {
			continue
		}
		ev := contracts.Event(repo, channel, msg)
		predicate := "publishes"
		if e.Direction == "subscribes" {
			predicate = "subscribes_to"
		}
		if add(e.Passage, e.Quote, content.Fact{Subject: repo, Predicate: predicate, Object: &ev}) {
			add(e.Passage, e.Quote, content.Fact{Subject: ev, Predicate: "channel", Text: channel})
		}
	}
	for _, m := range ans.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		model := contracts.Model(repo, name)
		for i, f := range m.Fields {
			if i == contracts.MaxFields || strings.TrimSpace(f.Name) == "" {
				break
			}
			text := strings.TrimSpace(f.Name)
			if t := strings.TrimSpace(f.Type); t != "" {
				text += ": " + t
			}
			if !add(m.Passage, m.Quote, content.Fact{Subject: model, Predicate: "has_field", Text: text}) {
				break
			}
		}
	}
	return out
}

// contractGroups are the predicates of the Contracts section, in order.
var contractGroups = []struct{ predicate, heading string }{
	{"provides_api", "APIs"},
	{"has_operation", "Operations"},
	{"publishes", "Events it publishes"},
	{"subscribes_to", "Events it receives"},
	{"request", "Requests"},
	{"response", "Responses"},
	{"payload", "Payloads"},
	{"has_field", "Data models"},
}

// contractsSection computes the Contracts section of a repository for a
// reader: the APIs, operations, events and data models of the repository
// from the claims, each with the cite of its claim. It returns nil when the
// reader sees no contract.
func (s *Service) contractsSection(ctx context.Context, vis visibility.Set, repoID int64, repoKey string) (*DesignPageSection, error) {
	var prefixes []string
	for _, t := range []string{"api", "operation", "event", "data_model"} {
		prefixes = append(prefixes, likeEscape(t+":"+repoKey+":")+"%")
	}
	ids, err := New(s.Pool).EntitiesByPrefix(ctx, EntitiesByPrefixParams{Prefixes: prefixes, Lim: 4 * maxContracts})
	if err != nil {
		return nil, err
	}
	all, err := claims.Load(ctx, s.Pool, vis, claims.Query{Subjects: append(ids, repoID)})
	if err != nil {
		return nil, err
	}
	by := map[string][]claims.Claim{}
	for _, c := range all {
		if current(c) {
			by[c.Predicate] = append(by[c.Predicate], c)
		}
	}
	cs := map[string]Cite{}
	var b strings.Builder
	n := 0
	for _, g := range contractGroups {
		list := by[g.predicate]
		if len(list) == 0 || n == maxContracts {
			continue
		}
		slices.SortFunc(list, func(a, b claims.Claim) int {
			return strings.Compare(a.SubjectName+"\x00"+a.Object(), b.SubjectName+"\x00"+b.Object())
		})
		fmt.Fprintf(&b, "%s:\n\n", g.heading)
		for _, c := range list {
			if n == maxContracts {
				break
			}
			n++
			id := fmt.Sprintf("C%d", n)
			text := c.SubjectName + " " + strings.ReplaceAll(c.Predicate, "_", " ") + " " + c.Object()
			cs[id] = Cite{Kind: "claim", ClaimID: c.ID, Text: text}
			fmt.Fprintf(&b, "- %s: %s [%s]\n", c.SubjectName, c.Object(), id)
		}
		b.WriteString("\n")
	}
	if n == 0 {
		return nil, nil
	}
	ps := &DesignPageSection{Slug: contractsSlug, Kind: KindContracts, Title: contractsTitle, Lint: []pages.Failure{}, Cites: []DesignCite{}}
	s.fillComputed(ps, strings.TrimSpace(b.String()), "", nil, cs)
	return ps, nil
}

// current reports a claim that the reader sees as supported or in
// conflict, with at least one current evidence row.
func current(c claims.Claim) bool {
	if c.Status != claims.Supported && c.Status != claims.Conflicting {
		return false
	}
	return slices.ContainsFunc(c.Evidence, func(e claims.Evidence) bool { return e.State == "current" })
}
