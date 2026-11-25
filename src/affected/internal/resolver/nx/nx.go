// Package nx resolves Nx projects.
//
// Members are the directories of project.json files (named by "name", else by
// the package.json of the same directory, else by the directory's base name)
// and the js workspace members (named by their package.json "nx.name" or
// "name"). Edges are "implicitDependencies" (names, name or directory globs,
// "tag:x", "*" for every project; "!" removals are ignored because removals
// never narrow) from project.json and from package.json "nx" sections, and the
// projects named in "dependsOn" and "inputs" entries.
//
// Globals: nx.json, and every "{workspaceRoot}/<glob>" entry of nx.json
// "namedInputs" (sharedGlobals included) and "targetDefaults.*.inputs", and of
// the projects' "namedInputs" and "targets.*.inputs". "{projectRoot}" entries,
// "^" entries, negations, "dependentTasksOutputFiles", "env", "runtime" and
// "externalDependencies" inputs are ignored.
//
// Nx also infers edges from TypeScript imports. Imports between packages that
// declare each other in package.json are already edges; imports through the
// "compilerOptions.paths" aliases ("integrated" repositories, where libraries
// have no package.json) are not declared anywhere. Until scan (§9) reports them
// as reference_paths, the resolver assumes the worst: every project depends on
// every project that an alias of the root tsconfig.base.json/tsconfig.json
// points into, and a project depends on every project that an alias of its own
// tsconfig*.json files points into. The files they "extend" with a relative
// path are followed (up to 5 levels). A wildcard target ("libs/*/src/index.ts")
// points into every project under its literal directory ("libs"). Finer
// import edges come from scan. Member package.json scripts that run nx ("nx
// build docs") are edges too.
//
// Projects that Nx plugins infer without a project.json are not modeled: their
// directories are outside every member, so they stay included.
package nx

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/glob"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/js"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/jsonc"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Resolver is the Nx resolver.
type Resolver struct{}

// New returns the resolver.
func New() *Resolver { return &Resolver{} }

var _ resolver.Resolver = (*Resolver)(nil)

// Name implements resolver.Resolver.
func (*Resolver) Name() string { return "nx" }

// rootTSConfigs are the root tsconfig files whose path aliases become edges.
var rootTSConfigs = []string{"tsconfig.base.json", "tsconfig.json"}

func inNodeModules(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "node_modules" {
			return true
		}
	}
	return false
}

// Pathspecs implements resolver.Resolver: nx.json, every project.json, every
// tsconfig*.json (the root ones, the projects' own and what they extend), and
// what the js workspace reads (the members are projects).
func (*Resolver) Pathspecs(p string) bool {
	if inNodeModules(p) {
		return false
	}
	switch {
	case p == "nx.json", path.Base(p) == "project.json", isTSConfig(p):
		return true
	}
	return js.New().Pathspecs(p)
}

// InputPatterns implements resolver.Resolver. A tsconfig extends file with
// another name makes the aliases widen, so it needs no pattern.
func (*Resolver) InputPatterns() []string {
	return append([]string{"nx.json", "**/project.json", "**/tsconfig*.json"}, js.New().InputPatterns()...)
}

// Detect implements resolver.Resolver.
func (*Resolver) Detect(t tree.Tree) bool {
	if _, ok := t.Lookup("nx.json"); ok {
		return true
	}
	for _, e := range t.Entries() {
		if (e.Path == "project.json" || strings.HasSuffix(e.Path, "/project.json")) && !inNodeModules(e.Path) {
			return true
		}
	}
	return false
}

// projectConfig is the part of project.json (or a package.json "nx" section)
// the resolver reads.
type projectConfig struct {
	Name                 string                       `json:"name"`
	ImplicitDependencies []string                     `json:"implicitDependencies"`
	Tags                 []string                     `json:"tags"`
	Targets              map[string]targetConfig      `json:"targets"`
	NamedInputs          map[string][]json.RawMessage `json:"namedInputs"`
}

type targetConfig struct {
	DependsOn []json.RawMessage `json:"dependsOn"`
	Inputs    []json.RawMessage `json:"inputs"`
}

