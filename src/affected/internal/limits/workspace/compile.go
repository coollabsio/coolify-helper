package workspace

import (
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
)

// WidenScope is the scope of the limit's widenings.
const WidenScope = "limit:" + limits.Workspace

// Rule names of origins.
const (
	RuleWorkspace     = "workspace"
	RuleGlobal        = "global"
	RuleLockfile      = "lockfile"
	RuleReference     = "reference"
	RuleManagedConfig = "managed_config"
)

// metaGlobs are root-level files that never affect a build (§7.4 step 3).
var metaGlobs = []string{"README*", "LICENSE*", "CHANGELOG*", "CONTRIBUTING*", "CODE_OF_CONDUCT*", "SECURITY*"}

// metaDirs are root-level directories that never affect a build.
var metaDirs = []string{".github", ".gitlab", ".vscode", ".idea", ".husky", ".changeset"}

// metaFiles are root-level files that never affect a build.
var metaFiles = []string{".gitlab-ci.yml", ".editorconfig", "CODEOWNERS"}

// Compile builds the workspace ruleset of an application. The ruleset defaults
// to include (DefaultOrigin "global": files outside every package affect every
// application), and its rules are, in order (the last matching rule wins):
//
//	(a) one rule per member directory, shallow first: include when the member is
//	    in the anchor's closure (origin "workspace" with the "via" chain), else
//	    exclude. The root member never gets a rule.
//	(b) the root-level meta excludes (README*, LICENSE*, …, .github/, …,
//	    CODEOWNERS); never "*.md" in general.
//	(b') the rules of (a) again for the members that a meta exclude hits (a
//	    member under .github/), so that they keep their decision.
//	(c) re-includes: every global (origin "global", lockfiles "lockfile"), every
//	    member manifest, every Lifecycle member directory (global), every
//	    reference path (origin "reference", dir) and every managed config path
//	    (origin "managed_config", file).
//
// The limit does not apply (nil Ruleset) when the graph has a build system it
// does not model, when there is no anchor, or when an anchor is not a member:
// those widen, with the reason. A graph without members has nothing to narrow:
// the limit is not relevant, so it returns a nil Ruleset without a widening.
func Compile(g *graph.Graph, a Anchor, referencePaths, managedConfigPaths []string) limits.Result {
	if g != nil && len(g.NotModeled) > 0 {
		return widen(notModeledReason(g.NotModeled))
	}
	if g == nil || !hasMembers(g) {
		return limits.Result{}
	}
	if !a.Found() {
		reason := a.Reason
		if reason == "" {
			reason = NoAnchor + "."
		}
		return widen(reason)
	}
	for _, d := range a.Dirs {
		if g.PackageAt(d) == nil {
			return widen(NoAnchor + ": the anchor " + pathx.Display(d) + " is not a workspace package.")
		}
	}
	closure := g.Closure(a.Dirs)

	members := make([]*graph.Package, 0, len(g.Packages))
	for i := range g.Packages {
		if g.Packages[i].Dir != "" {
			members = append(members, &g.Packages[i])
		}
	}
	sort.SliceStable(members, func(i, j int) bool {
		di, dj := depth(members[i].Dir), depth(members[j].Dir)
		if di != dj {
			return di < dj
		}
		return members[i].Dir < members[j].Dir
	})
	memberRule := func(p *graph.Package) match.Rule {
		if via, ok := closure[p.Dir]; ok {
			return match.IncludeDir(p.Dir, &match.Origin{Rule: RuleWorkspace, Via: via})
		}
		return match.ExcludeDir(p.Dir, &match.Origin{Rule: RuleWorkspace})
	}

	rs := &match.Ruleset{
		Limit:         limits.Workspace,
		Default:       match.Include,
		DefaultOrigin: &match.Origin{Rule: RuleGlobal},
	}
	// (a)
	for _, p := range members {
		rs.Rules = append(rs.Rules, memberRule(p))
	}
	// (b)
	meta := &match.Origin{Rule: RuleWorkspace}
	for _, gl := range metaGlobs {
		rs.Rules = append(rs.Rules, match.ExcludeGlob(gl, meta))
	}
	for _, d := range metaDirs {
		rs.Rules = append(rs.Rules, match.ExcludeDir(d, meta))
	}
	for _, f := range metaFiles {
		rs.Rules = append(rs.Rules, match.ExcludeFile(f, meta))
	}
	// (b')
	for _, p := range members {
		if hitByMeta(p.Dir) {
			rs.Rules = append(rs.Rules, memberRule(p))
		}
	}
	// (c)
	seen := map[string]bool{}
	add := func(r match.Rule) {
		key := string(r.Action) + "\x00" + string(r.Kind) + "\x00" + r.Path + "\x00" + r.Pattern
		if seen[key] {
			return
		}
		seen[key] = true
		rs.Rules = append(rs.Rules, r)
	}
	lockfiles := map[string]bool{}
	for _, l := range g.Lockfiles {
		lockfiles[l] = true
	}
	for _, gl := range g.Globals {
		origin := &match.Origin{Rule: RuleGlobal}
		switch gl.Kind {
		case match.Glob:
			add(match.IncludeGlob(gl.Pattern, origin))
		case match.Dir:
			if gl.Path == "" {
				return widen("A global covers the whole repository.")
			}
			add(match.IncludeDir(gl.Path, origin))
		default:
			if lockfiles[gl.Path] {
				origin = &match.Origin{Rule: RuleLockfile}
			}
			add(match.IncludeFile(gl.Path, origin))
		}
	}
	for _, p := range members {
		for _, mf := range p.Manifests {
			add(match.IncludeFile(mf, &match.Origin{Rule: RuleGlobal}))
		}
		if p.Lifecycle {
			add(match.IncludeDir(p.Dir, &match.Origin{Rule: RuleGlobal}))
		}
	}
	for _, rp := range referencePaths {
		c, ok := pathx.Clean(rp)
		if !ok {
			continue
		}
		if c == "" {
			return widen("A reference path is the repository root.")
		}
		add(match.IncludeDir(c, &match.Origin{Rule: RuleReference}))
	}
	for _, mp := range managedConfigPaths {
		c, ok := pathx.Clean(mp)
		if !ok || c == "" {
			continue
		}
		add(match.IncludeFile(c, &match.Origin{Rule: RuleManagedConfig}))
	}
	return limits.Result{Ruleset: rs}
}

