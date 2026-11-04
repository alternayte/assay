// Package contracts parses the spec files of indexed repositories: OpenAPI
// 3, Swagger 2, AsyncAPI 2 and 3, JSON Schema, Avro, Protobuf and GraphQL
// SDL. Each api, operation, event and data model becomes an entity whose key
// is scoped to the repository, so that a spec file and inference from code
// of the same repository meet on one entity. Each fact quotes the source
// line of its node.
package contracts

import (
	"bytes"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/alternayte/assay/internal/content"
)

// Source is the evidence source type of facts from spec files.
const Source = "repository"

// Limits of one parsed file.
const (
	MaxOperations = 2000
	MaxEvents     = 2000
	MaxModels     = 2000
	MaxFields     = 500
	maxQuote      = 300
)

// SkipLimit is the skip reason of a file past a limit. The facts up to the
// limit stay.
const SkipLimit = "contract_limit"

// Formats of Detect.
const (
	OpenAPI    = "openapi"
	Swagger    = "swagger"
	AsyncAPI   = "asyncapi"
	JSONSchema = "jsonschema"
	Avro       = "avro"
	Proto      = "proto"
	GraphQL    = "graphql"
)

// Detect returns the format of a spec file, or "" for another file. A YAML
// or JSON file is a spec file by its top-level key, whatever its name.
func Detect(file string, b []byte) string {
	lower := strings.ToLower(path.Base(file))
	switch ext := path.Ext(lower); {
	case ext == ".proto":
		return Proto
	case ext == ".graphql" || ext == ".gql":
		return GraphQL
	case ext == ".avsc":
		return Avro
	case strings.HasSuffix(lower, ".schema.json"):
		return JSONSchema
	case ext == ".yaml" || ext == ".yml" || ext == ".json":
		return topKey(b)
	}
	return ""
}

var topKeys = regexp.MustCompile(`(?m)^\s*"?(openapi|swagger|asyncapi)"?\s*:`)

// topKey reads the first top-level key that names a spec format.
func topKey(b []byte) string {
	m := topKeys.FindSubmatch(b[:min(len(b), 4096)])
	if m == nil {
		return ""
	}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return ""
	}
	for _, k := range []string{OpenAPI, Swagger, AsyncAPI} {
		if get(root.Content[0], k) != nil {
			return k
		}
	}
	return ""
}

// Result is what a spec file states.
type Result struct {
	Facts []content.Fact
	// Skip is SkipLimit when the file is past a limit.
	Skip string
}

// Parse returns the facts of a spec file of a repository.
func Parse(file string, b []byte, repo content.EntityRef) (Result, error) {
	p := &builder{repo: repo, file: file, lines: strings.Split(string(b), "\n"), models: map[string]bool{}, ops: map[string]bool{},
		events: map[string]bool{}}
	var err error
	switch Detect(file, b) {
	case OpenAPI, Swagger:
		err = p.openAPI(b)
	case AsyncAPI:
		err = p.asyncAPI(b)
	case JSONSchema:
		err = p.jsonSchema(b)
	case Avro:
		err = p.avro(b)
	case Proto:
		err = p.proto(b)
	case GraphQL:
		err = p.graphQL(b)
	}
	return Result{Facts: p.facts, Skip: p.skip}, err
}

// builder collects the facts of one file.
type builder struct {
	repo   content.EntityRef
	file   string
	lines  []string
	facts  []content.Fact
	skip   string
	ops    map[string]bool
	events map[string]bool
	models map[string]bool
}

func (p *builder) quote(line int) string {
	if line < 1 || line > len(p.lines) {
		return ""
	}
	q := strings.TrimSpace(p.lines[line-1])
	if utf8.RuneCountInString(q) > maxQuote {
		q = string([]rune(q)[:maxQuote])
	}
	return q
}

// fact adds a fact whose quote is the source line. A line without text
// gives no fact.
func (p *builder) fact(subject content.EntityRef, predicate string, object *content.EntityRef, text string, line int) {
	q := p.quote(line)
	if q == "" {
		return
	}
	p.facts = append(p.facts, content.Fact{Subject: subject, Predicate: predicate, Object: object, Text: text, Quote: q, Source: Source,
		Line: line})
}