type nxJSON struct {
	NamedInputs          map[string][]json.RawMessage `json:"namedInputs"`
	TargetDefaults       map[string]targetConfig      `json:"targetDefaults"`
	ImplicitDependencies map[string]json.RawMessage   `json:"implicitDependencies"`
}

type project struct {
	dir       string
	names     []string
	tags      []string
	manifests []string
	cfgs      []projectConfig
	depDirs   []string
}

// Resolve implements resolver.Resolver.
func (*Resolver) Resolve(ctx context.Context, t tree.Tree) (*graph.Graph, error) {
	w, err := js.Load(ctx, t)
	if err != nil {
		return nil, err
	}
	g := graph.New()
	g.Resolvers = append(g.Resolvers, "nx")

	// One batch: nx.json, root tsconfigs, every project.json and the package.json
	// beside a project.json that is not a js member (already read).
	var paths []string
	if _, ok := t.Lookup("nx.json"); ok {
		paths = append(paths, "nx.json")
	}
	for _, f := range rootTSConfigs {
		if _, ok := t.Lookup(f); ok {
			paths = append(paths, f)
		}
	}
	var projectFiles []string
	for _, e := range t.Entries() {
		if (e.Path == "project.json" || strings.HasSuffix(e.Path, "/project.json")) && !inNodeModules(e.Path) {
			projectFiles = append(projectFiles, e.Path)
			d := pathx.Dir(e.Path)
			if w.MemberAt(d) == nil {
				pj := "package.json"
				if d != "" {
					pj = d + "/package.json"
				}
				if _, ok := t.Lookup(pj); ok {
					paths = append(paths, pj)
				}
			}
		}
	}
	paths = append(paths, projectFiles...)
	files, err := t.Read(ctx, paths)
	if err != nil {
		return nil, err
	}
	read := func(p string) ([]byte, error) {
		b, ok := files[p]
		if !ok {
			return nil, fmt.Errorf("%s: not a regular file", p)
		}
		return b, nil
	}

	projects := map[string]*project{}
	get := func(dir string) *project {
		p := projects[dir]
		if p == nil {
			p = &project{dir: dir}
			projects[dir] = p
		}
		return p
	}
	// js members are Nx projects (package.json projects).
	for _, m := range w.Members {
		p := get(m.Dir)
		if m.Name != "" {
			p.names = append(p.names, m.Name)
		}
		if raw, ok := m.Raw["nx"]; ok && string(raw) != "null" {
			var cfg projectConfig
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return nil, fmt.Errorf("%s: \"nx\": %v", m.Manifest, err)
			}
			if cfg.Name != "" {
				p.names = append(p.names, cfg.Name)
			}
			p.tags = append(p.tags, cfg.Tags...)
			p.cfgs = append(p.cfgs, cfg)
		}
	}
	for _, pf := range projectFiles {
		d := pathx.Dir(pf)
		data, err := read(pf)
		if err != nil {
			return nil, err
		}
		var cfg projectConfig
		if err := jsonc.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("%s: %v", pf, err)
		}
		p := get(d)
		p.manifests = append(p.manifests, pf)
		name := cfg.Name
		if name == "" {
			if m := w.MemberAt(d); m != nil {
				name = m.Name
			} else {
				pj := "package.json"
				if d != "" {
					pj = d + "/package.json"
				}
				if b, ok := files[pj]; ok {
					var pkg struct {
						Name string `json:"name"`
					}
					if err := jsonc.Unmarshal(b, &pkg); err != nil {
						return nil, fmt.Errorf("%s: %v", pj, err)
					}
					name = pkg.Name
					p.manifests = append(p.manifests, pj)
				}
			}
		}
		if name == "" {
			name = path.Base(d)
			if d == "" {
				name = ""
			}
		}
		if name != "" {
			p.names = append(p.names, name)
		}
		p.tags = append(p.tags, cfg.Tags...)
		p.cfgs = append(p.cfgs, cfg)
	}

	dirs := make([]string, 0, len(projects))
	for d := range projects {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	r := &refs{projects: projects, dirs: dirs}

	// nx.json.
	if b, ok := files["nx.json"]; ok {
		var cfg nxJSON
		if err := jsonc.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("nx.json: %v", err)
		}
		g.AddGlobal(match.File, "nx.json", "nx")
		for _, name := range sortedKeys(cfg.NamedInputs) {
			if err := r.inputs(g, cfg.NamedInputs[name], dirs, "nx.json namedInputs"); err != nil {
				return nil, fmt.Errorf("nx.json: %v", err)
			}
		}
		for _, name := range sortedKeys(cfg.TargetDefaults) {
			td := cfg.TargetDefaults[name]
			if err := r.inputs(g, td.Inputs, dirs, "nx.json targetDefaults"); err != nil {
				return nil, fmt.Errorf("nx.json: %v", err)
			}
			if err := r.dependsOn(td.DependsOn, dirs); err != nil {
				return nil, fmt.Errorf("nx.json: %v", err)
			}
		}
		for f := range cfg.ImplicitDependencies {
			if p, ok := pathx.Clean(f); ok && p != "" {
				g.AddGlobal(match.File, p, "nx.json implicitDependencies")
			}
		}
	} else if _, exists := t.Lookup("nx.json"); exists {
		return nil, fmt.Errorf("nx.json: not a regular file")
	}

	// Project configurations.
	for _, d := range dirs {
		p := projects[d]
		for _, cfg := range p.cfgs {
			for _, dep := range cfg.ImplicitDependencies {
				p.depDirs = append(p.depDirs, r.resolve(dep)...)
			}
			for _, name := range sortedKeys(cfg.NamedInputs) {
				if err := r.inputs(g, cfg.NamedInputs[name], []string{d}, "project namedInputs"); err != nil {
					return nil, fmt.Errorf("%s: %v", d, err)
				}
			}
			for _, name := range sortedKeys(cfg.Targets) {
				tc := cfg.Targets[name]
				if err := r.inputs(g, tc.Inputs, []string{d}, "project inputs"); err != nil {
					return nil, fmt.Errorf("%s: %v", pathx.Display(d), err)
				}
				if err := r.dependsOn(tc.DependsOn, []string{d}); err != nil {
					return nil, fmt.Errorf("%s: %v", pathx.Display(d), err)
				}
			}
		}
	}

	// Path aliases: those of the root tsconfig files (with what they extend)
	// are edges from every project, and those of a project's own tsconfig*.json
	// files are edges from that project.
	if err := r.aliasEdges(ctx, t, files); err != nil {
		return nil, err
	}

	for _, d := range dirs {
		p := projects[d]
		g.AddPackage(graph.Package{
			Dir:       d,
			Names:     p.names,
			Resolvers: []string{"nx"},
			Manifests: p.manifests,
			DepDirs:   p.depDirs,
		})
	}
	// Member scripts that run nx ("nx build docs").
	js.AddScriptEdges(w, g, g, (*Resolver)(nil).AnchorFromCommand, []string{"nx"})
	return g, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// refs resolves project references.