func widen(reason string) limits.Result {
	return limits.Result{Widened: []graph.Widen{{Scope: WidenScope, Reason: reason}}}
}

func depth(dir string) int { return strings.Count(dir, "/") }

// hitByMeta reports whether a meta exclude matches a member directory or what
// is under it.
func hitByMeta(dir string) bool {
	top := dir
	if i := strings.IndexByte(dir, '/'); i >= 0 {
		top = dir[:i]
	}
	for _, d := range metaDirs {
		if top == d {
			return true
		}
	}
	if top != dir {
		return false
	}
	for _, f := range metaFiles {
		if dir == f {
			return true
		}
	}
	for _, gl := range metaGlobs {
		if strings.HasPrefix(dir, strings.TrimSuffix(gl, "*")) {
			return true
		}
	}
	return false
}

// Display describes a ruleset for people: "apps/web/**" for an included
// directory, "!apps/api/**" for an excluded one, files and globs as they are.
func Display(rs *match.Ruleset) []string {
	if rs == nil {
		return nil
	}
	var out []string
	for _, r := range rs.Rules {
		s := r.Path
		switch r.Kind {
		case match.Dir:
			s = r.Path + "/**"
		case match.Glob:
			s = r.Pattern
		}
		if r.Action == match.Exclude {
			s = "!" + s
		}
		out = append(out, s)
	}
	return out
}