// API returns the api of a spec file: api:<repo key>:<file path>.
func API(repo content.EntityRef, file, title string) content.EntityRef {
	if strings.TrimSpace(title) == "" {
		title = path.Base(file)
	}
	return content.EntityRef{Type: "api", Key: "api:" + repo.Key + ":" + file, Name: strings.TrimSpace(title)}
}

var pathParam = regexp.MustCompile(`\{[^}/]*\}|:[A-Za-z_][A-Za-z0-9_]*|<[^>/]*>`)

// NormalizePath writes each path parameter as {}.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return pathParam.ReplaceAllString(p, "{}")
}

// HTTPOperation is the operation of an HTTP method and path.
func HTTPOperation(repo content.EntityRef, method, route string) content.EntityRef {
	name := strings.ToUpper(strings.TrimSpace(method)) + " " + NormalizePath(route)
	return content.EntityRef{Type: "operation", Key: "operation:" + repo.Key + ":" + name, Name: name}
}

// RPCOperation is the operation of a gRPC method of package.Service.
func RPCOperation(repo content.EntityRef, service, method string) content.EntityRef {
	name := service + "/" + method
	return content.EntityRef{Type: "operation", Key: "operation:" + repo.Key + ":" + name, Name: name}
}

// GraphQLOperation is the operation of a field of a GraphQL root type:
// Query, Mutation or Subscription.
func GraphQLOperation(repo content.EntityRef, root, field string) content.EntityRef {
	name := root + "." + field
	return content.EntityRef{Type: "operation", Key: "operation:" + repo.Key + ":graphql:" + name, Name: name}
}

// Event is the event of a message on a channel.
func Event(repo content.EntityRef, channel, message string) content.EntityRef {
	return content.EntityRef{Type: "event", Key: "event:" + repo.Key + ":" + channel + ":" + message, Name: message, Aliases: []string{channel + " " + message}}
}

// Model is the data model of a qualified name.
func Model(repo content.EntityRef, name string) content.EntityRef {
	r := content.EntityRef{Type: "data_model", Key: "data_model:" + repo.Key + ":" + name, Name: name}
	if i := strings.LastIndexAny(name, "./#"); i >= 0 && i+1 < len(name) {
		r.Aliases = []string{name[i+1:]}
	}
	return r
}

// operation counts an operation against the limit. It reports false past
// the limit.
func (p *builder) operation(key string) bool {
	if p.ops[key] {
		return true
	}
	if len(p.ops) >= MaxOperations {
		p.skip = SkipLimit
		return false
	}
	p.ops[key] = true
	return true
}

func (p *builder) event(key string) bool {
	if p.events[key] {
		return true
	}
	if len(p.events) >= MaxEvents {
		p.skip = SkipLimit
		return false
	}
	p.events[key] = true
	return true
}

// model counts a data model against the limit. It reports whether the
// model is new and inside the limit.
func (p *builder) model(key string) (fresh, ok bool) {
	if p.models[key] {
		return false, true
	}
	if len(p.models) >= MaxModels {
		p.skip = SkipLimit
		return false, false
	}
	p.models[key] = true
	return true, true
}

// field adds a has_field fact of a model: "name: type", with " (required)"
// when the source says so.
func (p *builder) field(m content.EntityRef, n int, name, typ string, required bool, line int) {
	if n >= MaxFields {
		p.skip = SkipLimit
		return
	}
	text := strings.TrimSpace(name)
	if typ = strings.TrimSpace(typ); typ != "" {
		text += ": " + typ
	}
	if required {
		text += " (required)"
	}
	p.fact(m, "has_field", nil, text, line)
}

// get returns the value of a key of a mapping node, or nil.
func get(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// keyLine returns the line of a key of a mapping node, or 0.
func keyLine(n *yaml.Node, key string) int {
	if n == nil || n.Kind != yaml.MappingNode {
		return 0
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i].Line
		}
	}
	return 0
}

// pairs returns the keys and values of a mapping node in order.
func pairs(n *yaml.Node) [][2]*yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	out := make([][2]*yaml.Node, 0, len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, [2]*yaml.Node{n.Content[i], n.Content[i+1]})
	}
	return out
}

func str(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// root parses a YAML or JSON file into its root mapping.
func root(b []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(bytes.NewReader(b)).Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	return doc.Content[0], nil
}