type refs struct {
	projects map[string]*project
	dirs     []string
}

func (r *refs) addEdge(from, to string) {
	if from == to {
		return
	}
	p := r.projects[from]
	p.depDirs = append(p.depDirs, to)
}

// innermost returns the innermost project directory that contains p.
func (r *refs) innermost(p string) (string, bool) {
	for {
		if _, ok := r.projects[p]; ok {
			return p, true
		}
		if p == "" {
			return "", false
		}
		p = pathx.Dir(p)
	}
}

// resolve returns the projects a reference selects: a name, a name or
// directory glob, "tag:x", or "*". Removals ("!x") select nothing.
func (r *refs) resolve(ref string) []string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "!") {
		return nil
	}
	var out []string
	if strings.HasPrefix(ref, "tag:") {
		tag := strings.TrimPrefix(ref, "tag:")
		for _, d := range r.dirs {
			for _, t := range r.projects[d].tags {
				if glob.NameMatch(tag, t) {
					out = append(out, d)
					break
				}
			}
		}
		return out
	}
	for _, d := range r.dirs {
		p := r.projects[d]
		hit := false
		for _, n := range p.names {
			if glob.NameMatch(ref, n) {
				hit = true
			}
		}
		if !hit && strings.ContainsAny(ref, "*/") {
			if ok, err := glob.Match(strings.TrimPrefix(ref, "./"), d); err == nil && ok {
				hit = true
			}
		}
		if hit {
			out = append(out, d)
		}
	}
	return out
}

