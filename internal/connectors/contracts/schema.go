package contracts

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/alternayte/assay/internal/content"
)

// defs are the named schemas of a document that a local $ref can name,
// in document order.
type defs struct {
	by    map[string]*yaml.Node
	order []string
}

func newDefs() *defs { return &defs{by: map[string]*yaml.Node{}} }

// add adds the named schemas of a mapping node.
func (d *defs) add(n *yaml.Node) {
	for _, kv := range pairs(n) {
		if _, ok := d.by[kv[0].Value]; !ok {
			d.order = append(d.order, kv[0].Value)
		}
		d.by[kv[0].Value] = kv[1]
	}
}

// all defines every named schema.
func (p *builder) all(d *defs) {
	for _, name := range d.order {
		p.defineSchema(name, d.by[name], d)
	}
}

// refName returns the model name of a $ref and whether the ref is local.
// A local ref names its last path segment. Another ref keeps its text: it
// is not fetched.
func refName(ref string) (string, bool) {
	if strings.HasPrefix(ref, "#/") {
		return ref[strings.LastIndex(ref, "/")+1:], true
	}
	return ref, false
}

// typeName is the type of a schema as field text.
func typeName(n *yaml.Node) string {
	if r := str(get(n, "$ref")); r != "" {
		name, _ := refName(r)
		return name
	}
	t := get(n, "type")
	switch {
	case t != nil && t.Kind == yaml.SequenceNode:
		var parts []string
		for _, c := range t.Content {
			parts = append(parts, c.Value)
		}
		return strings.Join(parts, "|")
	case str(t) == "array":
		return typeName(get(n, "items")) + "[]"
	case str(t) != "":
		return str(t)
	case get(n, "properties") != nil:
		return "object"
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if alt := get(n, k); alt != nil && alt.Kind == yaml.SequenceNode {
			var parts []string
			for _, c := range alt.Content {
				parts = append(parts, typeName(c))
			}
			return strings.Join(parts, "|")
		}
	}
	return ""
}

// schemaModel returns the data model of a schema: the model that a $ref
// names, the model of the items of an array, or an inline object model
// with the name inline. A schema of another type has no model.
func (p *builder) schemaModel(n *yaml.Node, inline string, d *defs) *content.EntityRef {
	if n == nil {
		return nil
	}
	if r := str(get(n, "$ref")); r != "" {
		name, local := refName(r)
		m := Model(p.repo, name)
		if local {
			if def, ok := d.by[name]; ok {
				p.defineSchema(name, def, d)
			}
		}
		return &m
	}
	if str(get(n, "type")) == "array" {
		return p.schemaModel(get(n, "items"), inline, d)
	}
	if get(n, "properties") == nil && get(n, "allOf") == nil {
		return nil
	}
	p.defineSchema(inline, n, d)
	m := Model(p.repo, inline)
	return &m
}

// defineSchema adds the fields of a named schema once.
func (p *builder) defineSchema(name string, n *yaml.Node, d *defs) {
	m := Model(p.repo, name)
	fresh, ok := p.model(m.Key)
	if !fresh || !ok {
		return
	}
	count := 0
	var walk func(n *yaml.Node, depth int)
	walk = func(n *yaml.Node, depth int) {
		if n == nil || depth > 4 {
			return
		}
		var required []string
		if rq := get(n, "required"); rq != nil && rq.Kind == yaml.SequenceNode {
			for _, c := range rq.Content {
				required = append(required, c.Value)
			}
		}
		for _, kv := range pairs(get(n, "properties")) {
			p.field(m, count, kv[0].Value, typeName(kv[1]), slices.Contains(required, kv[0].Value), kv[0].Line)
			count++
		}
		if all := get(n, "allOf"); all != nil && all.Kind == yaml.SequenceNode {
			for _, c := range all.Content {
				if r := str(get(c, "$ref")); r != "" {
					if base, local := refName(r); local {
						walk(d.by[base], depth+1)
					}
					continue
				}
				walk(c, depth+1)
			}
		}
	}
	walk(n, 0)
}

// openAPI reads an OpenAPI 3 or Swagger 2 document.
func (p *builder) openAPI(b []byte) error {
	r, err := root(b)
	if err != nil || r == nil {
		return err
	}
	info := get(r, "info")
	api := p.apiFor(str(get(info, "title")))
	line := keyLine(info, "title")
	if line == 0 {
		line = max(keyLine(r, OpenAPI), keyLine(r, Swagger))
	}
	p.provide(api, line)
	d := newDefs()
	d.add(get(get(r, "components"), "schemas"))
	d.add(get(r, "definitions"))
	methods := []string{"get", "put", "post", "delete", "patch", "head", "options", "trace"}
	for _, pv := range pairs(get(r, "paths")) {
		for _, mv := range pairs(pv[1]) {
			if !slices.Contains(methods, strings.ToLower(mv[0].Value)) {
				continue
			}
			op := HTTPOperation(p.repo, mv[0].Value, pv[0].Value)
			if !p.operation(op.Key) {
				return nil
			}
			p.fact(api, "has_operation", &op, "", mv[0].Line)
			body := mv[1]
			if rb := get(body, "requestBody"); rb != nil {
				if m := p.schemaModel(mediaSchema(rb), op.Name+" request", d); m != nil {
					p.fact(op, "request", m, "", keyLine(body, "requestBody"))
				}
			}
			if params := get(body, "parameters"); params != nil && params.Kind == yaml.SequenceNode {
				for _, prm := range params.Content {
					if str(get(prm, "in")) != "body" {
						continue
					}
					if m := p.schemaModel(get(prm, "schema"), op.Name+" request", d); m != nil {
						p.fact(op, "request", m, "", prm.Line)
					}
				}
			}
			for _, rv := range pairs(get(body, "responses")) {
				s := mediaSchema(rv[1])
				if s == nil {
					s = get(rv[1], "schema")
				}
				if m := p.schemaModel(s, fmt.Sprintf("%s response %s", op.Name, rv[0].Value), d); m != nil {
					p.fact(op, "response", m, "", rv[0].Line)
				}
			}
		}
	}
	p.all(d)
	return nil
}

// mediaSchema returns the schema of the first media type of an OpenAPI 3
// request body or response.
func mediaSchema(n *yaml.Node) *yaml.Node {
	for _, kv := range pairs(get(n, "content")) {
		if s := get(kv[1], "schema"); s != nil {
			return s
		}
	}
	return nil
}

// jsonSchema reads a JSON Schema file. The root is a model named by its
// title or the file name, and each definition is a model.
func (p *builder) jsonSchema(b []byte) error {
	r, err := root(b)
	if err != nil || r == nil {
		return err
	}
	d := newDefs()
	d.add(get(r, "definitions"))
	d.add(get(r, "$defs"))
	name := str(get(r, "title"))
	if name == "" {
		base := p.file[strings.LastIndex(p.file, "/")+1:]
		name = strings.TrimSuffix(strings.TrimSuffix(base, ".json"), ".schema")
	}
	if get(r, "properties") != nil || get(r, "allOf") != nil {
		p.defineSchema(name, r, d)
	}
	p.all(d)
	return nil
}
