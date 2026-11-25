package js

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/glob"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/jsonc"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Lockfiles maps the root lockfiles to their package manager.
var Lockfiles = map[string]string{
	"pnpm-lock.yaml":      "pnpm",
	"package-lock.json":   "npm",
	"npm-shrinkwrap.json": "npm",
	"yarn.lock":           "yarn",
	"bun.lock":            "bun",
	"bun.lockb":           "bun",
}

// rootConfigs are root files that configure the package managers or the
// toolchain. They are globals when they exist.
var rootConfigs = []string{
	"pnpm-workspace.yaml", ".npmrc", ".yarnrc.yml", ".yarnrc", ".pnpmfile.cjs", ".pnpmfile.mjs",
	"bunfig.toml", "turbo.json", "turbo.jsonc", "nx.json", "lerna.json",
	".nvmrc", ".node-version", ".tool-versions", "mise.toml", ".mise.toml",
}

// memberConfigs are files in a member directory that the package managers
// read for that member: they are member manifests (global).
var memberConfigs = []string{".npmrc", ".yarnrc.yml", ".yarnrc", "bunfig.toml"}

// maxNesting caps nested workspaces (a member with its own "workspaces") and
// "file:" dependencies that point outside the workspace.
const maxNesting = 4

// Member is one workspace package.
type Member struct {
	Dir      string
	Name     string
	Manifest string
	// Raw is the parsed manifest, for other resolvers (the "nx" section).
	Raw map[string]json.RawMessage

	deps      []string
	depDirs   []string
	lifecycle bool
}

// Workspace is the npm/yarn/pnpm/bun workspace of a tree.
type Workspace struct {
	// Managers are the detected package managers, sorted ("npm" by default).
	Managers []string
	// HasRoot is true when the root package.json exists.
	HasRoot bool
	// Members are sorted by directory.
	Members []*Member

	rootRaw   map[string]json.RawMessage
	globals   []globalPath
	lockfiles []string
	// allDepDirs are edges from every member (overrides and resolutions to local paths).
	allDepDirs []string
	byDir      map[string]*Member
}

type globalPath struct {
	dir  bool
	path string
	glob string
}

// MemberAt returns the member whose directory is dir.
func (w *Workspace) MemberAt(dir string) *Member { return w.byDir[dir] }

// Innermost returns the innermost member that contains p, or nil.
func (w *Workspace) Innermost(p string) *Member {
	for {
		if m, ok := w.byDir[p]; ok {
			return m
		}
		if p == "" {
			return nil
		}
		p = pathx.Dir(p)
	}
}

var memo struct {
	sync.Mutex
	t   tree.Tree
	ws  *Workspace
	err error
}

// Load reads the workspace of a tree. The turbo and nx resolvers need it too,
// so the result for the last tree is kept: its manifests are read once.
func Load(ctx context.Context, t tree.Tree) (*Workspace, error) {
	cacheable := t != nil && reflect.TypeOf(t).Comparable()
	if cacheable {
		memo.Lock()
		if memo.t != nil && reflect.TypeOf(memo.t) == reflect.TypeOf(t) && memo.t == t {
			ws, err := memo.ws, memo.err
			memo.Unlock()
			return ws, err
		}
		memo.Unlock()
	}
	ws, err := load(ctx, t)
	if cacheable && ctx.Err() == nil {
		memo.Lock()
		memo.t, memo.ws, memo.err = t, ws, err
		memo.Unlock()
	}
	return ws, err
}

// skipPath reports whether a path is inside node_modules or bower_components,
// which no package manager treats as workspace members.
func skipPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "node_modules" || seg == "bower_components" {
			return true
		}
	}
	return false
}

// parseObject parses a JSON object (JSONC tolerated) into its fields.
func parseObject(b []byte) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := jsonc.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	return raw, nil
}

