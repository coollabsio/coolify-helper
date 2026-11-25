// Package js resolves npm, yarn (classic and Berry), pnpm and bun workspaces.
//
// Members come from pnpm-workspace.yaml "packages" or the root package.json
// "workspaces" (a list or {packages}), with "!" exclusions: a member is a
// directory that a pattern selects and that has a package.json, outside
// node_modules. A member that declares its own "workspaces" (a yarn Berry
// nested worktree) adds its nested members, with an edge from it to them, since
// the nested directories belonged to it.
//
// Edges: every dependencies/devDependencies/peerDependencies/
// optionalDependencies entry is a dependency name (graph edges are the names of
// members), plus the target name of an "npm:"/"workspace:" alias and the member
// that a local path ("file:", "link:", "portal:", "workspace:./x", "../x")
// points into. A local path outside every member is followed one manifest
// further, so that its own dependencies on members are not lost. Overrides and
// resolutions to local paths are edges from every member.
//
// Globals: the root package.json, the lockfiles, the package manager and
// toolchain configuration files at the root, .yarn/, root tsconfig*.json and
// patch files. Every member's package.json (and its .npmrc, .yarnrc.yml,
// .yarnrc, bunfig.toml) is a member manifest. A member with install-time
// lifecycle scripts is Lifecycle (its directory is global).
//
// Widening: an unparseable root or member manifest, an unparseable
// pnpm-workspace.yaml, a pattern with syntax this package doesn't model, an
// ambiguous choice between pnpm-workspace.yaml and package.json workspaces, and
// a read over the budget all fail Resolve, which drops the workspace limit. A
// member whose manifest can't be parsed has no known name, so any member could
// depend on it: no narrower answer is safe.
package js

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Resolver is the npm/yarn/pnpm/bun workspace resolver.
type Resolver struct{}

// New returns the resolver.
func New() *Resolver { return &Resolver{} }

var _ resolver.Resolver = (*Resolver)(nil)

// Name implements resolver.Resolver.
func (*Resolver) Name() string { return "js" }

// pathspecNames are file names the resolver reads or probes at any depth.
var pathspecNames = map[string]bool{"package.json": true, ".npmrc": true, ".yarnrc.yml": true, ".yarnrc": true, "bunfig.toml": true}

// Pathspecs implements resolver.Resolver: every package.json and per-directory
// package manager configuration file, and the root configuration files. It
// leaves lockfiles out: they are not read in this phase.
func (*Resolver) Pathspecs(p string) bool {
	if skipPath(p) {
		return false
	}
	if pathspecNames[path.Base(p)] {
		return true
	}
	if !strings.Contains(p, "/") {
		for _, f := range rootConfigs {
			if p == f {
				return true
			}
		}
	}
	return false
}

// Detect implements resolver.Resolver.
func (*Resolver) Detect(t tree.Tree) bool {
	if _, ok := t.Lookup("package.json"); ok {
		return true
	}
	_, ok := t.Lookup("pnpm-workspace.yaml")
	return ok
}

// Resolve implements resolver.Resolver.
func (*Resolver) Resolve(ctx context.Context, t tree.Tree) (*graph.Graph, error) {
	w, err := Load(ctx, t)
	if err != nil {
		return nil, err
	}
	g := graph.New()
	g.Resolvers = append(g.Resolvers, w.Managers...)
	for _, m := range w.Members {
		p := graph.Package{
			Dir:       m.Dir,
			Resolvers: w.Managers,
			Manifests: []string{m.Manifest},
			Deps:      m.deps,
			DepDirs:   append(append([]string(nil), m.depDirs...), w.allDepDirs...),
			Lifecycle: m.lifecycle,
		}
		if m.Name != "" {
			p.Names = []string{m.Name}
		}
		for _, f := range memberConfigs {
			if _, ok := t.Lookup(m.Dir + "/" + f); ok {
				p.Manifests = append(p.Manifests, m.Dir+"/"+f)
			}
		}
		g.AddPackage(p)
	}
	source := strings.Join(w.Managers, "/")
	for _, gl := range w.globals {
		switch {
		case gl.glob != "":
			g.AddGlobal(match.Glob, gl.glob, source)
		case gl.dir:
			g.AddGlobal(match.Dir, gl.path, source)
		default:
			g.AddGlobal(match.File, gl.path, source)
		}
	}
	for _, lf := range w.lockfiles {
		g.AddLockfile(lf, Lockfiles[lf])
	}
	return g, nil
}

