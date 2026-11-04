package contracts

import (
	"bytes"
	"strings"

	"github.com/bufbuild/protocompile/ast"
	"github.com/bufbuild/protocompile/parser"
	"github.com/bufbuild/protocompile/reporter"
	"google.golang.org/protobuf/types/descriptorpb"
)

// proto reads a Protobuf file. Each rpc of a service is an operation
// package.Service/Method of the api of the file, and each message is a data
// model package.Message. Imports are not read: a type of another file is a
// model with its name and no fields.
func (p *builder) proto(b []byte) error {
	file, err := parser.Parse(p.file, bytes.NewReader(b), reporter.NewHandler(nil))
	if err != nil {
		return err
	}
	res, err := parser.ResultFromAST(file, false, reporter.NewHandler(nil))
	if err != nil {
		return err
	}
	fd := res.FileDescriptorProto()
	pkg := fd.GetPackage()
	qualify := func(name string) string {
		name = strings.TrimPrefix(name, ".")
		if pkg != "" && !strings.Contains(name, ".") {
			return pkg + "." + name
		}
		return name
	}
	at := func(n ast.Node) int {
		if n == nil {
			return 0
		}
		return file.NodeInfo(n).Start().Line
	}
	title := pkg
	if title == "" {
		title = p.file
	}
	api := API(p.repo, p.file, title)
	for i, svc := range fd.GetService() {
		if i == 0 {
			p.fact(p.repo, "provides_api", &api, "", at(res.ServiceNode(svc)))
		}
		service := qualify(svc.GetName())
		for _, m := range svc.GetMethod() {
			op := RPCOperation(p.repo, service, m.GetName())
			if !p.operation(op.Key) {
				return nil
			}
			line := at(res.MethodNode(m))
			p.fact(api, "has_operation", &op, "", line)
			in, out := Model(p.repo, qualify(m.GetInputType())), Model(p.repo, qualify(m.GetOutputType()))
			p.fact(op, "request", &in, "", line)
			p.fact(op, "response", &out, "", line)
		}
	}
	var walk func(prefix string, msgs []*descriptorpb.DescriptorProto) bool
	walk = func(prefix string, msgs []*descriptorpb.DescriptorProto) bool {
		for _, msg := range msgs {
			name := prefix + "." + msg.GetName()
			if prefix == "" {
				name = qualify(msg.GetName())
			}
			model := Model(p.repo, name)
			fresh, ok := p.model(model.Key)
			if !ok {
				return false
			}
			if fresh {
				for i, f := range msg.GetField() {
					p.field(model, i, f.GetName(), protoType(f, qualify), f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REQUIRED,
						at(res.FieldNode(f)))
				}
			}
			if !walk(name, msg.GetNestedType()) {
				return false
			}
		}
		return true
	}
	walk("", fd.GetMessageType())
	return nil
}

// protoType is the type of a field as text.
func protoType(f *descriptorpb.FieldDescriptorProto, qualify func(string) string) string {
	t := ""
	switch {
	case f.GetTypeName() != "":
		t = qualify(f.GetTypeName())
	case f.Type != nil:
		t = strings.ToLower(strings.TrimPrefix(f.GetType().String(), "TYPE_"))
	}
	if f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
		t = "repeated " + t
	}
	return t
}
