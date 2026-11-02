// Package code splits source files into passages. Go, TypeScript,
// JavaScript, C# and Python files parse with tree-sitter: each top-level
// declaration is one passage with its leading comments and its line range.
// A declaration above the passage size splits into its members, and a
// member above the size splits at blank lines. Imports form one passage.
// Other files split at blank lines.
package code

import (
	"fmt"
	"path"
	"strings"
	gosync "sync"
	"unicode/utf8"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/alternayte/assay/internal/content"
)

// Languages by file extension. The value is the code fence language.
var extensions = map[string]string{
	".go": "go", ".ts": "typescript", ".mts": "typescript", ".cts": "typescript", ".tsx": "tsx",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascript",
	".cs": "csharp", ".py": "python",
}

// fenceLanguages are the code fence languages of the fallback files.
var fenceLanguages = map[string]string{".json": "json", ".mod": "go-mod", ".csproj": "xml", ".props": "xml", ".xml": "xml",
	".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".proto": "protobuf", ".sql": "sql", ".sh": "sh", ".java": "java",
	".kt": "kotlin", ".rb": "ruby", ".rs": "rust", ".php": "php", ".c": "c", ".h": "c", ".cpp": "cpp", ".swift": "swift"}

// Language is the parser language of a file, or "" when the file uses the
// blank-line fallback.
func Language(file string) string { return extensions[strings.ToLower(path.Ext(file))] }

// FenceLanguage is the code fence language of a file.
func FenceLanguage(file string) string {
	if l := Language(file); l != "" {
		return l
	}
	if path.Base(file) == "go.mod" {
		return "go-mod"
	}
	return fenceLanguages[strings.ToLower(path.Ext(file))]
}

var (
	loadOnce gosync.Once
	langs    map[string]*ts.Language
)

func language(name string) *ts.Language {
	loadOnce.Do(func() {
		langs = map[string]*ts.Language{"go": grammars.GoLanguage(), "typescript": grammars.TypescriptLanguage(),
			"tsx": grammars.TsxLanguage(), "javascript": grammars.JavascriptLanguage(), "csharp": grammars.CSharpLanguage(),
			"python": grammars.PythonLanguage()}
	})
	return langs[name]
}

// Body is the normalized Markdown body of a source file: one fenced code
// block.
func Body(file string, src []byte) string {
	s := strings.TrimRight(string(src), "\n")
	return fence(FenceLanguage(file), s)
}

func fence(lang, s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	f := strings.Repeat("`", max(3, longest+1))
	return fmt.Sprintf("%s%s\n%s\n%s", f, lang, s, f)
}

// unit is a run of source lines that becomes one passage or more.
type unit struct {
	start, end int // 0-based lines, end inclusive
	heading    []string
}

// Split returns the passages of a source file. Lines are 1-based in the
// passages.
func Split(file string, src []byte) []content.Passage {
	text := strings.TrimRight(string(src), "\n")
	lines := strings.Split(text, "\n")
	lang := FenceLanguage(file)
	var units []unit
	if name := Language(file); name != "" {
		units = parse(language(name), []byte(text), lines)
	}
	if units == nil {
		units = blankBlocks(lines)
	}
	var out []content.Passage
	for i, u := range units {
		for _, p := range pieces(lines, u.start, u.end) {
			body := strings.Join(lines[p[0]:p[1]+1], "\n")
			norm := content.Normalize(body)
			if norm == "" {
				continue
			}
			out = append(out, content.Passage{Section: i + 1, Kind: "code", HeadingPath: u.heading, Text: fence(lang, body), Norm: norm,
				StartLine: p[0] + 1, EndLine: p[1] + 1})
		}
	}
	for i := range out {
		out[i].Ordinal = i
		out[i].Hash = content.HashOf(out[i].Norm)
	}
	return out
}

// parse returns the units of a parsed file, or nil when it does not parse.
func parse(lang *ts.Language, src []byte, lines []string) []unit {
	if lang == nil {
		return nil
	}
	tree, err := ts.NewParser(lang).Parse(src)
	if err != nil || tree == nil {
		return nil
	}
	root := tree.RootNode()
	if root == nil {
		return nil
	}
	p := &parser{lang: lang, lines: lines}
	p.members(children(root), nil)
	return p.units
}

type parser struct {
	lang  *ts.Language
	lines []string
	units []unit
}

func children(n *ts.Node) []*ts.Node {
	out := make([]*ts.Node, 0, n.ChildCount())
	for i := range n.ChildCount() {
		if c := n.Child(i); c != nil && c.IsNamed() {
			out = append(out, c)
		}
	}
	return out
}

// importTypes are the node types that join the imports passage.
var importTypes = map[string]bool{"package_clause": true, "import_declaration": true, "import_statement": true,
	"import_from_statement": true, "future_import_statement": true, "using_directive": true, "extern_alias_directive": true,
	"file_scoped_namespace_declaration": true}

// containers are the node types that only wrap declarations. Their members
// are always units of their own.
var containers = map[string]bool{"namespace_declaration": true, "internal_module": true, "module": true}