// installCommands are install-like subcommands: they build nothing in particular.
var installCommands = map[string]bool{
	"install": true, "i": true, "ci": true, "clean-install": true, "ic": true, "install-clean": true, "isntall": true, "in": true,
	"add": true, "a": true, "fetch": true, "remove": true, "rm": true, "uninstall": true, "un": true, "r": true,
	"update": true, "up": true, "upgrade": true, "dedupe": true, "ddp": true, "prune": true, "rebuild": true, "rb": true,
	"link": true, "ln": true, "unlink": true, "import": true, "unplug": true,
}

// neutralCommands are other subcommands that build nothing.
var neutralCommands = map[string]bool{
	"audit": true, "outdated": true, "ls": true, "list": true, "la": true, "ll": true, "why": true, "explain": true,
	"config": true, "c": true, "set": true, "get": true, "cache": true, "store": true, "env": true, "setup": true,
	"help": true, "-h": true, "--help": true, "-v": true, "--version": true, "version": true, "versions": true,
	"bin": true, "root": true, "prefix": true, "doctor": true, "licenses": true, "info": true, "view": true, "show": true, "v": true,
	"init": true, "create": true, "login": true, "logout": true, "whoami": true, "ping": true, "search": true,
	"publish": true, "pkg": true, "npm": true, "plugin": true, "constraints": true, "patch": true, "patch-commit": true,
	"patch-remove": true, "approve-builds": true, "ignored-builds": true, "self-update": true, "server": true,
	"completion": true, "completions": true, "dlx": true, "check": true, "autoclean": true, "policies": true,
	"owner": true, "tag": true, "team": true, "dist-tag": true, "fund": true, "sbom": true, "query": true, "pm": true,
	"global": true, "generate-lock-entry": true, "cat-file": true, "cat-index": true, "find-hash": true, "upgrade-interactive": true,
}

// AnchorFromCommand implements resolver.Resolver for pnpm, npm, yarn and bun.
func (*Resolver) AnchorFromCommand(cmd command.Command, g *graph.Graph) resolver.Anchor {
	if len(cmd.Argv) == 0 {
		return resolver.Anchor{Kind: resolver.NotMine}
	}
	var a resolver.Anchor
	switch cmd.Argv[0] {
	case "pnpm":
		a = pnpmAnchor(cmd, g)
	case "npm":
		a = npmAnchor(cmd, g)
	case "yarn":
		a = yarnAnchor(cmd, g)
	case "bun":
		a = bunAnchor(cmd, g)
	case "npx", "pnpx", "bunx":
		// Left over after unwrapping: nothing to run.
		a = resolver.Anchor{Kind: resolver.Neutral}
	default:
		return resolver.Anchor{Kind: resolver.NotMine}
	}
	a.Tool = cmd.Argv[0]
	return a
}

// cwd is a command's working directory while its options are read.
type cwd struct {
	dir   string
	known bool
}

func (c *cwd) cd(p string) {
	if !c.known || p == "" || strings.ContainsAny(p, "$~`") || strings.HasPrefix(p, "/") {
		c.dir, c.known = "", false
		return
	}
	d, ok := pathx.Join(c.dir, p)
	if !ok {
		c.dir, c.known = "", false
		return
	}
	c.dir = d
}

func (c cwd) bases() []string {
	if !c.known {
		return nil
	}
	return []string{c.dir}
}

