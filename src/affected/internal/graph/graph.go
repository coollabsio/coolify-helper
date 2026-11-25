// Package graph is the resolved workspace graph of a commit: the member
// packages of every detected resolver, their edges, and the global files. It is
// what the graph cache stores, so it serializes to JSON.
package graph

import (
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
)

// Widen records why a limit or resolver was dropped, which widens the set.
type Widen struct {
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

// Package is one workspace member.
type Package struct {
	// Dir is repository-relative, "" for the repository root.
	Dir string `json:"dir"`
	// Names are the names other members and commands refer to it by (npm name, Nx project name…).
	Names []string `json:"names,omitempty"`
	// Resolvers that found the package.
	Resolvers []string `json:"resolvers"`
	// Manifests are the member's manifest files. Every member manifest is global.
	Manifests []string `json:"manifests,omitempty"`
	// Deps are names of dependencies. Only names of members become edges.
	Deps []string `json:"deps,omitempty"`
	// DepDirs are edges by directory (file: and link: specifiers, path dependencies).
	DepDirs []string `json:"dep_dirs,omitempty"`
	// Lifecycle is set when the manifest declares install-time lifecycle scripts:
	// the package manager runs them for every member, so the whole directory is global.
	Lifecycle bool `json:"lifecycle,omitempty"`
}

// Label is how the package appears in a "via" chain.
func (p *Package) Label() string {
	if len(p.Names) > 0 {
		return p.Names[0]
	}
	return pathx.Display(p.Dir)
}

// Global is a path that affects every application of the workspace.
type Global struct {
	Kind    match.Kind `json:"kind"`
	Path    string     `json:"path,omitempty"`
	Pattern string     `json:"pattern,omitempty"`
	// Source says where it comes from, such as "pnpm" or "turbo.json globalDependencies".
	Source string `json:"source"`
}

// Graph is the merged graph of every detected resolver.
type Graph struct {
	Packages  []Package `json:"packages"`
	Globals   []Global  `json:"globals"`
	Lockfiles []string  `json:"lockfiles"`
	Resolvers []string  `json:"resolvers"`
	Widened   []Widen   `json:"widened"`
	// NotModeled lists build systems the tool detects but does not model (bazel,
	// pants, buck). The workspace limit does not apply when it is not empty.
	NotModeled []string `json:"not_modeled"`

	byDir  map[string]int
	byName map[string][]int
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{Packages: []Package{}, Globals: []Global{}, Lockfiles: []string{}, Resolvers: []string{}, Widened: []Widen{}, NotModeled: []string{}}
}

// AddPackage adds a member or merges it into the member of the same directory.
func (g *Graph) AddPackage(p Package) {
	g.index()
	if i, ok := g.byDir[p.Dir]; ok {
		q := &g.Packages[i]
		q.Names = mergeStrings(q.Names, p.Names)
		q.Resolvers = mergeStrings(q.Resolvers, p.Resolvers)
		q.Manifests = mergeStrings(q.Manifests, p.Manifests)
		q.Deps = mergeStrings(q.Deps, p.Deps)
		q.DepDirs = mergeStrings(q.DepDirs, p.DepDirs)
		q.Lifecycle = q.Lifecycle || p.Lifecycle
		for _, n := range p.Names {
			if !containsInt(g.byName[n], i) {
				g.byName[n] = append(g.byName[n], i)
			}
		}
		return
	}
	p.Names = mergeStrings(nil, p.Names)
	p.Resolvers = mergeStrings(nil, p.Resolvers)
	p.Manifests = mergeStrings(nil, p.Manifests)
	p.Deps = mergeStrings(nil, p.Deps)
	p.DepDirs = mergeStrings(nil, p.DepDirs)
	g.Packages = append(g.Packages, p)
	i := len(g.Packages) - 1
	g.byDir[p.Dir] = i
	for _, n := range p.Names {
		g.byName[n] = append(g.byName[n], i)
	}
}

// AddGlobal adds a global path.
func (g *Graph) AddGlobal(kind match.Kind, pathOrPattern, source string) {
	gl := Global{Kind: kind, Source: source}
	if kind == match.Glob {
		gl.Pattern = pathOrPattern
	} else {
		gl.Path = pathOrPattern
	}
	for _, x := range g.Globals {
		if x.Kind == gl.Kind && x.Path == gl.Path && x.Pattern == gl.Pattern {
			return
		}
	}
	g.Globals = append(g.Globals, gl)
}

// AddLockfile records a lockfile, which is also a global file.
func (g *Graph) AddLockfile(path, source string) {
	if !containsString(g.Lockfiles, path) {
		g.Lockfiles = append(g.Lockfiles, path)
	}
	g.AddGlobal(match.File, path, source)
}

// AddWiden records a widening.
func (g *Graph) AddWiden(scope, reason string) {
	g.Widened = append(g.Widened, Widen{Scope: scope, Reason: reason})
}

// Merge merges another graph into g.
func (g *Graph) Merge(o *Graph) {
	if o == nil {
		return
	}
	for _, p := range o.Packages {
		g.AddPackage(p)
	}
	for _, gl := range o.Globals {
		if gl.Kind == match.Glob {
			g.AddGlobal(gl.Kind, gl.Pattern, gl.Source)
		} else {
			g.AddGlobal(gl.Kind, gl.Path, gl.Source)
		}
	}
	for _, l := range o.Lockfiles {
		if !containsString(g.Lockfiles, l) {
			g.Lockfiles = append(g.Lockfiles, l)
		}
	}
	g.Resolvers = mergeStrings(g.Resolvers, o.Resolvers)
	g.Widened = append(g.Widened, o.Widened...)
	g.NotModeled = mergeStrings(g.NotModeled, o.NotModeled)
}

// Normalize sorts the graph so that its JSON is deterministic, and rebuilds the index.
func (g *Graph) Normalize() {
	sort.Slice(g.Packages, func(i, j int) bool { return g.Packages[i].Dir < g.Packages[j].Dir })
	sort.Strings(g.Lockfiles)
	sort.Strings(g.Resolvers)
	sort.Strings(g.NotModeled)
	sort.SliceStable(g.Globals, func(i, j int) bool {
		a, b := g.Globals[i], g.Globals[j]
		if a.Path+a.Pattern != b.Path+b.Pattern {
			return a.Path+a.Pattern < b.Path+b.Pattern
		}
		return a.Kind < b.Kind
	})
	g.byDir, g.byName = nil, nil
	g.index()
}

func (g *Graph) index() {
	if g.byDir != nil {
		return
	}
	g.byDir = map[string]int{}
	g.byName = map[string][]int{}
	for i, p := range g.Packages {
		g.byDir[p.Dir] = i
		for _, n := range p.Names {
			g.byName[n] = append(g.byName[n], i)
		}
	}
}

// PackageAt returns the member whose directory is dir.
func (g *Graph) PackageAt(dir string) *Package {
	g.index()
	if i, ok := g.byDir[dir]; ok {
		return &g.Packages[i]
	}
	return nil
}

// ByName returns the members with a name.
func (g *Graph) ByName(name string) []*Package {
	g.index()
	var out []*Package
	for _, i := range g.byName[name] {
		out = append(out, &g.Packages[i])
	}
	return out
}

// Innermost returns the innermost member that contains path (a file or a directory).
func (g *Graph) Innermost(path string) *Package {
	g.index()
	p := path
	for {
		if i, ok := g.byDir[p]; ok {
			return &g.Packages[i]
		}
		if p == "" {
			return nil
		}
		p = pathx.Dir(p)
	}
}

// deps returns the direct dependencies of a member.
func (g *Graph) deps(p *Package) []*Package {
	var out []*Package
	seen := map[string]bool{}
	add := func(q *Package) {
		if q != nil && q.Dir != p.Dir && !seen[q.Dir] {
			seen[q.Dir] = true
			out = append(out, q)
		}
	}
	for _, n := range p.Deps {
		for _, q := range g.ByName(n) {
			add(q)
		}
	}
	for _, d := range p.DepDirs {
		add(g.PackageAt(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out
}

// Closure returns the anchors and their workspace dependencies, followed
// transitively, with the shortest "via" chain from an anchor to each member.
func (g *Graph) Closure(anchorDirs []string) map[string][]string {
	g.index()
	via := map[string][]string{}
	var queue []*Package
	anchors := append([]string(nil), anchorDirs...)
	sort.Strings(anchors)
	for _, d := range anchors {
		p := g.PackageAt(d)
		if p == nil || via[p.Dir] != nil {
			continue
		}
		label := pathx.Display(p.Dir)
		if len(p.Names) > 0 {
			label += " (" + p.Names[0] + ")"
		}
		via[p.Dir] = []string{label}
		queue = append(queue, p)
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, q := range g.deps(p) {
			if via[q.Dir] != nil {
				continue
			}
			chain := append(append([]string(nil), via[p.Dir]...), q.Label())
			via[q.Dir] = chain
			queue = append(queue, q)
		}
	}
	return via
}

// Dependents returns the members that depend on dir, transitively, without dir.
func (g *Graph) Dependents(dir string) []string {
	g.index()
	rev := map[string][]string{}
	for i := range g.Packages {
		p := &g.Packages[i]
		for _, q := range g.deps(p) {
			rev[q.Dir] = append(rev[q.Dir], p.Dir)
		}
	}
	seen := map[string]bool{dir: true}
	queue := []string{dir}
	var out []string
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		for _, r := range rev[d] {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
				queue = append(queue, r)
			}
		}
	}
	sort.Strings(out)
	return out
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsInt(list []int, n int) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

// SortedKeys returns the keys of a closure, sorted.
func SortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TrimSlash is a helper for resolvers: it strips "./" and trailing "/".
func TrimSlash(s string) string {
	s = strings.TrimPrefix(s, "./")
	return strings.TrimSuffix(s, "/")
}
