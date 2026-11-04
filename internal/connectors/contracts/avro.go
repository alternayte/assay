package contracts

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// avro reads an Avro schema file: a record, or a list of records. Each
// record is a data model named namespace.name.
func (p *builder) avro(b []byte) error {
	r, err := root(b)
	if err != nil || r == nil {
		return err
	}
	if r.Kind == yaml.SequenceNode {
		for _, c := range r.Content {
			p.record(c, "")
		}
		return nil
	}
	p.record(r, "")
	return nil
}

// record adds the model of an Avro record and of the records nested in it.
// It returns the qualified name, or "" for another type.
func (p *builder) record(n *yaml.Node, namespace string) string {
	if n == nil || n.Kind != yaml.MappingNode || str(get(n, "type")) != "record" {
		return ""
	}
	if ns := str(get(n, "namespace")); ns != "" {
		namespace = ns
	}
	name := str(get(n, "name"))
	if name == "" {
		return ""
	}
	if namespace != "" && !strings.Contains(name, ".") {
		name = namespace + "." + name
	}
	m := Model(p.repo, name)
	fresh, ok := p.model(m.Key)
	if !fresh || !ok {
		return name
	}
	fields := get(n, "fields")
	if fields == nil || fields.Kind != yaml.SequenceNode {
		return name
	}
	for i, f := range fields.Content {
		p.field(m, i, str(get(f, "name")), p.avroType(get(f, "type"), namespace), false, keyLine(f, "name"))
	}
	return name
}

// avroType is the type of an Avro field as text.
func (p *builder) avroType(n *yaml.Node, namespace string) string {
	switch {
	case n == nil:
		return ""
	case n.Kind == yaml.ScalarNode:
		return n.Value
	case n.Kind == yaml.SequenceNode:
		var parts []string
		for _, c := range n.Content {
			parts = append(parts, p.avroType(c, namespace))
		}
		return strings.Join(parts, "|")
	}
	switch str(get(n, "type")) {
	case "record":
		return p.record(n, namespace)
	case "array":
		return p.avroType(get(n, "items"), namespace) + "[]"
	case "map":
		return "map<" + p.avroType(get(n, "values"), namespace) + ">"
	case "enum", "fixed":
		return str(get(n, "name"))
	}
	if lt := str(get(n, "logicalType")); lt != "" {
		return lt
	}
	return str(get(n, "type"))
}