func unknown(format string, args ...any) resolver.Anchor {
	return resolver.Anchor{Kind: resolver.Unknown, Reason: fmt.Sprintf(format, args...)}
}

// scriptRun anchors an unfiltered script run to the member at the directory.
func scriptRun(tool string, c cwd, g *graph.Graph, what string) resolver.Anchor {
	if !c.known {
		return unknown("%q runs in a directory the tool can't follow", what)
	}
	if p := g.PackageAt(c.dir); p != nil && c.dir != "" && hasManifest(p, c.dir+"/package.json") {
		return resolver.Anchor{Kind: resolver.Anchored, Dirs: []string{p.Dir}}
	}
	if c.dir == "" {
		return unknown("%q runs a root script, which can build anything", what)
	}
	return unknown("%q runs in %s, which is not a workspace package", what, c.dir)
}

func hasManifest(p *graph.Package, mf string) bool {
	for _, m := range p.Manifests {
		if m == mf {
			return true
		}
	}
	return false
}

// selectFilters resolves filters into an anchor.
func selectFilters(filters []string, bases []string, g *graph.Graph) resolver.Anchor {
	var dirs []string
	for _, f := range filters {
		s := SelectFilter(f, bases, g)
		if s.Reason != "" {
			return unknown("%s", s.Reason)
		}
		dirs = append(dirs, s.Dirs...)
	}
	return resolver.Anchor{Kind: resolver.Anchored, Dirs: uniq(dirs)}
}

