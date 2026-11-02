package design

import (
	"path"
	"slices"
	"strings"
)

// File is one file of a repository for component detection.
type File struct {
	Path     string
	Code     bool
	Passages int
}

// Component is one section unit of a design page, found from the code
// structure. It is not an entity.
type Component struct {
	Path  string // "." for the repository root
	Name  string
	Kind  string // go, csproj, npm, python, group or root
	Files []string
	// Passages counts the passages of the source files.
	Passages int
}

// Components finds the components of a repository from the paths of its
// source files: Go package directories under cmd/ and internal/, the
// directories of .csproj files, of workspace package.json files and of
// top-level Python packages. Other source files belong to the root. The
// smallest component merges into its parent until at most limit remain.
func Components(repoName string, files []File, limit int) []Component {
	marks := map[string]string{}
	for _, f := range files {
		if !f.Code {
			continue
		}
		d, base := path.Dir(f.Path), path.Base(f.Path)
		switch {
		case strings.HasSuffix(base, ".go") && (strings.HasPrefix(d, "cmd/") || strings.HasPrefix(d, "internal/")):
			marks[d] = "go"
		case strings.HasSuffix(base, ".csproj") && d != ".":
			marks[d] = "csproj"
		case base == "package.json" && d != ".":
			marks[d] = "npm"
		case base == "__init__.py" && d != "." && !hasInit(files, path.Dir(d)):
			marks[d] = "python"
		}
	}
	byPath := map[string]*Component{}
	for _, f := range files {
		if !f.Code {
			continue
		}
		owner := "."
		for d := path.Dir(f.Path); d != "."; d = path.Dir(d) {
			if _, ok := marks[d]; ok {
				owner = d
				break
			}
		}
		c, ok := byPath[owner]
		if !ok {
			kind := marks[owner]
			if owner == "." {
				kind = "root"
			}
			c = &Component{Path: owner, Kind: kind}
			byPath[owner] = c
		}
		c.Files = append(c.Files, f.Path)
		c.Passages += f.Passages
	}
	for len(byPath) > max(limit, 1) {
		var small *Component
		for _, c := range byPath {
			if c.Path == "." && len(byPath) > 1 {
				continue
			}
			if small == nil || c.Passages < small.Passages || (c.Passages == small.Passages && depth(c.Path) > depth(small.Path)) ||
				(c.Passages == small.Passages && depth(c.Path) == depth(small.Path) && c.Path > small.Path) {
				small = c
			}
		}
		if small == nil || small.Path == "." {
			break
		}
		target := parentOf(small.Path, byPath)
		delete(byPath, small.Path)
		t, ok := byPath[target]
		if !ok {
			kind := "group"
			if target == "." {
				kind = "root"
			}
			t = &Component{Path: target, Kind: kind}
			byPath[target] = t
		}
		t.Files = append(t.Files, small.Files...)
		t.Passages += small.Passages
	}
	out := make([]Component, 0, len(byPath))
	for _, c := range byPath {
		slices.Sort(c.Files)
		c.Name = path.Base(c.Path)
		if c.Path == "." {
			c.Name = repoName
		}
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b Component) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func hasInit(files []File, dir string) bool {
	return slices.ContainsFunc(files, func(f File) bool { return f.Path == dir+"/__init__.py" })
}

func depth(p string) int {
	if p == "." {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// parentOf is the nearest component above p, or the parent directory of p
// when no component is above it.
func parentOf(p string, byPath map[string]*Component) string {
	for d := path.Dir(p); ; d = path.Dir(d) {
		if _, ok := byPath[d]; ok || d == "." {
			if ok {
				return d
			}
			break
		}
	}
	return path.Dir(p)
}
