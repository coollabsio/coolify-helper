// Package turbo resolves what Turborepo adds to a JavaScript workspace (whose
// members the js resolver finds):
//
//   - turbo.json "globalDependencies" and "globalDotEnv" (globs relative to the
//     root) become global rules; negations are ignored (they never narrow).
//   - "tasks" (or the legacy "pipeline") keys and "dependsOn" entries of the form
//     "pkg#task" become edges: "web#build" depending on "ui#build" is an edge from
//     web to ui, and a plain task depending on "ui#build" is an edge from every
//     member to ui. "inputs" that start with "$TURBO_ROOT$/" (or that climb out
//     of the package with "../") become globals.
//   - Package configurations (<member>/turbo.json) are member manifests, read
//     the same way, with their edges from that member.
//
// "globalEnv" and "env" are ignored: environment variables are not files.
// Anchors come from "turbo run <task> --filter=…" (also "-F", "--filter <f>",
// "--scope", "turbo <task> …") and "turbo prune <name…>"/"--scope=<name>".
// An unparseable turbo.json fails Resolve, which drops the workspace limit.
package turbo

import (
	"context"
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

// Resolver is the Turborepo resolver.
type Resolver struct{}

// New returns the resolver.
func New() *Resolver { return &Resolver{} }

var _ resolver.Resolver = (*Resolver)(nil)

// Name implements resolver.Resolver.
func (*Resolver) Name() string { return "turbo" }

// configNames are the names of a turbo configuration file.
var configNames = []string{"turbo.json", "turbo.jsonc"}

// Pathspecs implements resolver.Resolver: every turbo.json(c), and what the js
// workspace reads (the members come from it).
func (*Resolver) Pathspecs(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == "node_modules" {
			return false
		}
	}
	switch path.Base(p) {
	case "turbo.json", "turbo.jsonc":
		return true
	}
	return js.New().Pathspecs(p)
}

// Detect implements resolver.Resolver.
func (*Resolver) Detect(t tree.Tree) bool {
	for _, n := range configNames {
		if _, ok := t.Lookup(n); ok {
			return true
		}
	}
	return false
}

// config is the part of turbo.json the resolver reads.
type config struct {
	GlobalDependencies []string        `json:"globalDependencies"`
	GlobalDotEnv       []string        `json:"globalDotEnv"`
	Tasks              map[string]task `json:"tasks"`
	Pipeline           map[string]task `json:"pipeline"`
}

type task struct {
	DependsOn []string `json:"dependsOn"`
	Inputs    []string `json:"inputs"`
	DotEnv    []string `json:"dotEnv"`
}

// Resolve implements resolver.Resolver.
func (*Resolver) Resolve(ctx context.Context, t tree.Tree) (*graph.Graph, error) {
	w, err := js.Load(ctx, t)
	if err != nil {
		return nil, err
	}
	g := graph.New()
	g.Resolvers = append(g.Resolvers, "turbo")

	// One batch: the root configuration and every package configuration.
	var paths []string
	for _, n := range configNames {
		if _, ok := t.Lookup(n); ok {
			paths = append(paths, n)
		}
	}
	memberConfig := map[string]string{}
	for _, m := range w.Members {
		for _, n := range configNames {
			p := m.Dir + "/" + n
			if _, ok := t.Lookup(p); ok {
				paths = append(paths, p)
				memberConfig[p] = m.Dir
			}
		}
	}
	files, err := t.Read(ctx, paths)
	if err != nil {
		return nil, err
	}

	byName := map[string][]string{}
	var allDirs []string
	for _, m := range w.Members {
		allDirs = append(allDirs, m.Dir)
		if m.Name != "" {
			byName[m.Name] = append(byName[m.Name], m.Dir)
		}
	}
	edges := map[string][]string{}

	for _, p := range paths {
		data, ok := files[p]
		if !ok {
			return nil, fmt.Errorf("%s: not a regular file", p)
		}
		var c config
		if err := jsonc.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		owner, isMember := memberConfig[p]
		if isMember {
			g.AddPackage(graph.Package{Dir: owner, Resolvers: []string{"turbo"}, Manifests: []string{p}})
		} else {
			g.AddGlobal(match.File, p, "turbo")
			for _, list := range [][]string{c.GlobalDependencies, c.GlobalDotEnv} {
				for _, pat := range list {
					if err := addGlobs(g, "", pat, p+" globalDependencies"); err != nil {
						return nil, fmt.Errorf("%s: %v", p, err)
					}
				}
			}
		}
		tasks := c.Tasks
		if len(tasks) == 0 {
			tasks = c.Pipeline
		}
		keys := make([]string, 0, len(tasks))
		for k := range tasks {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			tk := tasks[key]
			// The packages the task belongs to.
			var from []string
			switch {
			case isMember:
				from = []string{owner}
			case strings.Contains(key, "#"):
				pkg := key[:strings.IndexByte(key, '#')]
				from = byName[pkg]
			default:
				from = allDirs
			}
			for _, dep := range tk.DependsOn {
				dep = strings.TrimPrefix(dep, "$")
				i := strings.IndexByte(dep, '#')
				if i <= 0 || strings.HasPrefix(dep, "^") {
					continue
				}
				for _, to := range byName[dep[:i]] {
					for _, f := range from {
						edges[f] = append(edges[f], to)
					}
				}
			}
			for _, in := range append(append([]string(nil), tk.Inputs...), tk.DotEnv...) {
				if strings.HasPrefix(in, "!") || in == "$TURBO_DEFAULT$" {
					continue
				}
				if strings.HasPrefix(in, "$TURBO_ROOT$/") {
					if err := addGlobs(g, "", strings.TrimPrefix(in, "$TURBO_ROOT$/"), p+" inputs"); err != nil {
						return nil, fmt.Errorf("%s: %v", p, err)
					}
					continue
				}
				if strings.HasPrefix(in, "../") {
					// A package-relative input outside the package: global for the
					// packages of the task.
					for _, f := range from {
						if err := addGlobs(g, f, in, p+" inputs"); err != nil {
							return nil, fmt.Errorf("%s: %v", p, err)
						}
					}
				}
			}
		}
	}
	for _, from := range sortedKeys(edges) {
		g.AddPackage(graph.Package{Dir: from, Resolvers: []string{"turbo"}, DepDirs: edges[from]})
	}
	// Member scripts that run turbo ("turbo run build --filter=docs").
	js.AddScriptEdges(w, w.MemberGraph(), g, (*Resolver)(nil).AnchorFromCommand, []string{"turbo"})
	return g, nil
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// addGlobs adds a turbo glob (relative to dir) as global rules.
func addGlobs(g *graph.Graph, dir, pattern, source string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "!") {
		return nil
	}
	pattern = strings.TrimPrefix(pattern, "./")
	rules, err := glob.ToRules(dir, pattern)
	if err != nil {
		return fmt.Errorf("glob %q: %v", pattern, err)
	}
	for _, r := range rules {
		g.AddGlobal(r.Kind, r.Value, source)
	}
	return nil
}