// dependsOn reads "dependsOn" entries: "proj:target" strings and objects with
// "projects". Entries of the projects in from get the edges.
func (r *refs) dependsOn(entries []json.RawMessage, from []string) error {
	for _, e := range entries {
		var s string
		if json.Unmarshal(e, &s) == nil {
			if strings.HasPrefix(s, "^") || !strings.Contains(s, ":") {
				continue
			}
			for _, to := range r.resolve(s[:strings.IndexByte(s, ':')]) {
				for _, f := range from {
					r.addEdge(f, to)
				}
			}
			continue
		}
		var obj struct {
			Projects json.RawMessage `json:"projects"`
		}
		if err := json.Unmarshal(e, &obj); err != nil {
			return fmt.Errorf("dependsOn entry %s is neither a string nor an object", string(e))
		}
		if err := r.projectList(obj.Projects, from); err != nil {
			return err
		}
	}
	return nil
}

// projectList reads a "projects" value (a string or a list) and adds edges.
func (r *refs) projectList(raw json.RawMessage, from []string) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("\"projects\" is neither a string nor a list")
		}
		list = []string{s}
	}
	for _, ref := range list {
		if ref == "self" || ref == "dependencies" || ref == "{self}" || ref == "{dependencies}" {
			continue
		}
		for _, to := range r.resolve(ref) {
			for _, f := range from {
				r.addEdge(f, to)
			}
		}
	}
	return nil
}

// inputs reads input entries: "{workspaceRoot}/…" globs (also in {fileset})
// become globals, and {input, projects} entries become edges.
func (r *refs) inputs(g *graph.Graph, entries []json.RawMessage, from []string, source string) error {
	for _, e := range entries {
		var s string
		if json.Unmarshal(e, &s) == nil {
			if err := workspaceRootGlob(g, s, source); err != nil {
				return err
			}
			continue
		}
		var obj struct {
			Fileset  *string         `json:"fileset"`
			Projects json.RawMessage `json:"projects"`
		}
		if err := json.Unmarshal(e, &obj); err != nil {
			return fmt.Errorf("input %s is neither a string nor an object", string(e))
		}
		if obj.Fileset != nil {
			if err := workspaceRootGlob(g, *obj.Fileset, source); err != nil {
				return err
			}
		}
		if err := r.projectList(obj.Projects, from); err != nil {
			return err
		}
	}
	return nil
}

// workspaceRootGlob adds a "{workspaceRoot}/<glob>" input as global rules.
func workspaceRootGlob(g *graph.Graph, s, source string) error {
	if !strings.HasPrefix(s, "{workspaceRoot}/") {
		return nil
	}
	pattern := strings.TrimPrefix(s, "{workspaceRoot}/")
	rules, err := glob.ToRules("", pattern)
	if err != nil {
		return fmt.Errorf("input %q: %v", s, err)
	}
	for _, rule := range rules {
		g.AddGlobal(rule.Kind, rule.Value, source)
	}
	return nil
}

// maxExtends caps how many "extends" levels of a tsconfig file are followed.
const maxExtends = 5

// isTSConfig reports whether a path is a tsconfig file the resolver reads:
// "tsconfig*.json" at any depth. Pathspecs covers exactly these.
func isTSConfig(p string) bool {
	b := path.Base(p)
	return strings.HasPrefix(b, "tsconfig") && strings.HasSuffix(b, ".json")
}

// tsconfig is the part of a tsconfig file that path aliases depend on.
type tsconfig struct {
	extends []string
	// baseURL is the repository path of compilerOptions.baseUrl, "" with hasBase false when unset.
	baseURL string
	hasBase bool
	targets []string
}

