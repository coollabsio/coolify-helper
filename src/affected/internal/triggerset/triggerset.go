// Package triggerset composes the limits of an application into its trigger
// set: the intersection of every limit that applies (build_context,
// dockerfile, compose, workspace), plus the files that always affect it.
package triggerset

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/cache"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/buildcontext"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/compose"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/dockerfile"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/dockerignore"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/workspace"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/all"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/js"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// maxDisplay caps the display list of a trigger set.
const maxDisplay = 100

// Workspace is the resolved workspace of one commit, shared by every application.
type Workspace struct {
	Graph     *graph.Graph
	Resolvers []resolver.Resolver
	// Reads are the paths the resolvers read, for the manifest fingerprint.
	Reads []string
	// Fingerprint covers Reads.
	Fingerprint string
	// CacheHit says the graph came from the cache, without fetching or parsing.
	CacheHit bool
	// Errors are sanitized messages for the response.
	Errors []string
	failed []graph.Widen

	inputsOnce sync.Once
	inputs     *sharedInputs
}

// Failed reports whether a detected resolver failed; the workspace limit then does not apply.
func (w *Workspace) Failed() bool { return len(w.failed) > 0 }

// prefetcher is a tree that can fetch many blobs in one round trip.
type prefetcher interface {
	Prefetch(ctx context.Context, paths []string) error
}

// appFiles are the files the limits of the applications will read.
func appFiles(apps []*api.Application) []string {
	var out []string
	for _, a := range apps {
		switch a.BuildPack {
		case api.BuildPackDockerfile:
			df := a.DockerfilePath()
			s, g := buildcontext.IgnoreFiles(a.BaseDirectory, df)
			out = append(out, df, s, g)
		case api.BuildPackDockerCompose:
			out = append(out, a.ComposePath())
		}
		out = append(out, a.ManagedConfigPaths...)
	}
	return out
}

// ResolveWorkspace resolves the workspace graph of a commit, from the cache
// when the manifests are unchanged. Every blob the resolution and the limits
// of the applications need is fetched in one round trip.
func ResolveWorkspace(ctx context.Context, t tree.Tree, apps []*api.Application, c *cache.Cache) *Workspace {
	ws := &Workspace{Graph: graph.New(), Resolvers: all.All()}
	var prefetch []string
	for _, e := range t.Entries() {
		for _, r := range ws.Resolvers {
			if r.Pathspecs(e.Path) {
				ws.Reads = append(ws.Reads, e.Path)
				// The not-modeled detector reads nothing: its files only change the fingerprint.
				if r.Name() != "not_modeled" {
					prefetch = append(prefetch, e.Path)
				}
				break
			}
		}
	}
	// Which lockfiles exist decides the package manager, and with it the members.
	var lockfiles []string
	for _, e := range t.Entries() {
		if _, ok := js.Lockfiles[path.Base(e.Path)]; ok {
			lockfiles = append(lockfiles, e.Path)
		}
	}
	ws.Fingerprint = cache.FingerprintWith(t, ws.Reads, lockfiles)
	if g, ok := c.Get(ws.Fingerprint); ok {
		ws.Graph, ws.CacheHit = g, true
		prefetch = nil
	}
	if p, ok := t.(prefetcher); ok {
		// A failed prefetch is not fatal: the reads fetch what is missing, or fail and widen.
		_ = p.Prefetch(ctx, append(prefetch, appFiles(apps)...))
	}
	if ws.CacheHit {
		return ws
	}
	for _, r := range ws.Resolvers {
		if ctx.Err() != nil {
			ws.fail(r.Name(), ctx.Err())
			return ws
		}
		if !r.Detect(t) {
			continue
		}
		g, err := r.Resolve(ctx, t)
		if err != nil {
			ws.fail(r.Name(), err)
			continue
		}
		ws.Graph.Merge(g)
	}
	ws.Graph.Normalize()
	if !ws.Failed() {
		_ = c.Put(ws.Fingerprint, ws.Graph)
	}
	return ws
}

func (w *Workspace) fail(name string, err error) {
	reason := err.Error()
	if err == tree.ErrBudget {
		reason = "the manifests are larger than max_manifest_files or max_manifest_bytes"
	}
	w.failed = append(w.failed, graph.Widen{Scope: "resolver:" + name, Reason: reason})
	w.Errors = append(w.Errors, fmt.Sprintf("resolver %s: %s", name, reason))
}

