// Package catalog reads Backstage catalog-info.yaml files of indexed
// repositories. Each Component, API, System, Group, Resource and Domain
// document becomes an entity with the key backstage:<kind>:<ns>/<name>, and
// its relations become deterministic facts.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/alternayte/assay/internal/content"
)

// Source is the evidence source type of catalog facts.
const Source = "catalog"

// types maps the catalog kinds to entity types. A Domain has no entity
// type of its own and becomes a system.
var types = map[string]string{
	"component": "service",
	"api":       "api",
	"system":    "system",
	"group":     "team",
	"user":      "person",
	"resource":  "resource",
	"domain":    "system",
}

// documentKinds are the kinds whose documents create entities.
var documentKinds = []string{"component", "api", "system", "group", "resource", "domain"}

// Repository is the repository that holds a catalog file.
type Repository struct {
	Ref content.EntityRef
	// GitHubHost is the web host for github.com/project-slug annotations.
	GitHubHost string
}

type document struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Namespace   string            `yaml:"namespace"`
		Title       string            `yaml:"title"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		Lifecycle    string   `yaml:"lifecycle"`
		Owner        string   `yaml:"owner"`
		System       string   `yaml:"system"`
		DependsOn    []string `yaml:"dependsOn"`
		ProvidesAPIs []string `yaml:"providesApis"`
		ConsumesAPIs []string `yaml:"consumesApis"`
	} `yaml:"spec"`
}

// Facts returns the facts of a catalog file. Quotes are the source lines,
// so each quote is in the passage of the file.
func Facts(file []byte, repo Repository) ([]content.Fact, error) {
	lines := strings.Split(strings.ReplaceAll(string(file), "\r\n", "\n"), "\n")
	line := func(n int) string {
		if n < 1 || n > len(lines) {
			return ""
		}
		return strings.TrimSpace(lines[n-1])
	}
	var out []content.Fact
	dec := yaml.NewDecoder(bytes.NewReader(file))
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("catalog: %w", err)
		}
		var d document
		if err := node.Decode(&d); err != nil {
			return out, fmt.Errorf("catalog: %w", err)
		}
		kind := strings.ToLower(d.Kind)
		if d.Metadata.Name == "" || !slices.Contains(documentKinds, kind) {
			continue
		}
		ns := d.Metadata.Namespace
		if ns == "" {
			ns = "default"
		}
		subject := ref(kind, ns, d.Metadata.Name)
		if d.Metadata.Title != "" {
			subject.Name = d.Metadata.Title
		}
		fact := func(pred string, obj *content.EntityRef, text string, path ...string) {
			q := line(lineOf(&node, path...))
			if q == "" {
				return
			}
			out = append(out, content.Fact{Subject: subject, Predicate: pred, Object: obj, Text: text, Quote: q, Source: Source})
		}
		if d.Spec.Owner != "" {
			fact("owned_by", parse(d.Spec.Owner, "group", ns), "", "spec", "owner")
		}
		if d.Spec.System != "" && kind != "system" && kind != "domain" {
			fact("part_of", parse(d.Spec.System, "system", ns), "", "spec", "system")
		}
		if kind == "component" || kind == "resource" {
			for i, r := range d.Spec.DependsOn {
				fact("depends_on", parse(r, "component", ns), "", "spec", "dependsOn", fmt.Sprint(i))
			}
		}
		if kind == "component" {
			for i, r := range d.Spec.ProvidesAPIs {
				fact("provides_api", parse(r, "api", ns), "", "spec", "providesApis", fmt.Sprint(i))
			}
			for i, r := range d.Spec.ConsumesAPIs {
				fact("consumes_api", parse(r, "api", ns), "", "spec", "consumesApis", fmt.Sprint(i))
			}
			if d.Spec.Lifecycle != "" {
				fact("status", nil, d.Spec.Lifecycle, "spec", "lifecycle")
			}
		}
		if kind == "component" || kind == "api" {
			r := repo.Ref
			path := []string{"metadata", "name"}
			if linked, key := repo.linked(d.Metadata.Annotations); linked != nil {
				r = *linked
				path = []string{"metadata", "annotations", key}
			}
			q := line(lineOf(&node, path...))
			obj := subject
			out = append(out, content.Fact{Subject: r, Predicate: "implements", Object: &obj, Quote: q, Source: Source})
		}
	}
	return out, nil
}

// Entities returns the entities that the documents of a catalog file
// define, so that a document without relations is still an entity.
func Entities(file []byte) []content.EntityRef {
	var out []content.EntityRef
	dec := yaml.NewDecoder(bytes.NewReader(file))
	for {
		var d document
		if err := dec.Decode(&d); err != nil {
			return out
		}
		kind := strings.ToLower(d.Kind)
		if d.Metadata.Name == "" || !slices.Contains(documentKinds, kind) {
			continue
		}
		ns := d.Metadata.Namespace
		if ns == "" {
			ns = "default"
		}
		r := ref(kind, ns, d.Metadata.Name)
		if d.Metadata.Title != "" {
			r.Name = d.Metadata.Title
		}
		out = append(out, r)
	}
}

// linked returns the repository that an annotation names, and the
// annotation key.
func (r Repository) linked(ann map[string]string) (*content.EntityRef, string) {
	if slug := strings.Trim(ann["github.com/project-slug"], "/ "); strings.Count(slug, "/") == 1 {
		host := r.GitHubHost
		if host == "" {
			host = "github.com"
		}
		return repoRef("github", host, slug), "github.com/project-slug"
	}
	for _, key := range []string{"backstage.io/source-location", "backstage.io/managed-by-location"} {
		v := ann[key]
		if v == "" {
			continue
		}
		if ref := RepositoryOf(strings.TrimPrefix(strings.TrimPrefix(v, "url:"), "file:")); ref != nil {
			return ref, key
		}
	}
	return nil, ""
}

// RepositoryOf returns the repository of a browse URL. An Azure DevOps URL
// has the form .../<project>/_git/<repository>. Other URLs are GitHub URLs
// of the form https://<host>/<owner>/<repository>/....
func RepositoryOf(raw string) *content.EntityRef {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	for i, p := range parts {
		if p == "_git" && i > 0 && i+1 < len(parts) {
			return repoRef("azdo", u.Host, parts[i-1]+"/"+parts[i+1])
		}
	}
	if len(parts) < 2 {
		return nil
	}
	return repoRef("github", u.Host, parts[0]+"/"+strings.TrimSuffix(parts[1], ".git"))
}

func repoRef(provider, host, id string) *content.EntityRef {
	name := id[strings.LastIndex(id, "/")+1:]
	return &content.EntityRef{Type: "repository", Key: provider + ":" + strings.ToLower(host) + ":" + strings.ToLower(id), Name: name,
		Aliases: []string{id}}
}

// parse reads an entity reference of the form [kind:][namespace/]name.
func parse(s, defaultKind, defaultNS string) *content.EntityRef {
	s = strings.TrimSpace(s)
	kind := defaultKind
	if k, rest, ok := strings.Cut(s, ":"); ok {
		kind, s = strings.ToLower(k), rest
	}
	ns := defaultNS
	if n, rest, ok := strings.Cut(s, "/"); ok {
		ns, s = n, rest
	}
	if _, ok := types[kind]; !ok {
		kind = defaultKind
	}
	r := ref(kind, ns, s)
	return &r
}

func ref(kind, ns, name string) content.EntityRef {
	ns, name = strings.ToLower(ns), strings.ToLower(name)
	return content.EntityRef{Type: types[kind], Key: "backstage:" + kind + ":" + ns + "/" + name, Name: name, Aliases: []string{name}}
}

// lineOf returns the line of the value at a path of mapping keys and
// sequence indexes, or 0.
func lineOf(n *yaml.Node, path ...string) int {
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, p := range path {
		switch n.Kind {
		case yaml.MappingNode:
			var next *yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == p {
					next = n.Content[i+1]
					break
				}
			}
			if next == nil {
				return 0
			}
			n = next
		case yaml.SequenceNode:
			var i int
			if _, err := fmt.Sscan(p, &i); err != nil || i >= len(n.Content) {
				return 0
			}
			n = n.Content[i]
		default:
			return 0
		}
	}
	return n.Line
}