func parseTSConfig(file string, b []byte) (tsconfig, error) {
	var cfg struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := jsonc.Unmarshal(b, &cfg); err != nil {
		return tsconfig{}, err
	}
	var out tsconfig
	if len(cfg.Extends) > 0 && string(cfg.Extends) != "null" {
		var one string
		if json.Unmarshal(cfg.Extends, &one) == nil {
			out.extends = []string{one}
		} else if err := json.Unmarshal(cfg.Extends, &out.extends); err != nil {
			return tsconfig{}, fmt.Errorf("\"extends\" is neither a string nor a list of strings")
		}
	}
	dir := pathx.Dir(file)
	if u := cfg.CompilerOptions.BaseURL; u != nil && !strings.HasPrefix(*u, "/") {
		if p, ok := pathx.Join(dir, *u); ok {
			out.baseURL, out.hasBase = p, true
		}
	}
	for _, alias := range sortedKeys(cfg.CompilerOptions.Paths) {
		out.targets = append(out.targets, cfg.CompilerOptions.Paths[alias]...)
	}
	return out, nil
}

// aliasEdges adds the edges of compilerOptions.paths aliases. A chain of
// tsconfig files is the root (or project) file and the files it "extends"
// with a relative path, transitively. Each alias target is resolved against
// every base of its chain (each file's directory and baseUrl): TypeScript uses
// one of them, and the union only widens. A chain that can't be read whole
// (a file that is not "tsconfig*.json", so the cache fingerprint doesn't cover
// it, a file that can't be parsed, more than maxExtends levels) may alias
// anything: its projects get an edge to every project.
func (r *refs) aliasEdges(ctx context.Context, t tree.Tree, files map[string][]byte) error {
	cache := map[string]tsconfig{}
	bad := map[string]bool{}
	for p, b := range files {
		if isTSConfig(p) {
			cfg, err := parseTSConfig(p, b)
			if err != nil {
				if pathx.Dir(p) == "" {
					return fmt.Errorf("%s: %v", p, err)
				}
				bad[p] = true
				continue
			}
			cache[p] = cfg
		}
	}
	// chains: origin file → the projects that get the edges.
	type origin struct {
		file string
		from []string
	}
	var origins []origin
	for _, f := range rootTSConfigs {
		if _, ok := t.Lookup(f); ok {
			origins = append(origins, origin{file: f, from: r.dirs})
		}
	}
	var own []string
	for _, e := range t.Entries() {
		d := pathx.Dir(e.Path)
		if d == "" || !isTSConfig(e.Path) || inNodeModules(e.Path) {
			continue
		}
		if _, ok := r.projects[d]; ok {
			own = append(own, e.Path)
			origins = append(origins, origin{file: e.Path, from: []string{d}})
		}
	}
	// Read the files level by level.
	pending := own
	for level := 0; level <= maxExtends+1 && len(pending) > 0; level++ {
		var batch []string
		for _, p := range pending {
			if _, ok := cache[p]; !ok && !bad[p] {
				batch = append(batch, p)
			}
		}
		if len(batch) > 0 {
			sort.Strings(batch)
			got, err := t.Read(ctx, batch)
			if err != nil {
				return err
			}
			for _, p := range batch {
				b, ok := got[p]
				if !ok {
					bad[p] = true
					continue
				}
				cfg, err := parseTSConfig(p, b)
				if err != nil {
					bad[p] = true
					continue
				}
				cache[p] = cfg
			}
		}
		pending = nil
		for _, p := range batch {
			for _, ext := range cache[p].extends {
				if f, ok := r.extendsFile(t, p, ext); ok && isTSConfig(f) {
					pending = append(pending, f)
				}
			}
		}
		// Root files were read in the first batch, but what they extend was not.
		if level == 0 {
			for _, f := range rootTSConfigs {
				for _, ext := range cache[f].extends {
					if ef, ok := r.extendsFile(t, f, ext); ok && isTSConfig(ef) {
						pending = append(pending, ef)
					}
				}
			}
		}
	}

	for _, o := range origins {
		chain, complete := r.chain(t, o.file, cache, bad)
		if !complete {
			for _, from := range o.from {
				for _, to := range r.dirs {
					r.addEdge(from, to)
				}
			}
			continue
		}
		var bases, targets []string
		for _, f := range chain {
			cfg := cache[f]
			bases = append(bases, pathx.Dir(f))
			if cfg.hasBase {
				bases = append(bases, cfg.baseURL)
			}
			targets = append(targets, cfg.targets...)
		}
		for _, tg := range targets {
			for _, base := range bases {
				for _, to := range r.aliasProjects(base, tg) {
					for _, from := range o.from {
						r.addEdge(from, to)
					}
				}
			}
		}
	}
	return nil
}

