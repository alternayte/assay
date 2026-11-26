package files

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// DrawIOMarkdown returns the labels of a draw.io file. Each diagram of the
// mxfile is a section named after its tab, and its labels are list items
// without HTML. A compressed diagram is base64 of raw deflate of the
// URL-encoded graph model. A file that is only an mxGraphModel is one
// diagram.
func DrawIOMarkdown(b []byte) (string, error) {
	var file struct {
		XMLName  xml.Name
		Diagrams []struct {
			Name  string `xml:"name,attr"`
			Inner []byte `xml:",innerxml"`
		} `xml:"diagram"`
	}
	if err := xml.Unmarshal(b, &file); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	var out []string
	text := false
	switch file.XMLName.Local {
	case "mxfile":
		for i, d := range file.Diagrams {
			model, err := diagramModel(d.Inner)
			if err != nil {
				return "", fmt.Errorf("%w: diagram %d: %w", ErrUnreadable, i+1, err)
			}
			labels, err := drawioLabels(model)
			if err != nil {
				return "", fmt.Errorf("%w: diagram %d: %w", ErrUnreadable, i+1, err)
			}
			name := strings.TrimSpace(d.Name)
			if name == "" {
				name = "Page " + strconv.Itoa(i+1)
			}
			out = append(out, heading(1, name))
			if len(labels) > 0 {
				out = append(out, list(labels))
				text = true
			}
		}
	case "mxGraphModel":
		labels, err := drawioLabels(b)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrUnreadable, err)
		}
		if len(labels) > 0 {
			out = append(out, list(labels))
			text = true
		}
	default:
		return "", fmt.Errorf("%w: the root element is %s, not mxfile", ErrUnreadable, file.XMLName.Local)
	}
	if !text {
		return "", ErrNoText
	}
	return strings.Join(out, "\n\n"), nil
}

func list(items []string) string {
	lines := make([]string, len(items))
	for i, s := range items {
		lines[i] = "- " + escape(s)
	}
	return strings.Join(lines, "\n")
}

// diagramModel returns the graph model XML of a diagram element: the
// element itself, or the decoded text of a compressed diagram.
func diagramModel(inner []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(inner)
	if bytes.HasPrefix(trimmed, []byte("<")) {
		return trimmed, nil
	}
	if len(trimmed) == 0 {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(trimmed))
	if err != nil {
		return nil, fmt.Errorf("the diagram is not base64: %w", err)
	}
	r := flate.NewReader(bytes.NewReader(raw))
	defer func() { _ = r.Close() }()
	inflated, err := io.ReadAll(io.LimitReader(r, maxText+1))
	if err != nil {
		return nil, fmt.Errorf("the diagram does not inflate: %w", err)
	}
	if len(inflated) > maxText {
		return nil, errors.New("the diagram is larger than the limit")
	}
	decoded, err := url.PathUnescape(string(inflated))
	if err != nil {
		return nil, fmt.Errorf("the diagram is not URL-encoded: %w", err)
	}
	return []byte(decoded), nil
}

// drawioLabels returns the labels of a graph model in document order:
// mxCell@value, and the label of UserObject and object elements, which
// wrap a cell. Duplicates and empty labels are left out.
func drawioLabels(model []byte) ([]string, error) {
	if len(model) == 0 {
		return nil, nil
	}
	dec := xml.NewDecoder(bytes.NewReader(model))
	seen := map[string]bool{}
	var out []string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		var v string
		switch se.Name.Local {
		case "mxCell":
			v = attr(se, "value")
		case "UserObject", "object":
			v = attr(se, "label")
		default:
			continue
		}
		if s := plainLabel(v); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
}

// labelBreaks are the elements of a label that separate words.
var labelBreaks = map[string]bool{"br": true, "div": true, "p": true, "li": true, "ul": true, "ol": true, "tr": true, "td": true, "th": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "hr": true}

// plainLabel removes the HTML of a label. Line breaks and blocks become
// spaces.
func plainLabel(v string) string {
	if !strings.ContainsAny(v, "<&") {
		return strings.Join(strings.Fields(v), " ")
	}
	nodes, err := html.ParseFragment(strings.NewReader(v), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return strings.Join(strings.Fields(v), " ")
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style"):
			return
		case n.Type == html.ElementNode && labelBreaks[n.Data]:
			b.WriteString(" ")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && labelBreaks[n.Data] {
			b.WriteString(" ")
		}
	}
	for _, n := range nodes {
		walk(n)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