// appLimits are the limits of one application before they are combined.
type appLimits struct {
	bc, df, co, ws limits.Result
	anchor         workspace.Anchor
	wsWidened      []graph.Widen
	// contexts are the build contexts of the Dockerfile or Compose services.
	contexts []string
	// serviceFiles are the Dockerfiles of the Compose services, their ignore files
	// and the lowercase fallbacks.
	serviceFiles []string
}

func computeLimits(ctx context.Context, t tree.Tree, ws *Workspace, a *api.Application) *appLimits {
	l := &appLimits{}
	in := buildcontext.Input{BaseDir: a.BaseDirectory, BuildPack: a.BuildPack}
	anchorIn := workspace.AnchorInput{
		Graph:     ws.Graph,
		Resolvers: ws.Resolvers,
		BuildPack: a.BuildPack,
		BaseDir:   a.BaseDirectory,
		Commands:  a.Commands.List(),
	}
	switch a.BuildPack {
	case api.BuildPackDockerfile:
		in.DockerfilePath = a.DockerfilePath()
		out := dockerfile.Compile(ctx, t, dockerfile.Input{ContextDir: a.BaseDirectory, DockerfilePath: in.DockerfilePath})
		l.df = expandSymlinks(ctx, t, withParentLinks(t, out.Result), limits.Dockerfile)
		anchorIn.DockerfilePath = in.DockerfilePath
		anchorIn.RunCommands = out.RunCommands
	case api.BuildPackDockerCompose:
		out := compose.Compile(ctx, t, compose.Input{BaseDir: a.BaseDirectory, ComposePath: a.ComposePath()})
		l.co = expandSymlinks(ctx, t, withParentLinks(t, out.Result), limits.Compose)
		for _, s := range out.Services {
			anchorIn.Services = append(anchorIn.Services, workspace.ComposeService{Name: s.Name, ContextDir: s.ContextDir, DockerfilePath: s.DockerfilePath, RunCommands: s.RunCommands})
			l.contexts = append(l.contexts, s.ContextDir)
			if s.DockerfilePath != "" {
				l.serviceFiles = append(l.serviceFiles, dockerignore.AlwaysIncluded(s.ContextDir, s.DockerfilePath)...)
			}
		}
		defer func() {
			if out.Failed || l.co.Ruleset == nil {
				// Compose can read files outside the base directory (../shared): when the
				// Compose file can't be resolved, the base directory is not an upper bound.
				l.bc.Ruleset = nil
				l.bc.Widened = append(l.bc.Widened, graph.Widen{Scope: "limit:" + limits.BuildContext,
					Reason: "The Compose file could not be resolved, so files outside the base directory may matter."})
			} else if l.bc.Ruleset != nil {
				// Compose reads paths outside the base directory (../shared, symlink
				// targets): everything the compose limit includes is in the build context.
				for _, p := range out.OutsideBase {
					l.bc.Ruleset.Rules = append(l.bc.Ruleset.Rules, match.IncludeDir(p, &match.Origin{Rule: limits.Compose}))
				}
				for _, r := range l.co.Ruleset.Rules {
					p := r.Path
					if r.Kind == match.Glob {
						p = literalPrefix(r.Pattern)
					}
					if r.Action == match.Include && !pathx.Within(p, a.BaseDirectory) {
						r.Origin = &match.Origin{Rule: limits.Compose}
						l.bc.Ruleset.Rules = append(l.bc.Ruleset.Rules, r)
					}
				}
			}
		}()
	}
	l.bc = buildcontext.Compile(ctx, t, in)

	switch {
	case ws.Failed():
		l.wsWidened = append(l.wsWidened, graph.Widen{Scope: workspace.WidenScope, Reason: "A workspace resolver failed, see the resolver entries."})
	case len(ws.Graph.Packages) == 0 && len(ws.Graph.NotModeled) == 0:
		// No workspace: nothing to narrow.
	default:
		l.anchor = workspace.FindAnchor(anchorIn)
		l.ws = workspace.ExpandSymlinks(ctx, t, workspace.Compile(ws.Graph, l.anchor, a.ReferencePaths, a.ManagedConfigPaths))
	}
	return l
}