// AnchorFromCommand implements resolver.Resolver for "turbo run", "turbo
// <task>" and "turbo prune".
func (*Resolver) AnchorFromCommand(cmd command.Command, g *graph.Graph) resolver.Anchor {
	if len(cmd.Argv) == 0 || cmd.Argv[0] != "turbo" {
		return resolver.Anchor{Kind: resolver.NotMine}
	}
	a := anchor(cmd, g)
	a.Tool = "turbo"
	return a
}

// turboValued are turbo options that take a separate value.
var turboValued = map[string]bool{
	"--cwd": true, "--concurrency": true, "--cache-dir": true, "--log-order": true, "--output-logs": true,
	"--graph": true, "--env-mode": true, "--cache": true, "--profile": true, "--summarize": true, "--log-prefix": true,
	"--ui": true, "--team": true, "--token": true, "--api": true, "--login": true, "--heap": true, "--trace": true,
	"--out-dir": true, "--remote-cache-timeout": true, "--cache-workers": true, "--global-deps": true, "--since": true,
	"--ignore": true,
}

// neutralSubcommands build nothing.
var neutralSubcommands = map[string]bool{
	"login": true, "logout": true, "link": true, "unlink": true, "daemon": true, "telemetry": true, "gen": true,
	"generate": true, "info": true, "ls": true, "query": true, "scan": true, "bin": true, "completion": true,
	"boundaries": true, "devtools": true, "--version": true, "-v": true, "--help": true, "-h": true, "help": true, "config": true,
}

func unknown(format string, args ...any) resolver.Anchor {
	return resolver.Anchor{Kind: resolver.Unknown, Reason: fmt.Sprintf(format, args...)}
}

func anchor(cmd command.Command, g *graph.Graph) resolver.Anchor {
	dir, known := cmd.Dir, cmd.DirKnown
	var filters, scopes []string
	var positional []string
	affected := false
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
		take := func() string {
			if hasVal {
				return val
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case name == "--filter" || name == "-F":
			filters = append(filters, take())
		case name == "--scope":
			scopes = append(scopes, take())
		case name == "--cwd":
			v := take()
			if !known || v == "" || strings.ContainsAny(v, "$~") || strings.HasPrefix(v, "/") {
				dir, known = "", false
			} else if d, ok := pathx.Join(dir, v); ok {
				dir = d
			} else {
				dir, known = "", false
			}
		case name == "--affected":
			affected = true
		case name == "--since" || name == "--ignore":
			take()
			affected = affected || name == "--since"
		case turboValued[name] && !hasVal:
			take()
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
	case sub == "" && len(filters) == 0:
		return resolver.Anchor{Kind: resolver.Neutral}
	case neutralSubcommands[sub]:
		return resolver.Anchor{Kind: resolver.Neutral}
	case sub == "prune":
		names := append(append([]string(nil), positional[1:]...), scopes...)
		if len(names) == 0 {
			return unknown("%q names no package", strings.Join(cmd.Argv, " "))
		}
		return selectAll(names, nil, g)
	}
	if affected {
		return unknown("%q selects packages by changed files", strings.Join(cmd.Argv, " "))
	}
	filters = append(filters, scopes...)
	if len(filters) > 0 {
		// A path filter is relative to the directory turbo runs in; the root is
		// tried too (wider).
		var bases []string
		if known {
			bases = []string{dir}
			if dir != "" {
				bases = append(bases, "")
			}
		}
		return selectAll(filters, bases, g)
	}
	// No filter: turbo (1.8 and later) scopes the run to the package it runs in.
	if !known {
		return unknown("%q runs in a directory the tool can't follow", strings.Join(cmd.Argv, " "))
	}
	if dir != "" && g.PackageAt(dir) != nil {
		return resolver.Anchor{Kind: resolver.Anchored, Dirs: []string{dir}}
	}
	return unknown("%q has no filter, so it runs every package", strings.Join(cmd.Argv, " "))
}

func selectAll(filters, bases []string, g *graph.Graph) resolver.Anchor {
	var dirs []string
	for _, f := range filters {
		s := js.SelectFilter(f, bases, g)
		if s.Reason != "" {
			return unknown("%s", s.Reason)
		}
		dirs = append(dirs, s.Dirs...)
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
