// Command vischeck fails when a query on a content table has no visible-set
// filter and no allowed system marker (REQ-VIS-002).
//
// A content query names one of the tables source_items, passages, chunks,
// emb_*, claim_evidence, entity_sources and entity_aliases. Each reference
// needs a filter "connection_id = ANY(@visible...)" in the same query. A
// sync, purge or resolution query can carry "-- vis:system" instead, but
// only in the packages sync, content, connectors and resolve.
//
// vischeck reads every queries.sql file and every SQL string literal in the
// Go files under internal/ and cmd/. Test files and generated files are not
// read.
//
//	go run ./tools/vischeck [root]
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Finding is one query that fails the rule.
type Finding struct {
	File  string
	Line  int
	Query string
	Issue string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: query %s %s", f.File, f.Line, f.Query, f.Issue)
}

var (
	tableRef    = regexp.MustCompile(`(?i)\b(?:from|join|into|update)\s+(source_items|passages|chunks|emb_[a-z0-9_]+|claim_evidence|entity_sources|entity_aliases)\b`)
	visibleRef  = regexp.MustCompile(`(?i)\bconnection_id\s*=\s*any\s*\(\s*(?:@visible|sqlc\.arg\(\s*'?visible'?\s*\))`)
	systemMark  = regexp.MustCompile(`--\s*vis:system\b`)
	nameLine    = regexp.MustCompile(`^--\s*name:\s*(\w+)`)
	systemPkgs  = []string{"sync", "content", "connectors", "resolve"}
	lineComment = regexp.MustCompile(`--[^\n]*`)
)

// checkQuery applies the rule to one query of the package pkg.
func checkQuery(pkg, sql string) string {
	body := lineComment.ReplaceAllStringFunc(sql, func(c string) string {
		if systemMark.MatchString(c) {
			return c
		}
		return ""
	})
	refs := tableRef.FindAllStringSubmatch(body, -1)
	if len(refs) == 0 {
		return ""
	}
	if systemMark.MatchString(body) {
		if slices.Contains(systemPkgs, pkg) {
			return ""
		}
		return "has the vis:system marker outside the packages " + strings.Join(systemPkgs, ", ")
	}
	if n := len(visibleRef.FindAllString(body, -1)); n < len(refs) {
		tables := []string{}
		for _, r := range refs {
			if !slices.Contains(tables, strings.ToLower(r[1])) {
				tables = append(tables, strings.ToLower(r[1]))
			}
		}
		return fmt.Sprintf("reads %s with %d of %d visible filters", strings.Join(tables, ", "), n, len(refs))
	}
	return ""
}

// pkgOf returns the feature package of a path under internal/.
func pkgOf(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) > 2 && parts[0] == "internal" {
		return parts[1]
	}
	return ""
}

// Check returns the findings under root.
func Check(root string) ([]Finding, error) {
	var out []Finding
	for _, dir := range []string{"internal", "cmd"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			name := d.Name()
			switch {
			case strings.HasSuffix(name, ".sql") && strings.Contains(name, "queries"):
				f, err := checkSQLFile(path, rel)
				out = append(out, f...)
				return err
			case strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") && !strings.HasSuffix(name, ".gen.go"):
				f, err := checkGoFile(path, rel)
				out = append(out, f...)
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func checkSQLFile(path, rel string) ([]Finding, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Finding
	lines := strings.Split(string(b), "\n")
	flush := func(name string, line int, body []string) {
		if name == "" {
			return
		}
		if issue := checkQuery(pkgOf(rel), strings.Join(body, "\n")); issue != "" {
			out = append(out, Finding{File: rel, Line: line, Query: name, Issue: issue})
		}
	}
	var name string
	var start int
	var body []string
	for i, l := range lines {
		if m := nameLine.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			flush(name, start, body)
			name, start, body = m[1], i+1, nil
			continue
		}
		body = append(body, l)
	}
	flush(name, start, body)
	return out, nil
}

func checkGoFile(path, rel string) ([]Finding, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var out []Finding
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		s, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if issue := checkQuery(pkgOf(rel), s); issue != "" {
			out = append(out, Finding{File: rel, Line: fset.Position(lit.Pos()).Line, Query: "literal", Issue: issue})
		}
		return true
	})
	return out, nil
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	findings, err := Check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vischeck:", err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Printf("vischeck: %d queries need a visible filter or an allowed vis:system marker\n", len(findings))
		os.Exit(1)
	}
}