// expandSymlinks adds the targets of included symlinks to a Docker limit:
// BuildKit follows symlinks in COPY sources.
func expandSymlinks(ctx context.Context, t tree.Tree, r limits.Result, limit string) limits.Result {
	out := workspace.ExpandSymlinks(ctx, t, r)
	for i := range out.Widened {
		if out.Widened[i].Scope == workspace.WidenScope && i >= len(r.Widened) {
			out.Widened[i].Scope = "limit:" + limit
		}
	}
	return out
}

// withParentLinks makes the symlink expansion follow symlinks in the parent
// directories of the included paths too: COPY lib/foo, with lib a symlink to
// vendor/lib, copies vendor/lib/foo. It includes the symlink entries themselves,
// so that ExpandSymlinks adds their targets.
func withParentLinks(t tree.Tree, r limits.Result) limits.Result {
	if r.Ruleset == nil {
		return r
	}
	rs := *r.Ruleset
	rs.Rules = append([]match.Rule(nil), rs.Rules...)
	seen := map[string]bool{}
	for _, rule := range r.Ruleset.Rules {
		if rule.Action != match.Include {
			continue
		}
		p := rule.Path
		if rule.Kind == match.Glob {
			p = literalPrefix(rule.Pattern)
		}
		for q := pathx.Dir(p); q != ""; q = pathx.Dir(q) {
			if e, ok := t.Lookup(q); ok && e.IsSymlink() && !seen[q] {
				seen[q] = true
				rs.Rules = append(rs.Rules, match.IncludeFile(q, rule.Origin))
			}
		}
		if rule.Kind == match.Glob && p != "" {
			if e, ok := t.Lookup(p); ok && e.IsSymlink() && !seen[p] {
				seen[p] = true
				rs.Rules = append(rs.Rules, match.IncludeFile(p, rule.Origin))
			}
		}
	}
	r.Ruleset = &rs
	return r
}

// inputPatterns is the set of paths whose change can alter the resolution of
// the trigger set: what the resolvers and limits read, what they would read if
// it were added (every resolver ran its detection), the tracked symlinks, the
// base directory itself, and paths with a newline (they drop the .dockerignore
// rules). When a glob can't be expressed, the set matches everything.
func inputPatterns(t tree.Tree, ws *Workspace, a *api.Application, l *appLimits, reads []string) match.Set {
	shared := ws.sharedInputs(t)
	if shared.invalid {
		return match.Set{Version: match.SetVersion, AllOf: []match.Ruleset{}, Lockfiles: []string{}}
	}
	rs := match.Ruleset{Limit: "inputs", Default: match.Exclude, Rules: append([]match.Rule{}, shared.rules...)}

	// The shared rules cover the resolver reads and the symlinks; only the
	// application's own files are left to check against the globs.
	var files []string
	for _, f := range reads[len(ws.Reads):] {
		files = append(files, f)
	}
	switch a.BuildPack {
	case api.BuildPackDockerfile:
		files = append(files, dockerignore.AlwaysIncluded(a.BaseDirectory, a.DockerfilePath())...)
	case api.BuildPackDockerCompose:
		files = append(files, a.ComposePath())
	}
	files = append(files, l.serviceFiles...)
	for p := a.BaseDirectory; p != ""; p = pathx.Dir(p) {
		files = append(files, p)
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		if f == "" || seen[f] || shared.files[f] {
			continue
		}
		seen[f] = true
		if !shared.covered(f) {
			rs.Rules = append(rs.Rules, match.IncludeFile(f, nil))
		}
	}
	return match.Set{Version: match.SetVersion, AllOf: []match.Ruleset{rs}, Lockfiles: []string{}}
}

// sharedInputs are the input rules every application of a workspace shares:
// the globs of every resolver, the files the resolvers read that no glob
// covers, and the tracked symlinks.
type sharedInputs struct {
	invalid bool
	globs   []string
	rules   []match.Rule
	files   map[string]bool
}

func (s *sharedInputs) covered(f string) bool {
	for _, g := range s.globs {
		if match.GlobMatch(g, f) {
			return true
		}
	}
	return false
}

