package workspace

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/all"
	rt "github.com/coollabsio/coolify-helper/src/affected/internal/resolver/resolvertest"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

func compileFor(t *testing.T, files map[string]string, dockerfile string, refs, managed []string) limits.Result {
	t.Helper()
	g := rt.Resolve(t, tree.NewMem(files))
	a := FindAnchor(AnchorInput{Graph: g, Resolvers: all.All(), BuildPack: "dockerfile", DockerfilePath: dockerfile})
	return Compile(g, a, refs, managed)
}

// decide returns "include:<rule>" or "exclude" for a path.
func decide(rs *match.Ruleset, p string) string {
	for i := len(rs.Rules) - 1; i >= 0; i-- {
		r := rs.Rules[i]
		if ruleMatches(r, p) {
			if r.Action == match.Exclude {
				return "exclude"
			}
			return "include:" + r.Origin.Rule
		}
	}
	if rs.Default == match.Include {
		return "include:" + rs.DefaultOrigin.Rule
	}
	return "exclude"
}

func TestCompileMonorepo(t *testing.T) {
	res := compileFor(t, monorepo, "apps/web/Dockerfile", []string{"content/blog", "packages/cfg/theme"}, []string{"coolify.json"})
	if res.Ruleset == nil {
		t.Fatalf("no ruleset: %v", res.Widened)
	}
	rs := res.Ruleset
	if rs.Limit != "workspace" || rs.Default != match.Include || rs.DefaultOrigin.Rule != "global" {
		t.Errorf("ruleset header = %+v", rs)
	}
	tests := map[string]string{
		"apps/web/src/page.tsx":               "include:workspace",
		"apps/web":                            "include:workspace",
		"packages/ui/src/Button.tsx":          "include:workspace",
		"packages/utils/index.ts":             "include:global", // a lifecycle member
		"apps/api/src/main.ts":                "exclude",
		"apps/api/package.json":               "include:global",
		"apps/api":                            "exclude",
		"apps/web/examples/demo/index.ts":     "include:workspace", // inside apps/web, a closure member
		"apps/web/examples/demo/package.json": "include:global",
		"packages/cfg/index.js":               "exclude",
		"packages/cfg/theme/x.css":            "include:reference",
		"content/blog/post.md":                "include:reference",
		"content/other/post.md":               "include:global",
		"docs/guide.md":                       "include:global",
		"README.md":                           "exclude",
		"README":                              "exclude",
		"LICENSE":                             "exclude",
		"CHANGELOG.md":                        "exclude",
		"apps/web/README.md":                  "include:workspace",
		"apps/api/README.md":                  "exclude",
		".github/workflows/ci.yml":            "exclude",
		".github":                             "exclude",
		".gitlab-ci.yml":                      "exclude",
		"CODEOWNERS":                          "exclude",
		"docs/CODEOWNERS":                     "include:global",
		".editorconfig":                       "exclude",
		".husky/pre-commit":                   "exclude",
		"pnpm-lock.yaml":                      "include:lockfile",
		"package.json":                        "include:global",
		"pnpm-workspace.yaml":                 "include:global",
		"turbo.json":                          "include:global",
		"scripts/build.sh":                    "include:global",
		"coolify.json":                        "include:managed_config",
		"Dockerfile":                          "include:global",
	}
	for p, want := range tests {
		if got := decide(rs, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
	// The via chain of a workspace match.
	for _, r := range rs.Rules {
		if r.Kind == match.Dir && r.Path == "packages/utils" && r.Action == match.Include && r.Origin.Rule == "workspace" {
			if want := []string{"apps/web (@acme/web)", "@acme/ui", "@acme/utils"}; !reflect.DeepEqual(r.Origin.Via, want) {
				t.Errorf("via = %v, want %v", r.Origin.Via, want)
			}
		}
	}
}

func TestCompileRuleOrder(t *testing.T) {
	res := compileFor(t, monorepo, "apps/web/Dockerfile", nil, nil)
	got := Display(res.Ruleset)
	want := []string{
		// (a) members, shallow first
		"!apps/api/**", "apps/web/**", "!packages/cfg/**", "packages/ui/**", "packages/utils/**",
		// (no rule for apps/web/examples/demo: it is inside apps/web, a closure member)
		// (b) meta
		"!README*", "!LICENSE*", "!CHANGELOG*", "!CONTRIBUTING*", "!CODE_OF_CONDUCT*", "!SECURITY*",
		"!.github/**", "!.gitlab/**", "!.vscode/**", "!.idea/**", "!.husky/**", "!.changeset/**",
		"!.gitlab-ci.yml", "!.editorconfig", "!CODEOWNERS",
		// (c) globals, manifests, lifecycle dirs
		"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "turbo.json",
		"apps/api/package.json", "apps/web/package.json", "packages/cfg/package.json", "packages/ui/package.json",
		"packages/utils/package.json", "packages/utils/**", "apps/web/examples/demo/package.json",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules =\n%q\nwant\n%q", got, want)
	}
}

func TestCompileNestedMembers(t *testing.T) {
	// The nested member is the anchor; its parent is a dependency.
	res := compileFor(t, monorepo, "apps/web/examples/demo/Dockerfile", nil, nil)
	for p, want := range map[string]string{
		"apps/web/examples/demo/x.ts": "include:workspace",
		"apps/web/src/x.ts":           "include:workspace",
		"apps/api/x.ts":               "exclude",
	} {
		if got := decide(res.Ruleset, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
	// The parent is the anchor; the nested member is not a dependency, but its
	// files are inside the anchor's directory: they stay included.
	res = compileFor(t, monorepo, "apps/web/Dockerfile", nil, nil)
	if got := decide(res.Ruleset, "apps/web/examples/demo/x.ts"); got != "include:workspace" {
		t.Errorf("nested non-dependency: %s", got)
	}
	// An outer member excluded, an inner one in the closure.
	files := map[string]string{
		"package.json":                      `{"workspaces":["apps/*","apps/web/libs/*"]}`,
		"apps/web/package.json":             `{"name":"web"}`,
		"apps/api/package.json":             `{"name":"api","dependencies":{"shared":"*"}}`,
		"apps/web/libs/shared/package.json": `{"name":"shared"}`,
	}
	res = compileFor(t, files, "apps/api/Dockerfile", nil, nil)
	for p, want := range map[string]string{
		"apps/web/libs/shared/a.ts": "include:workspace",
		"apps/web/a.ts":             "exclude",
	} {
		if got := decide(res.Ruleset, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

func TestCompileMemberUnderMeta(t *testing.T) {
	files := map[string]string{
		"package.json":                        `{"workspaces":["apps/*",".github/actions/*"]}`,
		"apps/web/package.json":               `{"name":"web","dependencies":{"deploy-action":"*"}}`,
		".github/actions/deploy/package.json": `{"name":"deploy-action"}`,
		".github/actions/other/package.json":  `{"name":"other-action"}`,
	}
	res := compileFor(t, files, "apps/web/Dockerfile", nil, nil)
	for p, want := range map[string]string{
		".github/actions/deploy/index.js": "include:workspace",
		".github/actions/other/index.js":  "exclude",
		".github/workflows/ci.yml":        "exclude",
	} {
		if got := decide(res.Ruleset, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

func TestCompileRootMember(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(map[string]string{
		"nx.json": `{}`, "project.json": `{"name":"app","implicitDependencies":["a"]}`,
		"libs/a/project.json": `{"name":"a"}`, "libs/b/project.json": `{"name":"b"}`,
	}))
	res := Compile(g, Anchor{Dirs: []string{""}}, nil, nil)
	for _, r := range res.Ruleset.Rules {
		if r.Kind == match.Dir && r.Path == "" {
			t.Errorf("a rule for the root member: %+v", r)
		}
	}
	for p, want := range map[string]string{"src/main.ts": "include:global", "libs/a/x.ts": "include:workspace", "libs/b/x.ts": "exclude"} {
		if got := decide(res.Ruleset, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

func TestCompileGlobalsFromTurboAndNx(t *testing.T) {
	files := map[string]string{
		"package.json":                 `{"workspaces":["apps/*","packages/*"]}`,
		"turbo.json":                   `{"globalDependencies":["packages/config/*.json"]}`,
		"apps/web/package.json":        `{"name":"web"}`,
		"packages/config/package.json": `{"name":"config"}`,
	}
	res := compileFor(t, files, "apps/web/Dockerfile", nil, nil)
	for p, want := range map[string]string{
		"packages/config/base.json": "include:global",
		"packages/config/index.js":  "exclude",
	} {
		if got := decide(res.Ruleset, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

// Every widening path of Compile.
func TestCompileWidening(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(monorepo))
	tests := []struct {
		name   string
		g      *graph.Graph
		a      Anchor
		refs   []string
		reason string
	}{
		{"no anchor", g, Anchor{Reason: NoAnchor + ": x."}, nil, NoAnchor + ": x."},
		{"no anchor without a reason", g, Anchor{}, nil, NoAnchor + "."},
		{"anchor is not a member", g, Anchor{Dirs: []string{"docs"}}, nil, "not a workspace package"},
		{"reference path is the root", g, Anchor{Dirs: []string{"apps/web"}}, []string{"/"}, "repository root"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Compile(tt.g, tt.a, tt.refs, nil)
			if res.Ruleset != nil || len(res.Widened) != 1 || res.Widened[0].Scope != "limit:workspace" || !strings.Contains(res.Widened[0].Reason, tt.reason) {
				t.Errorf("result = %+v", res)
			}
		})
	}
	nm := graph.New()
	nm.Merge(g)
	nm.NotModeled = []string{"bazel"}
	if res := Compile(nm, Anchor{Dirs: []string{"apps/web"}}, nil, nil); res.Ruleset != nil || len(res.Widened) != 1 || !strings.Contains(res.Widened[0].Reason, "Bazel is not modeled") {
		t.Errorf("not modeled: %+v", res)
	}
	gd := graph.New()
	gd.Merge(g)
	gd.AddGlobal(match.Dir, "", "x")
	if res := Compile(gd, Anchor{Dirs: []string{"apps/web"}}, nil, nil); res.Ruleset != nil || len(res.Widened) != 1 {
		t.Errorf("root global: %+v", res)
	}
	// No members: nothing to narrow, and nothing to report.
	empty := rt.Resolve(t, tree.NewMem(map[string]string{"package.json": `{}`}))
	if res := Compile(empty, Anchor{}, nil, nil); res.Ruleset != nil || len(res.Widened) != 0 {
		t.Errorf("no members: %+v", res)
	}
	if res := Compile(nil, Anchor{}, nil, nil); res.Ruleset != nil || len(res.Widened) != 0 {
		t.Errorf("nil graph: %+v", res)
	}
}

func TestExpandSymlinks(t *testing.T) {
	files := map[string]string{}
	for k, v := range monorepo {
		files[k] = v
	}
	files["apps/web/shared"] = "symlink:../../packages/cfg/shared"
	files["apps/api/link"] = "symlink:../../packages/cfg/api-only"        // excluded: not followed
	files["packages/cfg/shared/inner"] = "symlink:../../../apps/api/i18n" // reached through the first link
	files["packages/cfg/shared/x.css"] = ""
	g := rt.Resolve(t, tree.NewMem(files))
	a := FindAnchor(AnchorInput{Graph: g, Resolvers: all.All(), BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile"})
	tr := &rt.Counting{Tree: tree.NewMem(files)}
	res := ExpandSymlinks(context.Background(), tr, Compile(g, a, nil, nil))
	if res.Ruleset == nil {
		t.Fatalf("widened: %v", res.Widened)
	}
	for p, want := range map[string]string{
		"packages/cfg/shared/x.css": "include:reference",
		"apps/api/i18n/en.json":     "include:reference",
		"packages/cfg/api-only/x":   "exclude",
		"packages/cfg/other":        "exclude",
	} {
		if got := decide(res.Ruleset, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
	if len(tr.Batches) != 2 {
		t.Errorf("batches = %v", tr.Batches)
	}
	if !reflect.DeepEqual(res.Reads, []string{"apps/web/shared", "packages/cfg/shared/inner"}) {
		t.Errorf("reads = %v", res.Reads)
	}
}

func TestExpandSymlinksWidening(t *testing.T) {
	for name, target := range map[string]string{
		"absolute": "symlink:/etc/passwd",
		"escape":   "symlink:../../../../outside",
		"to root":  "symlink:../..",
	} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range monorepo {
				files[k] = v
			}
			files["apps/web/link"] = target
			res := compileFor(t, files, "apps/web/Dockerfile", nil, nil)
			res = ExpandSymlinks(context.Background(), tree.NewMem(files), res)
			if res.Ruleset != nil || len(res.Widened) != 1 || res.Widened[0].Scope != "limit:workspace" {
				t.Errorf("result = %+v", res)
			}
		})
	}
	// A chain of symlinks deeper than the cap.
	files := map[string]string{}
	for k, v := range monorepo {
		files[k] = v
	}
	files["apps/web/l0"] = "symlink:../../chain/l1"
	for i := 1; i <= maxSymlinkRounds+1; i++ {
		files["chain/l"+itoa(i)] = "symlink:l" + itoa(i+1)
	}
	res := ExpandSymlinks(context.Background(), tree.NewMem(files), compileFor(t, files, "apps/web/Dockerfile", nil, nil))
	if res.Ruleset == nil && len(res.Widened) == 0 {
		t.Errorf("chain: %+v", res)
	}
	// The read budget.
	files = map[string]string{}
	for k, v := range monorepo {
		files[k] = v
	}
	files["apps/web/l"] = "symlink:../ui"
	base := compileFor(t, files, "apps/web/Dockerfile", nil, nil)
	res = ExpandSymlinks(context.Background(), tree.NewMem(files).WithBudget(&tree.Budget{MaxFiles: 0, MaxBytes: 1}), base)
	if res.Ruleset != nil || len(res.Widened) != 1 {
		t.Errorf("budget: %+v", res)
	}
	// Nothing to do.
	if res := ExpandSymlinks(context.Background(), tree.NewMem(monorepo), base); res.Ruleset == nil {
		t.Errorf("no symlinks widened")
	}
	if res := ExpandSymlinks(context.Background(), tree.NewMem(files), limits.Result{}); res.Ruleset != nil || len(res.Widened) != 0 {
		t.Errorf("nil ruleset: %+v", res)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestIncludes(t *testing.T) {
	rs := &match.Ruleset{Default: match.Exclude, Rules: []match.Rule{
		match.IncludeGlob("a/**/*.go", nil), match.ExcludeFile("a/b/x.go", nil), match.IncludeDir("c", nil), match.IncludeGlob("d/?.txt", nil),
	}}
	for p, want := range map[string]bool{
		"a/x.go": true, "a/b/c/y.go": true, "a/b/x.go": false, "c": true, "c/d": true, "cd": false, "d/é.txt": true, "d/ab.txt": false, "z": false,
	} {
		if got := Includes(rs, p); got != want {
			t.Errorf("Includes(%q) = %v", p, got)
		}
	}
}