// extendsFile resolves an "extends" value of the tsconfig file at p. It
// reports false for a package ("@tsconfig/node20/tsconfig.json"), which lives
// in node_modules, and for a path that leaves the repository or doesn't exist.
func (r *refs) extendsFile(t tree.Tree, p, ext string) (string, bool) {
	if !strings.HasPrefix(ext, "./") && !strings.HasPrefix(ext, "../") && ext != "." && ext != ".." {
		return "", false
	}
	f, ok := pathx.Join(pathx.Dir(p), ext)
	if !ok || f == "" {
		return "", false
	}
	if _, ok := t.Lookup(f); ok {
		return f, true
	}
	if !strings.HasSuffix(f, ".json") {
		if _, ok := t.Lookup(f + ".json"); ok {
			return f + ".json", true
		}
	}
	return "", false
}

// chain returns the tsconfig files of a chain, and false when it can't be read whole.
func (r *refs) chain(t tree.Tree, file string, cache map[string]tsconfig, bad map[string]bool) ([]string, bool) {
	var out []string
	seen := map[string]bool{}
	level := []string{file}
	for depth := 0; len(level) > 0; depth++ {
		if depth > maxExtends {
			return nil, false
		}
		var next []string
		for _, f := range level {
			if seen[f] {
				continue
			}
			seen[f] = true
			cfg, ok := cache[f]
			if !ok || bad[f] {
				return nil, false
			}
			out = append(out, f)
			for _, ext := range cfg.extends {
				if !strings.HasPrefix(ext, ".") {
					continue
				}
				ef, ok := r.extendsFile(t, f, ext)
				if !ok {
					// Outside the repository, or a file that doesn't exist (tsc
					// fails): nothing in the repository to alias.
					continue
				}
				if !isTSConfig(ef) {
					return nil, false
				}
				next = append(next, ef)
			}
		}
		level = next
	}
	return out, true
}

// aliasProjects returns the projects an alias target, relative to base, may
// point into: the innermost project that contains the literal directory before
// the first "*" ("libs" for "libs/*/src/index.ts", "libs" for "libs/ui-*"),
// and every project under that directory.
func (r *refs) aliasProjects(base, tg string) []string {
	if strings.HasPrefix(tg, "/") {
		return nil
	}
	lit := tg
	wild := false
	if i := strings.IndexByte(tg, '*'); i >= 0 {
		lit, wild = tg[:i], true
	}
	dir, ok := pathx.Join(base, lit)
	if !ok {
		return nil
	}
	if wild && lit != "" && !strings.HasSuffix(lit, "/") && path.Base(lit) != "." && path.Base(lit) != ".." {
		// "libs/ui-*": the directory is "libs".
		dir = pathx.Dir(dir)
	}
	var out []string
	if owner, ok := r.innermost(dir); ok {
		out = append(out, owner)
	}
	for _, d := range r.dirs {
		if d != dir && pathx.Within(d, dir) {
			out = append(out, d)
		}
	}
	return out
}

// AnchorFromCommand implements resolver.Resolver for "nx <target> <project>",
// "nx run <project>:<target>" and "nx run-many -p …".
func (*Resolver) AnchorFromCommand(cmd command.Command, g *graph.Graph) resolver.Anchor {
	if len(cmd.Argv) == 0 || cmd.Argv[0] != "nx" {
		return resolver.Anchor{Kind: resolver.NotMine}
	}
	a := anchor(cmd, g)
	a.Tool = "nx"
	return a
}