func (w *Workspace) sharedInputs(t tree.Tree) *sharedInputs {
	w.inputsOnce.Do(func() {
		s := &sharedInputs{files: map[string]bool{}}
		w.inputs = s
		seen := map[string]bool{}
		add := func(g string) {
			if !seen[g] {
				seen[g] = true
				s.globs = append(s.globs, g)
			}
		}
		for _, r := range w.Resolvers {
			for _, g := range r.InputPatterns() {
				add(g)
			}
		}
		// Paths with a newline drop the .dockerignore rules.
		add("**/*\n*")
		add("**/*\n*/**")
		for _, g := range s.globs {
			if match.ValidateGlob(g) != nil {
				s.invalid = true
				return
			}
			s.rules = append(s.rules, match.IncludeGlob(g, nil))
		}
		files := append([]string(nil), w.Reads...)
		for _, e := range t.Entries() {
			if e.IsSymlink() {
				files = append(files, e.Path)
			}
		}
		sort.Strings(files)
		for _, f := range files {
			if s.files[f] {
				continue
			}
			s.files[f] = true
			if !s.covered(f) {
				s.rules = append(s.rules, match.IncludeFile(f, nil))
			}
		}
	})
	return w.inputs
}

// withDockerIncludes re-includes, at the end of the workspace ruleset, the
// paths a Dockerfile or Compose file names explicitly (COPY sources, bind
// mounts, env files): the build reads them, whatever the workspace says, so a
// COPY CHANGELOG.md or COPY packages/other must not be excluded as a meta file
// or a member outside the closure. Whole build contexts are not re-included:
// they mean "the limit couldn't narrow", not "the build reads all of it".
func withDockerIncludes(ws *match.Ruleset, contexts []string, docker ...*match.Ruleset) *match.Ruleset {
	if ws == nil {
		return nil
	}
	out := *ws
	out.Rules = append([]match.Rule(nil), ws.Rules...)
	isContext := map[string]bool{}
	for _, c := range contexts {
		isContext[c] = true
	}
	for _, rs := range docker {
		if rs == nil {
			continue
		}
		for _, r := range rs.Rules {
			if r.Action != match.Include || (r.Kind == match.Dir && isContext[r.Path]) {
				continue
			}
			r.Origin = &match.Origin{Rule: rs.Limit}
			out.Rules = append(out.Rules, r)
		}
	}
	return &out
}

// literalPrefix returns the directory prefix of a glob before its first wildcard segment.
func literalPrefix(pattern string) string {
	var lit []string
	for _, seg := range strings.Split(pattern, "/") {
		if strings.ContainsAny(seg, "*?") {
			break
		}
		lit = append(lit, seg)
	}
	return strings.Join(lit, "/")
}

// FindAnchor returns the packages an application builds.
func FindAnchor(ctx context.Context, t tree.Tree, ws *Workspace, a *api.Application) (workspace.Anchor, error) {
	if ws.Failed() {
		return workspace.Anchor{}, fmt.Errorf("a workspace resolver failed")
	}
	return computeLimits(ctx, t, ws, a).anchor, nil
}

// Resolved is the trigger set of an application, ready to match.
type Resolved struct {
	TriggerSet api.TriggerSet
	Matcher    *match.Matcher
	// ResolutionFailed says only the build context narrows the set, because the
	// other limits widened.
	ResolutionFailed bool
	lockfiles        map[string]bool
}