// members adds the units of a list of sibling nodes. Comments join the
// next declaration. Imports join into one unit.
func (p *parser) members(nodes []*ts.Node, heading []string) {
	pending := -1 // first line of comments that wait for a declaration
	imports := -1
	importsEnd := -1
	flushImports := func() {
		if imports >= 0 {
			p.units = append(p.units, unit{start: imports, end: importsEnd, heading: heading})
			imports, importsEnd = -1, -1
		}
	}
	for _, n := range nodes {
		typ := n.Type(p.lang)
		start, end := int(n.StartPoint().Row), endLine(n)
		switch {
		case typ == "comment" || typ == "line_comment" || typ == "block_comment":
			if pending < 0 {
				pending = start
			}
			continue
		case importTypes[typ]:
			if pending >= 0 && imports < 0 {
				start = pending
			}
			pending = -1
			if imports < 0 {
				imports = start
			}
			importsEnd = max(importsEnd, end)
			continue
		}
		flushImports()
		if pending >= 0 {
			start = pending
			pending = -1
		}
		if body, head := p.body(n); body != nil && (containers[typ] || chars(p.lines, start, end) > content.MaxPassageChars) {
			inner := append(append([]string{}, heading...), head)
			bodyStart := int(body.StartPoint().Row)
			if bodyStart > start {
				p.units = append(p.units, unit{start: start, end: bodyStart, heading: inner})
			}
			before := len(p.units)
			p.members(children(body), inner)
			if len(p.units) == before {
				p.units = append(p.units, unit{start: start, end: end, heading: heading})
			}
			continue
		}
		p.units = append(p.units, unit{start: start, end: end, heading: heading})
	}
	flushImports()
	if pending >= 0 && len(nodes) > 0 {
		p.units = append(p.units, unit{start: pending, end: endLine(nodes[len(nodes)-1]), heading: heading})
	}
}

// endLine is the last line of a node. A node that ends at column 0 ends on
// the line before.
func endLine(n *ts.Node) int {
	e := n.EndPoint()
	if e.Column == 0 && e.Row > n.StartPoint().Row {
		return int(e.Row) - 1
	}
	return int(e.Row)
}

// bodyTypes are the node types that hold the members of a declaration.
var bodyTypes = map[string]bool{"declaration_list": true, "class_body": true, "interface_body": true, "enum_body": true,
	"object_type": true, "field_declaration_list": true, "statement_block": true, "block": true, "enum_member_declaration_list": true}

// body returns the member list of a declaration and its heading: the first
// line of the declaration. Wrappers such as export statements and
// decorators pass through to the declaration that they hold.
func (p *parser) body(n *ts.Node) (*ts.Node, string) {
	head := strings.TrimSpace(strings.TrimRight(p.lines[n.StartPoint().Row], "{ "))
	if utf8.RuneCountInString(head) > 120 {
		head = string([]rune(head)[:120])
	}
	var find func(n *ts.Node, depth int) *ts.Node
	find = func(n *ts.Node, depth int) *ts.Node {
		if depth > 4 {
			return nil
		}
		for _, c := range children(n) {
			if bodyTypes[c.Type(p.lang)] && c.NamedChildCount() > 0 {
				return c
			}
		}
		for _, c := range children(n) {
			switch c.Type(p.lang) {
			case "class_declaration", "abstract_class_declaration", "interface_declaration", "struct_declaration", "record_declaration",
				"enum_declaration", "class_definition", "function_definition", "type_spec", "struct_type", "interface_type",
				"lexical_declaration", "variable_declarator", "class":
				if b := find(c, depth+1); b != nil {
					return b
				}
			}
		}
		return nil
	}
	return find(n, 0), head
}

func chars(lines []string, start, end int) int {
	n := 0
	for _, l := range lines[start : end+1] {
		n += utf8.RuneCountInString(l) + 1
	}
	return n
}

// blankBlocks splits lines at blank lines. Small blocks join the next
// block up to the passage size.
func blankBlocks(lines []string) []unit {
	var out []unit
	start := -1
	for i, l := range lines {
		blank := strings.TrimSpace(l) == ""
		if !blank && start < 0 {
			start = i
		}
		if (blank || i == len(lines)-1) && start >= 0 {
			end := i
			if blank {
				end = i - 1
			}
			if n := len(out); n > 0 && chars(lines, out[n-1].start, end) <= minBlockChars {
				out[n-1].end = end
			} else {
				out = append(out, unit{start: start, end: end})
			}
			start = -1
		}
	}
	return out
}

// minBlockChars is the size below which a blank-line block joins the next.
const minBlockChars = 400

// pieces cuts lines start..end into runs of at most MaxPassageChars, at
// blank lines where it can. A line longer than the limit is its own run.
func pieces(lines []string, start, end int) [][2]int {
	if chars(lines, start, end) <= content.MaxPassageChars {
		return [][2]int{{start, end}}
	}
	var out [][2]int
	from, size, lastBlank := start, 0, -1
	for i := start; i <= end; i++ {
		n := utf8.RuneCountInString(lines[i]) + 1
		if size+n > content.MaxPassageChars && i > from {
			cut := i - 1
			if lastBlank > from {
				cut = lastBlank
			}
			out = append(out, [2]int{from, cut})
			from, size, lastBlank = cut+1, chars(lines, cut+1, i-1), -1
			if from > i-1 {
				size = 0
			}
		}
		size += n
		if strings.TrimSpace(lines[i]) == "" {
			lastBlank = i
		}
	}
	if from <= end {
		out = append(out, [2]int{from, end})
	}
	return out
}
