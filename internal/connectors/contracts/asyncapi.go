package contracts

import (
	"strings"

	"go.yaml.in/yaml/v3"
)

// pointer follows a local JSON pointer such as #/components/messages/X. It
// returns nil for another ref.
func pointer(r *yaml.Node, ref string) *yaml.Node {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	n := r
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if n = get(n, part); n == nil {
			return nil
		}
	}
	return n
}

// deref returns the node that a $ref names, up to a few hops, or the node.
func deref(r, n *yaml.Node) *yaml.Node {
	for range 4 {
		ref := str(get(n, "$ref"))
		if ref == "" {
			return n
		}
		next := pointer(r, ref)
		if next == nil {
			return n
		}
		n = next
	}
	return n
}

// message is one message of an AsyncAPI operation.
type message struct {
	name string
	node *yaml.Node
	line int
}

// asyncAPI reads an AsyncAPI 2 or 3 document. Messages that the
// application sends give publishes: v2 subscribe and v3 send. Messages
// that it receives give subscribes_to: v2 publish and v3 receive.
func (p *builder) asyncAPI(b []byte) error {
	r, err := root(b)
	if err != nil || r == nil {
		return err
	}
	info := get(r, "info")
	api := API(p.repo, p.file, str(get(info, "title")))
	line := keyLine(info, "title")
	if line == 0 {
		line = keyLine(r, AsyncAPI)
	}
	p.fact(p.repo, "provides_api", &api, "", line)
	d := newDefs()
	d.add(get(get(r, "components"), "schemas"))
	if strings.HasPrefix(str(get(r, AsyncAPI)), "2") {
		p.asyncV2(r, d)
	} else {
		p.asyncV3(r, d)
	}
	p.all(d)
	return nil
}

func (p *builder) asyncV2(r *yaml.Node, d *defs) {
	for _, cv := range pairs(get(r, "channels")) {
		channel, ch := cv[0].Value, cv[1]
		for _, op := range []struct{ key, predicate string }{{"subscribe", "publishes"}, {"publish", "subscribes_to"}} {
			o := get(ch, op.key)
			if o == nil {
				continue
			}
			for _, m := range v2Messages(r, get(o, "message"), str(get(o, "operationId")), channel) {
				if !p.message(r, d, channel, cv[0].Line, op.predicate, keyLine(ch, op.key), m) {
					return
				}
			}
		}
	}
}

// v2Messages returns the messages of a v2 operation: one message, or the
// list of its oneOf.
func v2Messages(r, n *yaml.Node, operationID, channel string) []message {
	if n == nil {
		return nil
	}
	if alt := get(n, "oneOf"); alt != nil && alt.Kind == yaml.SequenceNode {
		var out []message
		for _, c := range alt.Content {
			out = append(out, v2Messages(r, c, operationID, channel)...)
		}
		return out
	}
	name := ""
	if ref := str(get(n, "$ref")); ref != "" {
		name, _ = refName(ref)
	}
	body := deref(r, n)
	for _, k := range []string{"name", "messageId"} {
		if v := str(get(body, k)); v != "" {
			name = v
			break
		}
	}
	switch {
	case name != "":
	case operationID != "":
		name = operationID
	default:
		name = channel + " message"
	}
	return []message{{name: name, node: body, line: n.Line}}
}

func (p *builder) asyncV3(r *yaml.Node, d *defs) {
	channels := get(r, "channels")
	for _, ov := range pairs(get(r, "operations")) {
		o := ov[1]
		predicate := ""
		switch str(get(o, "action")) {
		case "send":
			predicate = "publishes"
		case "receive":
			predicate = "subscribes_to"
		default:
			continue
		}
		chRef := str(get(get(o, "channel"), "$ref"))
		id, _ := refName(chRef)
		ch := deref(r, get(channels, id))
		if ch == nil {
			continue
		}
		channel := str(get(ch, "address"))
		if channel == "" {
			channel = id
		}
		var msgs []message
		if list := get(o, "messages"); list != nil && list.Kind == yaml.SequenceNode && len(list.Content) > 0 {
			for _, m := range list.Content {
				mid, _ := refName(str(get(m, "$ref")))
				msgs = append(msgs, v3Message(r, get(get(ch, "messages"), mid), mid, m.Line))
			}
		} else {
			for _, mv := range pairs(get(ch, "messages")) {
				msgs = append(msgs, v3Message(r, mv[1], mv[0].Value, mv[0].Line))
			}
		}
		for _, m := range msgs {
			if m.node == nil {
				continue
			}
			if !p.message(r, d, channel, keyLine(channels, id), predicate, keyLine(o, "action"), m) {
				return
			}
		}
	}
}

func v3Message(r, n *yaml.Node, id string, line int) message {
	body := deref(r, n)
	name := str(get(body, "name"))
	if name == "" {
		name = id
	}
	return message{name: name, node: body, line: line}
}

// message adds the event of a message on a channel: the publishes or
// subscribes_to fact of the repository, the channel and the payload. It
// reports false past the event limit.
func (p *builder) message(r *yaml.Node, d *defs, channel string, channelLine int, predicate string, opLine int, m message) bool {
	ev := Event(p.repo, channel, m.name)
	if !p.event(ev.Key) {
		return false
	}
	p.fact(p.repo, predicate, &ev, "", opLine)
	p.fact(ev, "channel", nil, channel, channelLine)
	payload := get(m.node, "payload")
	if model := p.schemaModel(payload, m.name+" payload", d); model != nil {
		line := keyLine(m.node, "payload")
		if line == 0 {
			line = m.line
		}
		p.fact(ev, "payload", model, "", line)
	}
	return true
}