// Build resolves the trigger set of an application.
func Build(ctx context.Context, t tree.Tree, ws *Workspace, a *api.Application) (*Resolved, error) {
	l := computeLimits(ctx, t, ws, a)
	ts := api.TriggerSet{Display: []string{}, Limits: []string{}, Resolvers: append([]string{}, ws.Graph.Resolvers...), Anchors: []api.Anchor{}, Widened: []graph.Widen{}}
	var sets []match.Ruleset
	reads := append([]string(nil), ws.Reads...)
	if a.BuildPack == api.BuildPackDockerfile {
		l.contexts = append(l.contexts, a.BaseDirectory)
	}
	l.ws.Ruleset = withDockerIncludes(l.ws.Ruleset, l.contexts, l.df.Ruleset, l.co.Ruleset)
	for _, r := range []limits.Result{l.bc, l.df, l.co, l.ws} {
		ts.Widened = append(ts.Widened, r.Widened...)
		reads = append(reads, r.Reads...)
		if r.Ruleset != nil {
			rs := *r.Ruleset
			rs.Rules = append([]match.Rule{}, rs.Rules...)
			sets = append(sets, rs)
		}
	}
	ts.Widened = append(ts.Widened, l.wsWidened...)
	ts.Widened = append(ts.Widened, ws.failed...)
	ts.Widened = append(ts.Widened, ws.Graph.Widened...)

	// Managed config files always affect the application: every ruleset includes them.
	for i := range sets {
		for _, p := range a.ManagedConfigPaths {
			sets[i].Rules = append(sets[i].Rules, match.IncludeFile(p, &match.Origin{Rule: "managed_config"}))
		}
		ts.Limits = append(ts.Limits, sets[i].Limit)
	}
	reads = append(reads, a.ManagedConfigPaths...)

	ts.InputPatterns = inputPatterns(t, ws, a, l, reads)
	ts.Compiled = match.Set{Version: match.SetVersion, AllOf: sets, Lockfiles: append([]string{}, ws.Graph.Lockfiles...)}
	if ts.Compiled.AllOf == nil {
		ts.Compiled.AllOf = []match.Ruleset{}
	}
	m, err := match.Compile(ts.Compiled)
	if err != nil {
		return nil, fmt.Errorf("compile the trigger set: %w", err)
	}

	for i, d := range l.anchor.Dirs {
		an := api.Anchor{Resolver: l.anchor.Resolver, Dir: d, Source: l.anchor.Source}
		if i < len(l.anchor.Packages) {
			an.Package = l.anchor.Packages[i]
		}
		ts.Anchors = append(ts.Anchors, an)
	}
	if len(ts.Anchors) > 0 {
		first := ts.Anchors[0]
		ts.Anchor = &first
	}
	seen := map[string]bool{}
	for _, rs := range sets {
		for _, s := range workspace.Display(&rs) {
			if seen[rs.Limit+s] {
				continue
			}
			seen[rs.Limit+s] = true
			if len(ts.Display) == maxDisplay {
				ts.Display = append(ts.Display, "…")
				break
			}
			ts.Display = append(ts.Display, rs.Limit+": "+s)
		}
		if len(ts.Display) > maxDisplay {
			break
		}
	}
	ts.ManifestFingerprint = cache.Fingerprint(t, reads)

	res := &Resolved{TriggerSet: ts, Matcher: m, lockfiles: map[string]bool{}}
	for _, lf := range ts.Compiled.Lockfiles {
		res.lockfiles[lf] = true
	}
	if len(ts.Widened) > 0 {
		res.ResolutionFailed = true
		for _, rs := range sets {
			if rs.Limit != limits.BuildContext {
				res.ResolutionFailed = false
			}
		}
	}
	return res, nil
}

// limitPriority orders the limits when explaining a match: the most specific first.
var limitPriority = map[string]int{limits.Workspace: 0, limits.Dockerfile: 1, limits.Compose: 2, limits.BuildContext: 3}

// Explain says why a file is in the set.
func (r *Resolved) Explain(file string) api.Match {
	if r.lockfiles[file] {
		return api.Match{File: file, Rule: "lockfile"}
	}
	_, decisions := r.Matcher.Explain(file)
	sort.SliceStable(decisions, func(i, j int) bool { return limitPriority[decisions[i].Limit] < limitPriority[decisions[j].Limit] })
	for _, d := range decisions {
		if d.Rule == nil || !d.Included {
			continue
		}
		if d.Rule.Origin != nil && d.Rule.Origin.Rule != "" {
			return api.Match{File: file, Rule: d.Rule.Origin.Rule, Via: append([]string(nil), d.Rule.Origin.Via...)}
		}
		return api.Match{File: file, Rule: d.Limit}
	}
	for _, d := range decisions {
		if d.Limit == limits.Workspace && d.Included {
			return api.Match{File: file, Rule: "global"}
		}
	}
	for _, d := range decisions {
		if d.Included {
			return api.Match{File: file, Rule: d.Limit}
		}
	}
	return api.Match{File: file, Rule: limits.BuildContext}
}