func display(argv []string) string {
	s := strings.Join(argv, " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// splitOpt splits "--opt=value".
func splitOpt(a string) (string, string, bool) {
	if strings.HasPrefix(a, "-") {
		if i := strings.IndexByte(a, '='); i > 0 {
			return a[:i], a[i+1:], true
		}
	}
	return a, "", false
}

// pnpmValued are pnpm options that take a separate value.
var pnpmValued = map[string]bool{
	"--workspace-concurrency": true, "--reporter": true, "--loglevel": true, "--store-dir": true, "--virtual-store-dir": true,
	"--modules-dir": true, "--lockfile-dir": true, "--changed-files-ignore-pattern": true, "--test-pattern": true,
	"--resume-from": true, "--node-linker": true, "--registry": true, "--config-dir": true, "--global-dir": true,
	"--package-import-method": true, "--network-concurrency": true, "--child-concurrency": true, "--cpu": true, "--os": true, "--libc": true,
}

func pnpmAnchor(cmd command.Command, g *graph.Graph) resolver.Anchor {
	c := cwd{dir: cmd.Dir, known: cmd.DirKnown}
	var filters []string
	var positional []string
	recursive, wsRoot := false, false
	args := cmd.Argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := splitOpt(a)
		switch {
		case name == "--filter" || name == "-F" || name == "--filter-prod":
			if hasVal {
				filters = append(filters, val)
			} else if i+1 < len(args) {
				i++
				filters = append(filters, args[i])
			}
		case strings.HasPrefix(a, "-F") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			filters = append(filters, a[2:])
		case name == "-C" || name == "--dir":
			if hasVal {
				c.cd(val)
			} else if i+1 < len(args) {
				i++
				c.cd(args[i])
			}
		case name == "-r" || name == "--recursive":
			recursive = true
		case name == "-w" || name == "--workspace-root":
			wsRoot = true
		case pnpmValued[name] && !hasVal:
			i++
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
	case "recursive", "m", "multi":
		recursive = true
		positional = positional[1:]
		sub = ""
		if len(positional) > 0 {
			sub = positional[0]
		}
	}
	if sub == "" || installCommands[sub] || neutralCommands[sub] {
		return resolver.Anchor{Kind: resolver.Neutral}
	}
	if len(filters) > 0 {
		return selectFilters(filters, c.bases(), g)
	}
	if recursive {
		return unknown("%q runs in every package", display(cmd.Argv))
	}
	if wsRoot {
		return unknown("%q runs a root script, which can build anything", display(cmd.Argv))
	}
	return scriptRun("pnpm", c, g, display(cmd.Argv))
}

// npmValued are npm options that take a separate value.
var npmValued = map[string]bool{
	"--loglevel": true, "--registry": true, "--cache": true, "--userconfig": true, "--globalconfig": true, "--tag": true,
	"--omit": true, "--include": true, "--install-strategy": true, "--script-shell": true, "--save-prefix": true,
	"--before": true, "--otp": true, "--access": true, "--auth-type": true,
}

// npmScripts are npm subcommands that run package scripts or binaries.
var npmScripts = map[string]bool{
	"run": true, "run-script": true, "rum": true, "urn": true, "test": true, "tst": true, "t": true,
	"start": true, "stop": true, "restart": true, "exec": true, "x": true, "pack": true,
	"install-test": true, "it": true, "install-ci-test": true, "cit": true, "explore": true,
}

func npmAnchor(cmd command.Command, g *graph.Graph) resolver.Anchor {
	c := cwd{dir: cmd.Dir, known: cmd.DirKnown}
	var workspaces []string
	all := false
	var positional []string
	args := cmd.Argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := splitOpt(a)
		switch {
		case name == "-w" || name == "--workspace":
			if hasVal {
				workspaces = append(workspaces, val)
			} else if i+1 < len(args) {
				i++
				workspaces = append(workspaces, args[i])
			}
		case name == "--workspaces" || name == "-ws" || name == "--ws":
			all = !hasVal || val == "true"
		case name == "--prefix" || name == "-C":
			if hasVal {
				c.cd(val)
			} else if i+1 < len(args) {
				i++
				c.cd(args[i])
			}
		case npmValued[name] && !hasVal:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			positional = append(positional, a)
		}
	}
	sub := ""
	if len(positional) > 0 {
		sub = positional[0]
	}
	switch {
	case sub == "" || installCommands[sub] || neutralCommands[sub]:
		return resolver.Anchor{Kind: resolver.Neutral}
	case !npmScripts[sub]:
		return unknown("%q is an npm command the tool does not model", display(cmd.Argv))
	}
	if len(workspaces) > 0 {
		var dirs []string
		for _, w := range workspaces {
			d, reason := npmWorkspace(w, c, g)
			if reason != "" {
				return unknown("%s", reason)
			}
			dirs = append(dirs, d...)
		}
		return resolver.Anchor{Kind: resolver.Anchored, Dirs: uniq(dirs)}
	}
	if all {
		return unknown("%q runs in every workspace", display(cmd.Argv))
	}
	return scriptRun("npm", c, g, display(cmd.Argv))
}

// npmWorkspace resolves an npm --workspace value: a name, a member directory,
// or a parent directory of members.
func npmWorkspace(w string, c cwd, g *graph.Graph) ([]string, string) {
	if strings.Contains(w, "$") {
		return nil, fmt.Sprintf("the workspace %q has a variable", w)
	}
	var dirs []string
	for _, p := range g.ByName(w) {
		dirs = append(dirs, p.Dir)
	}
	if len(dirs) > 0 {
		return dirs, ""
	}
	if strings.HasPrefix(w, "/") {
		return nil, fmt.Sprintf("the workspace %q is an absolute path", w)
	}
	bases := []string{""}
	if c.known && c.dir != "" {
		bases = append(bases, c.dir)
	}
	for _, base := range bases {
		joined, ok := pathx.Join(base, w)
		if !ok {
			continue
		}
		for i := range g.Packages {
			if d := g.Packages[i].Dir; d != "" && pathx.Within(d, joined) {
				dirs = append(dirs, d)
			}
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Sprintf("no package matches the workspace %q", w)
	}
	return dirs, ""
}

// yarnValued are yarn options that take a separate value.
var yarnValued = map[string]bool{
	"--modules-folder": true, "--cache-folder": true, "--mutex": true, "--network-timeout": true, "--link-folder": true,
	"--global-folder": true, "--registry": true, "--network-concurrency": true,
}

func yarnAnchor(cmd command.Command, g *graph.Graph) resolver.Anchor {
	c := cwd{dir: cmd.Dir, known: cmd.DirKnown}
	var positional []string
	args := cmd.Argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := splitOpt(a)
		switch {
		case name == "--cwd":
			if hasVal {
				c.cd(val)
			} else if i+1 < len(args) {
				i++
				c.cd(args[i])
			}
		case yarnValued[name] && !hasVal:
			i++
		case strings.HasPrefix(a, "-") && len(positional) == 0:
		default:
			positional = append(positional, a)
		}
		if len(positional) > 0 && (positional[0] == "workspace" || positional[0] == "workspaces") && len(positional) >= 2 {
			break
		}
	}
	sub := ""
	if len(positional) > 0 {
		sub = positional[0]
	}
	switch {
	case sub == "" || installCommands[sub] || neutralCommands[sub]:
		// A bare "yarn" installs.
		return resolver.Anchor{Kind: resolver.Neutral}
	case sub == "workspace":
		if len(positional) < 2 {
			return unknown("%q names no workspace", display(cmd.Argv))
		}
		name := positional[1]
		if strings.Contains(name, "$") {
			return unknown("the workspace %q has a variable", name)
		}
		var dirs []string
		for _, p := range g.ByName(name) {
			dirs = append(dirs, p.Dir)
		}
		if len(dirs) == 0 {
			return unknown("no package is named %q", name)
		}
		return resolver.Anchor{Kind: resolver.Anchored, Dirs: uniq(dirs)}
	case sub == "workspaces":
		next := ""
		if len(positional) > 1 {
			next = positional[1]
		}
		switch next {
		case "focus", "info", "list":
			return resolver.Anchor{Kind: resolver.Neutral}
		}
		return unknown("%q runs in several workspaces", display(cmd.Argv))
	}
	return scriptRun("yarn", c, g, display(cmd.Argv))
}

// bunValued are bun options that take a separate value.
var bunValued = map[string]bool{
	"--config": true, "-c": true, "--env-file": true, "--preload": true, "-r": true, "--define": true, "-d": true,
	"--loader": true, "-l": true, "--tsconfig-override": true, "--main-fields": true, "--extension-order": true,
	"--jsx-factory": true, "--jsx-fragment": true, "--jsx-import-source": true, "--port": true, "--elide-lines": true,
}

func bunAnchor(cmd command.Command, g *graph.Graph) resolver.Anchor {
	c := cwd{dir: cmd.Dir, known: cmd.DirKnown}
	var filters []string
	var positional []string
	all := false
	args := cmd.Argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, val, hasVal := splitOpt(a)
		switch {
		case name == "--filter" || name == "-F":
			if hasVal {
				filters = append(filters, val)
			} else if i+1 < len(args) {
				i++
				filters = append(filters, args[i])
			}
		case name == "--cwd":
			if hasVal {
				c.cd(val)
			} else if i+1 < len(args) {
				i++
				c.cd(args[i])
			}
		case name == "--workspaces":
			all = true
		case len(positional) == 0 && bunValued[name] && !hasVal:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			positional = append(positional, a)
		}
	}
	sub := ""
	if len(positional) > 0 {
		sub = positional[0]
	}
	if sub == "" && len(filters) == 0 {
		return resolver.Anchor{Kind: resolver.Neutral}
	}
	if installCommands[sub] || neutralCommands[sub] || sub == "repl" {
		return resolver.Anchor{Kind: resolver.Neutral}
	}
	if len(filters) > 0 {
		return selectFilters(filters, c.bases(), g)
	}
	if all {
		return unknown("%q runs in every workspace", display(cmd.Argv))
	}
	return scriptRun("bun", c, g, display(cmd.Argv))
}