func stringField(raw map[string]json.RawMessage, key string) (string, error) {
	v, ok := raw[key]
	if !ok || string(v) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", fmt.Errorf("%q is not a string", key)
	}
	return s, nil
}

// workspacePatterns reads the "workspaces" field: an array, or {packages: […]}.
func workspacePatterns(raw map[string]json.RawMessage) ([]string, bool, error) {
	v, ok := raw["workspaces"]
	if !ok || string(v) == "null" {
		return nil, false, nil
	}
	var list []string
	if err := json.Unmarshal(v, &list); err == nil {
		return list, true, nil
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(v, &obj); err != nil {
		return nil, true, fmt.Errorf(`"workspaces" is neither a list of patterns nor {"packages": […]}`)
	}
	return obj.Packages, true, nil
}

type pnpmWorkspace struct {
	Packages            []string          `yaml:"packages"`
	PatchedDependencies map[string]string `yaml:"patchedDependencies"`
	Overrides           map[string]string `yaml:"overrides"`
}

func load(ctx context.Context, t tree.Tree) (*Workspace, error) {
	w := &Workspace{byDir: map[string]*Member{}}
	_, hasRoot := t.Lookup("package.json")
	_, hasYAML := t.Lookup("pnpm-workspace.yaml")
	w.HasRoot = hasRoot

	// Batch 1: the root manifests.
	var first []string
	if hasRoot {
		first = append(first, "package.json")
	}
	if hasYAML {
		first = append(first, "pnpm-workspace.yaml")
	}
	files, err := t.Read(ctx, first)
	if err != nil {
		return nil, err
	}

	var wsPatterns []string
	hasWS := false
	if hasRoot {
		data, ok := files["package.json"]
		if !ok {
			return nil, fmt.Errorf("package.json: not a regular file")
		}
		w.rootRaw, err = parseObject(data)
		if err != nil {
			return nil, fmt.Errorf("package.json: %v", err)
		}
		wsPatterns, hasWS, err = workspacePatterns(w.rootRaw)
		if err != nil {
			return nil, fmt.Errorf("package.json: %v", err)
		}
	}
	var pw pnpmWorkspace
	if hasYAML {
		data, ok := files["pnpm-workspace.yaml"]
		if !ok {
			return nil, fmt.Errorf("pnpm-workspace.yaml: not a regular file")
		}
		if err := yaml.Unmarshal(data, &pw); err != nil {
			return nil, fmt.Errorf("pnpm-workspace.yaml: %v", err)
		}
	}

	// Package managers.
	signals := map[string]bool{}
	for lf, pm := range Lockfiles {
		if _, ok := t.Lookup(lf); ok {
			signals[pm] = true
			w.lockfiles = append(w.lockfiles, lf)
		}
	}
	sort.Strings(w.lockfiles)
	if pmField, _ := stringField(w.rootRaw, "packageManager"); pmField != "" {
		name := pmField
		if i := strings.IndexByte(name, '@'); i > 0 {
			name = name[:i]
		}
		switch name {
		case "npm", "pnpm", "yarn", "bun":
			signals[name] = true
		}
	}
	explicit := map[string]bool{}
	for k := range signals {
		explicit[k] = true
	}
	if hasYAML {
		signals["pnpm"] = true
	}
	if _, ok := t.Lookup(".yarnrc.yml"); ok {
		signals["yarn"] = true
	}
	for pm := range signals {
		w.Managers = append(w.Managers, pm)
	}
	if len(w.Managers) == 0 {
		w.Managers = []string{"npm"}
	}
	sort.Strings(w.Managers)

	// Candidate members: directories with a package.json.
	var candidates []string
	for _, e := range t.Entries() {
		if e.Path == "package.json" || !strings.HasSuffix(e.Path, "/package.json") || skipPath(e.Path) {
			continue
		}
		candidates = append(candidates, pathx.Dir(e.Path))
	}

	// Which configuration lists the members.
	var patterns []string
	switch {
	case hasYAML && hasWS:
		a, errA := selectDirs("", pw.Packages, candidates)
		b, errB := selectDirs("", wsPatterns, candidates)
		switch {
		case explicit["pnpm"] && len(explicit) == 1:
			patterns = pw.Packages
		case len(explicit) > 0 && !explicit["pnpm"]:
			patterns = wsPatterns
		case errA == nil && errB == nil && equalStrings(a, b):
			patterns = pw.Packages
		default:
			return nil, fmt.Errorf("both pnpm-workspace.yaml and the package.json workspaces list members, and the package manager is ambiguous")
		}
	case hasYAML:
		patterns = pw.Packages
	case hasWS:
		patterns = wsPatterns
	}

	// Members, then nested workspaces, one read batch per level.
	type level struct {
		parent   string
		patterns []string
	}
	queue := []level{{parent: "", patterns: patterns}}
	for depth := 0; len(queue) > 0; depth++ {
		if depth > maxNesting {
			return nil, fmt.Errorf("workspaces nest deeper than %d levels", maxNesting)
		}
		var newDirs []string
		parentOf := map[string]string{}
		for _, lv := range queue {
			dirs, err := selectDirs(lv.parent, lv.patterns, candidates)
			if err != nil {
				return nil, err
			}
			for _, d := range dirs {
				if d == "" || w.byDir[d] != nil {
					continue
				}
				if _, seen := parentOf[d]; seen {
					continue
				}
				parentOf[d] = lv.parent
				newDirs = append(newDirs, d)
			}
		}
		queue = nil
		if len(newDirs) == 0 {
			break
		}
		sort.Strings(newDirs)
		var paths []string
		for _, d := range newDirs {
			paths = append(paths, d+"/package.json")
		}
		got, err := t.Read(ctx, paths)
		if err != nil {
			return nil, err
		}
		for _, d := range newDirs {
			mf := d + "/package.json"
			data, ok := got[mf]
			if !ok {
				return nil, fmt.Errorf("%s: not a regular file", mf)
			}
			raw, err := parseObject(data)
			if err != nil {
				return nil, fmt.Errorf("%s: %v (its dependencies are unknown)", mf, err)
			}
			m := &Member{Dir: d, Manifest: mf, Raw: raw}
			if m.Name, err = stringField(raw, "name"); err != nil {
				return nil, fmt.Errorf("%s: %v", mf, err)
			}
			w.byDir[d] = m
			w.Members = append(w.Members, m)
			if p := parentOf[d]; p != "" {
				// A nested worktree: the directory belonged to its parent.
				if parent := w.byDir[p]; parent != nil {
					parent.depDirs = append(parent.depDirs, d)
				}
			}
			if nested, ok, err := workspacePatterns(raw); err != nil {
				return nil, fmt.Errorf("%s: %v", mf, err)
			} else if ok && len(nested) > 0 {
				queue = append(queue, level{parent: d, patterns: nested})
			}
		}
	}
	sort.Slice(w.Members, func(i, j int) bool { return w.Members[i].Dir < w.Members[j].Dir })

	// Dependencies, lifecycle scripts and links.
	external := map[string][]*Member{} // non-member dirs that members link to with file:
	for _, m := range w.Members {
		names, dirs, outside, err := w.dependencies(m.Dir, m.Raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", m.Manifest, err)
		}
		m.deps = append(m.deps, names...)
		m.depDirs = append(m.depDirs, dirs...)
		for _, o := range outside {
			external[o] = append(external[o], m)
		}
		m.lifecycle = hasLifecycle(m.Raw)
	}
	if err := w.followExternal(ctx, t, external); err != nil {
		return nil, err
	}

	// Root-level edges (overrides, resolutions) and globals.
	if err := w.rootEdges(&pw); err != nil {
		return nil, err
	}
	w.collectGlobals(t, &pw)
	return w, nil
}

// followExternal follows "file:" dependencies to directories outside every
// member: such a directory is global, but its own dependencies on members must
// become dependencies of the members that link to it.
func (w *Workspace) followExternal(ctx context.Context, t tree.Tree, external map[string][]*Member) error {
	seen := map[string]bool{}
	for depth := 0; len(external) > 0; depth++ {
		if depth > maxNesting {
			return fmt.Errorf("file: dependencies outside the workspace nest deeper than %d levels", maxNesting)
		}
		var paths []string
		for d := range external {
			seen[d] = true
			mf := "package.json"
			if d != "" {
				mf = d + "/package.json"
			}
			if _, ok := t.Lookup(mf); ok {
				paths = append(paths, mf)
			}
		}
		sort.Strings(paths)
		got, err := t.Read(ctx, paths)
		if err != nil {
			return err
		}
		next := map[string][]*Member{}
		for _, mf := range paths {
			d := pathx.Dir(mf)
			if mf == "package.json" {
				d = ""
			}
			data, ok := got[mf]
			if !ok {
				return fmt.Errorf("%s: not a regular file", mf)
			}
			raw, err := parseObject(data)
			if err != nil {
				return fmt.Errorf("%s: %v (a member links to it with file:)", mf, err)
			}
			names, dirs, outside, err := w.dependencies(d, raw)
			if err != nil {
				return fmt.Errorf("%s: %v", mf, err)
			}
			for _, m := range external[d] {
				m.deps = append(m.deps, names...)
				m.depDirs = append(m.depDirs, dirs...)
				for _, o := range outside {
					if !seen[o] {
						next[o] = append(next[o], m)
					}
				}
			}
		}
		external = next
	}
	return nil
}

// selectDirs returns the candidate directories that the patterns (relative to
// parent) select: a candidate matches when a positive pattern matches it and no
// "!" pattern does.
func selectDirs(parent string, patterns []string, candidates []string) ([]string, error) {
	var pos, neg []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		isNeg := strings.HasPrefix(p, "!")
		p = strings.TrimPrefix(p, "!")
		p = strings.TrimSuffix(strings.TrimSuffix(p, "/package.json"), "/")
		if p == "package.json" {
			p = "."
		}
		alts, err := glob.Expand(p)
		if err != nil {
			return nil, fmt.Errorf("workspace pattern %q: %v", p, err)
		}
		for _, a := range alts {
			a = strings.TrimPrefix(a, "/")
			joined, ok := pathx.Join(parent, a)
			if !ok {
				// Outside the repository: nothing in it can change in a push.
				continue
			}
			if isNeg {
				neg = append(neg, joined)
			} else {
				pos = append(pos, joined)
			}
		}
	}
	var out []string
	for _, c := range candidates {
		if !pathx.Within(c, parent) {
			continue
		}
		in := false
		for _, p := range pos {
			ok, err := glob.Match(p, c)
			if err != nil {
				return nil, fmt.Errorf("workspace pattern %q: %v", p, err)
			}
			if ok {
				in = true
				break
			}
		}
		if !in {
			continue
		}
		for _, p := range neg {
			ok, err := glob.Match(p, c)
			if err != nil {
				return nil, fmt.Errorf("workspace pattern %q: %v", p, err)
			}
			if ok {
				in = false
				break
			}
		}
		if in {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lifecycleScripts are the scripts a package manager runs for every member on install.
var lifecycleScripts = []string{"preinstall", "install", "postinstall", "prepare", "preprepare", "postprepare", "prepublish"}

// hasLifecycle reports whether a manifest declares install-time lifecycle
// scripts. A "scripts" field it can't read counts as declaring them.
func hasLifecycle(raw map[string]json.RawMessage) bool {
	v, ok := raw["scripts"]
	if !ok || string(v) == "null" {
		return false
	}
	var scripts map[string]json.RawMessage
	if err := json.Unmarshal(v, &scripts); err != nil {
		return true
	}
	for _, s := range lifecycleScripts {
		if _, ok := scripts[s]; ok {
			return true
		}
	}
	return false
}

var depFields = []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"}

// dependencies returns the dependency names of a manifest in dir (with alias
// targets), the member directories its local-path specifiers point to, and the
// local-path targets outside every member.
func (w *Workspace) dependencies(dir string, raw map[string]json.RawMessage) (names, dirs, outside []string, err error) {
	for _, f := range depFields {
		v, ok := raw[f]
		if !ok || string(v) == "null" {
			continue
		}
		var deps map[string]string
		if err := json.Unmarshal(v, &deps); err != nil {
			return nil, nil, nil, fmt.Errorf("%q is not an object of strings", f)
		}
		for name, spec := range deps {
			names = append(names, name)
			n, targets := specTargets(spec)
			if n != "" {
				names = append(names, n)
			}
			for _, tg := range targets {
				for _, base := range tg.bases(dir) {
					p, ok := pathx.Join(base, tg.path)
					if !ok {
						continue
					}
					if m := w.Innermost(p); m != nil {
						dirs = append(dirs, m.Dir)
					} else if tg.local {
						outside = append(outside, p)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names, dirs, outside, nil
}

// target is a local path in a dependency specifier.
type target struct {
	path string
	// local is set for file:/link:/portal: specifiers, whose target is a package.
	local bool
	// rootToo also tries the path relative to the repository root.
	rootToo bool
}

func (tg target) bases(dir string) []string {
	if tg.rootToo && dir != "" {
		return []string{dir, ""}
	}
	return []string{dir}
}

// specTargets reads a dependency specifier: the package name of an alias
// ("npm:@acme/ui@^1", "workspace:@acme/ui@*") and local paths ("file:../ui",
// "link:", "portal:", "workspace:../ui", "../ui", a yarn "patch:" file).
func specTargets(spec string) (string, []target) {
	spec = strings.TrimSpace(spec)
	switch {
	case strings.HasPrefix(spec, "npm:"):
		return aliasName(strings.TrimPrefix(spec, "npm:")), nil
	case strings.HasPrefix(spec, "file:"), strings.HasPrefix(spec, "link:"), strings.HasPrefix(spec, "portal:"):
		p := spec[strings.IndexByte(spec, ':')+1:]
		if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
			return "", nil
		}
		return "", []target{{path: p, local: true}}
	case strings.HasPrefix(spec, "workspace:"):
		rest := strings.TrimPrefix(spec, "workspace:")
		if rest == "" || strings.ContainsAny(rest[:1], "*^~<>=0123456789") {
			return "", nil
		}
		if strings.HasPrefix(rest, ".") {
			return "", []target{{path: rest, local: true, rootToo: true}}
		}
		// "workspace:@acme/ui@*" (an alias), or "workspace:packages/ui" (a path).
		return aliasName(rest), []target{{path: rest, rootToo: true}}
	case strings.HasPrefix(spec, "patch:"):
		// yarn patch:pkg@npm:1.0.0#./patches/x.patch: the patch is read on install.
		if i := strings.LastIndexByte(spec, '#'); i >= 0 {
			name := aliasName(strings.TrimPrefix(spec[:i], "patch:"))
			return name, nil
		}
		return "", nil
	case strings.HasPrefix(spec, "./"), strings.HasPrefix(spec, "../"), spec == "." || spec == "..":
		return "", []target{{path: spec, local: true}}
	}
	return "", nil
}

// patchFile returns the patch file of a yarn "patch:" specifier.
func patchFile(spec string) (string, bool) {
	if !strings.HasPrefix(spec, "patch:") {
		return "", false
	}
	i := strings.LastIndexByte(spec, '#')
	if i < 0 {
		return "", false
	}
	p := spec[i+1:]
	if j := strings.IndexByte(p, '&'); j >= 0 {
		p = p[:j]
	}
	p = strings.TrimPrefix(p, "~/")
	if strings.HasPrefix(p, "builtin<") || p == "" {
		return "", false
	}
	return p, true
}

// aliasName returns the package name of "name@range" or "@scope/name@range".
func aliasName(s string) string {
	s = strings.TrimSpace(s)
	start := 0
	if strings.HasPrefix(s, "@") {
		start = 1
	}
	if i := strings.IndexByte(s[start:], '@'); i >= 0 {
		s = s[:start+i]
	}
	if strings.ContainsAny(s, " :") {
		return ""
	}
	return s
}

// rootEdges reads overrides and resolutions that point at local paths: every
// member may then use the target.
func (w *Workspace) rootEdges(pw *pnpmWorkspace) error {
	var specs []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			specs = append(specs, x)
		case map[string]any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	for _, key := range []string{"overrides", "resolutions"} {
		if v, ok := w.rootRaw[key]; ok {
			var x any
			if err := json.Unmarshal(v, &x); err == nil {
				walk(x)
			}
		}
	}
	if v, ok := w.rootRaw["pnpm"]; ok {
		var p struct {
			Overrides map[string]string `json:"overrides"`
		}
		if err := json.Unmarshal(v, &p); err != nil {
			return fmt.Errorf("package.json: \"pnpm\" is not an object of the expected shape")
		}
		for _, s := range p.Overrides {
			specs = append(specs, s)
		}
	}
	for _, s := range pw.Overrides {
		specs = append(specs, s)
	}
	for _, s := range specs {
		_, targets := specTargets(s)
		for _, tg := range targets {
			p, ok := pathx.Join("", tg.path)
			if !ok {
				continue
			}
			if m := w.Innermost(p); m != nil {
				w.allDepDirs = append(w.allDepDirs, m.Dir)
			}
		}
	}
	return nil
}

func (w *Workspace) addGlobal(g globalPath) {
	for _, x := range w.globals {
		if x == g {
			return
		}
	}
	w.globals = append(w.globals, g)
}

// collectGlobals lists the root files that affect every member.
func (w *Workspace) collectGlobals(t tree.Tree, pw *pnpmWorkspace) {
	if w.HasRoot {
		w.addGlobal(globalPath{path: "package.json"})
	}
	for _, f := range rootConfigs {
		if _, ok := t.Lookup(f); ok {
			w.addGlobal(globalPath{path: f})
		}
	}
	if tree.HasDir(t, ".yarn") {
		w.addGlobal(globalPath{dir: true, path: ".yarn"})
	}
	for _, e := range t.Entries() {
		if !strings.Contains(e.Path, "/") && strings.HasPrefix(e.Path, "tsconfig") && strings.HasSuffix(e.Path, ".json") {
			w.addGlobal(globalPath{glob: "tsconfig*.json"})
			break
		}
	}
	// Patch files that the package manager applies on install.
	var patches []string
	if v, ok := w.rootRaw["pnpm"]; ok {
		var p struct {
			PatchedDependencies map[string]string `json:"patchedDependencies"`
		}
		if json.Unmarshal(v, &p) == nil {
			for _, f := range p.PatchedDependencies {
				patches = append(patches, f)
			}
		}
	}
	for _, f := range pw.PatchedDependencies {
		patches = append(patches, f)
	}
	for _, raw := range append([]map[string]json.RawMessage{w.rootRaw}, w.memberRaws()...) {
		for _, f := range append(depFields, "resolutions") {
			var deps map[string]string
			if v, ok := raw[f]; ok && json.Unmarshal(v, &deps) == nil {
				for _, spec := range deps {
					if p, ok := patchFile(spec); ok {
						patches = append(patches, p)
					}
				}
			}
		}
	}
	for _, f := range patches {
		if p, ok := pathx.Join("", f); ok && p != "" {
			w.addGlobal(globalPath{path: p})
		}
	}
}

func (w *Workspace) memberRaws() []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	for _, m := range w.Members {
		out = append(out, m.Raw)
	}
	return out
}
