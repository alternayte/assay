package contracts

import (
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

func line(p *ast.Position) int {
	if p == nil {
		return 0
	}
	return p.Line
}

// graphQL reads a GraphQL SDL file. Each field of a root type is an
// operation of the api of the file. Object, input and interface types are
// data models.
func (p *builder) graphQL(b []byte) error {
	doc, err := parser.ParseSchema(&ast.Source{Name: p.file, Input: string(b)})
	if err != nil {
		return err
	}
	roots := map[string]string{"Query": "Query", "Mutation": "Mutation", "Subscription": "Subscription"}
	for _, list := range []ast.SchemaDefinitionList{doc.Schema, doc.SchemaExtension} {
		for _, s := range list {
			for _, ot := range s.OperationTypes {
				switch ot.Operation {
				case ast.Query:
					roots[ot.Type] = "Query"
				case ast.Mutation:
					roots[ot.Type] = "Mutation"
				case ast.Subscription:
					roots[ot.Type] = "Subscription"
				}
			}
		}
	}
	defs := append(append(ast.DefinitionList{}, doc.Definitions...), doc.Extensions...)
	known := map[string]bool{}
	for _, d := range defs {
		switch d.Kind {
		case ast.Object, ast.InputObject, ast.Interface:
			if _, root := roots[d.Name]; !root {
				known[d.Name] = true
			}
		}
	}
	api := p.apiFor("")
	first := true
	for _, d := range defs {
		root, isRoot := roots[d.Name]
		if d.Kind != ast.Object || !isRoot {
			continue
		}
		for _, f := range d.Fields {
			if first {
				p.provide(api, line(d.Position))
				first = false
			}
			op := GraphQLOperation(p.repo, root, f.Name)
			if !p.operation(op.Key) {
				return nil
			}
			p.fact(api, "has_operation", &op, "", line(f.Position))
			if t := named(f.Type); known[t] {
				m := Model(p.repo, t)
				p.fact(op, "response", &m, "", line(f.Position))
			}
			for _, a := range f.Arguments {
				if t := named(a.Type); known[t] {
					m := Model(p.repo, t)
					p.fact(op, "request", &m, "", line(a.Position))
					break
				}
			}
		}
	}
	counts := map[string]int{}
	for _, d := range defs {
		if !known[d.Name] {
			continue
		}
		m := Model(p.repo, d.Name)
		if _, ok := p.model(m.Key); !ok {
			return nil
		}
		for _, f := range d.Fields {
			p.field(m, counts[m.Key], f.Name, f.Type.String(), f.Type.NonNull, line(f.Position))
			counts[m.Key]++
		}
	}
	return nil
}

// named is the innermost named type of a field type.
func named(t *ast.Type) string {
	for t != nil && t.NamedType == "" {
		t = t.Elem
	}
	if t == nil {
		return ""
	}
	return t.NamedType
}