var nxValued = map[string]bool{
	"--projects": true, "-p": true, "--project": true, "--configuration": true, "-c": true, "--exclude": true,
	"--output-style": true, "--base": true, "--head": true, "--files": true, "--target": true, "-t": true, "--targets": true,
	"--runner": true,
}

var neutral = map[string]bool{
	"show": true, "list": true, "report": true, "reset": true, "clear-cache": true, "daemon": true, "migrate": true,
	"generate": true, "g": true, "add": true, "init": true, "connect": true, "connect-to-nx-cloud": true,
	"format": true, "format:check": true, "format:write": true, "repair": true, "sync": true, "sync:check": true,
	"view-logs": true, "login": true, "logout": true, "--version": true, "--help": true, "help": true, "import": true,
	"mcp": true, "configure-ai-agents": true, "record": true, "start-ci-run": true, "fix-ci": true,
}

func unknown(format string, args ...any) resolver.Anchor {
	return resolver.Anchor{Kind: resolver.Unknown, Reason: fmt.Sprintf(format, args...)}
}

func anchor(cmd command.Command, g *graph.Graph) resolver.Anchor {
	var positional, projects []string
	project := ""
	line := strings.Join(cmd.Argv, " ")
	args := cmd.Argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := a, "", false
		if strings.HasPrefix(a, "-") {
			if j := strings.IndexByte(a, '='); j > 0 {
				name, val, hasVal = a[:j], a[j+1:], true
			}
		}
		if nxValued[name] && !hasVal && i+1 < len(args) {
			i++
			val = args[i]
		}
		switch {
		case name == "--projects" || name == "-p":
			for _, p := range strings.Split(val, ",") {
				if p = strings.TrimSpace(p); p != "" {
					projects = append(projects, p)
				}
			}
		case name == "--project":
			project = val
		case strings.HasPrefix(a, "-"):
		default:
			positional = append(positional, a)
		}
	}
	sub := ""
	if len(positional) > 0 {
		sub = positional[0]
	}
	switch sub {
	case "":
		return resolver.Anchor{Kind: resolver.Neutral}
	case "affected", "graph", "dep-graph", "exec", "watch", "release":
		return unknown("%q does not name the project it builds", line)
	case "run":
		if len(positional) < 2 || !strings.Contains(positional[1], ":") {
			return unknown("%q does not name a project", line)
		}
		return byNames([]string{positional[1][:strings.IndexByte(positional[1], ':')]}, g)
	case "run-many":
		if len(projects) == 0 {
			return unknown("%q runs every project", line)
		}
		return byNames(projects, g)
	}
	if neutral[sub] {
		return resolver.Anchor{Kind: resolver.Neutral}
	}
	// nx <target> [project], or nx <project>:<target>.
	if strings.Contains(sub, ":") && project == "" && len(positional) == 1 {
		return byNames([]string{sub[:strings.IndexByte(sub, ':')]}, g)
	}
	if project == "" && len(positional) > 1 {
		project = positional[1]
	}
	if project != "" {
		return byNames([]string{project}, g)
	}
	// No project: Nx infers it from the directory it runs in.
	if !cmd.DirKnown {
		return unknown("%q runs in a directory the tool can't follow", line)
	}
	if p := g.PackageAt(cmd.Dir); p != nil && cmd.Dir != "" {
		return resolver.Anchor{Kind: resolver.Anchored, Dirs: []string{p.Dir}}
	}
	return unknown("%q does not name a project", line)
}

// byNames resolves exact project names.
func byNames(names []string, g *graph.Graph) resolver.Anchor {
	var dirs []string
	for _, n := range names {
		if n == "" || strings.ContainsAny(n, "*!$") || strings.HasPrefix(n, "tag:") {
			return unknown("the project pattern %q is not modeled", n)
		}
		ps := g.ByName(n)
		if len(ps) == 0 {
			return unknown("no project is named %q", n)
		}
		for _, p := range ps {
			dirs = append(dirs, p.Dir)
		}
	}
	sort.Strings(dirs)
	out := dirs[:0]
	for i, d := range dirs {
		if i == 0 || d != dirs[i-1] {
			out = append(out, d)
		}
	}
	return resolver.Anchor{Kind: resolver.Anchored, Dirs: out}
}
