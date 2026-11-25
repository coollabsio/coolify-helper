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
// "compilerOptions.paths" aliases of the root tsconfig.base.json/tsconfig.json
// ("integrated" repositories, where libraries have no package.json) are not
// declared anywhere. Until scan (§9) reports them as reference_paths, the
// resolver assumes the worst: every project depends on every project that a
// root alias points into. Finer import edges come from scan.
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

// Pathspecs implements resolver.Resolver: nx.json, every project.json, the root
// tsconfig files, and what the js workspace reads (the members are projects).
func (*Resolver) Pathspecs(p string) bool {
	if inNodeModules(p) {
		return false
	}
	switch {
	case p == "nx.json", path.Base(p) == "project.json":
		return true
	}
	for _, f := range rootTSConfigs {
		if p == f {
			return true
		}
	}
	return js.New().Pathspecs(p)
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

	// Root path aliases: every project may import them.
	for _, f := range rootTSConfigs {
		b, ok := files[f]
		if !ok {
			continue
		}
		targets, err := aliasTargets(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
		for _, tg := range targets {
			if owner, ok := r.innermost(tg); ok {
				for _, d := range dirs {
					r.addEdge(d, owner)
				}
			}
		}
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

// aliasTargets returns the repository paths that the compilerOptions.paths of
// a root tsconfig point to (up to the first "*").
func aliasTargets(b []byte) ([]string, error) {
	var cfg struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := jsonc.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	base, ok := pathx.Join("", cfg.CompilerOptions.BaseURL)
	if !ok {
		return nil, nil
	}
	var out []string
	for _, alias := range sortedKeys(cfg.CompilerOptions.Paths) {
		for _, tg := range cfg.CompilerOptions.Paths[alias] {
			if i := strings.IndexByte(tg, '*'); i >= 0 {
				tg = tg[:i]
			}
			if strings.HasPrefix(tg, "/") {
				continue
			}
			p, ok := pathx.Join(base, tg)
			if !ok {
				continue
			}
			out = append(out, p)
		}
	}
	return out, nil
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
