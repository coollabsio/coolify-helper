package nx_test

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/nx"
	rt "github.com/coollabsio/coolify-helper/src/affected/internal/resolver/resolvertest"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

var repo = map[string]string{
	"package.json": `{"name":"root","devDependencies":{"nx":"19"}}`,
	"nx.json": `{
		"namedInputs": {
			"sharedGlobals": ["{workspaceRoot}/babel.config.json", "{workspaceRoot}/tools/eslint-rules/**/*"],
			"default": ["{projectRoot}/**/*", "sharedGlobals"],
			"production": ["default", "!{projectRoot}/**/*.spec.ts", "!{workspaceRoot}/x"]
		},
		"targetDefaults": {
			"build": {"inputs": ["production", "^production", {"externalDependencies": ["vite"]}, {"env": "CI"},
				{"fileset": "{workspaceRoot}/.browserslistrc"}, {"dependentTasksOutputFiles": "**/*.d.ts"}]}
		},
		"implicitDependencies": {"workspace.json": "*"}
	}`,
	"apps/web/project.json":       `{"name":"web","implicitDependencies":["shared-*","!data","tag:scope:ui"],"targets":{"build":{"dependsOn":[{"projects":["config"],"target":"build"},"^build"]}}}`,
	"apps/api/project.json":       `{"name":"api","implicitDependencies":["data"],"targets":{"build":{"inputs":[{"input":"production","projects":["proto"]}]}}}`,
	"apps/admin/project.json":     `{"implicitDependencies":["*"]}`,
	"apps/admin/package.json":     `{"name":"@acme/admin"}`,
	"apps/cli/project.json":       `{}`,
	"libs/shared-a/project.json":  `{"name":"shared-a"}`,
	"libs/shared-b/project.json":  `{"name":"shared-b"}`,
	"libs/data/project.json":      `{"name":"data"}`,
	"libs/ui/project.json":        `{"name":"ui","tags":["scope:ui"]}`,
	"libs/config/project.json":    `{"name":"config"}`,
	"libs/proto/project.json":     `{"name":"proto"}`,
	"libs/alone/project.json":     `{"name":"alone"}`,
	"node_modules/x/project.json": `{"name":"x"}`,
}

func TestResolve(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(repo))
	wantDirs := []string{"apps/admin", "apps/api", "apps/cli", "apps/web", "libs/alone", "libs/config", "libs/data", "libs/proto", "libs/shared-a", "libs/shared-b", "libs/ui"}
	if got := rt.Dirs(g); !reflect.DeepEqual(got, wantDirs) {
		t.Fatalf("members = %v", got)
	}
	if got := graph.SortedKeys(g.Closure([]string{"apps/web"})); !reflect.DeepEqual(got, []string{"apps/web", "libs/config", "libs/shared-a", "libs/shared-b", "libs/ui"}) {
		t.Errorf("web closure = %v", got)
	}
	if got := graph.SortedKeys(g.Closure([]string{"apps/api"})); !reflect.DeepEqual(got, []string{"apps/api", "libs/data", "libs/proto"}) {
		t.Errorf("api closure = %v", got)
	}
	if got := g.Closure([]string{"apps/admin"}); len(got) != len(wantDirs) {
		t.Errorf("'*' must depend on every project: %v", graph.SortedKeys(got))
	}
	if p := g.PackageAt("apps/admin"); !reflect.DeepEqual(p.Names, []string{"@acme/admin"}) || !reflect.DeepEqual(p.Manifests, []string{"apps/admin/package.json", "apps/admin/project.json"}) {
		t.Errorf("admin = %+v", p)
	}
	if p := g.PackageAt("apps/cli"); !reflect.DeepEqual(p.Names, []string{"cli"}) {
		t.Errorf("cli takes the directory name: %+v", p)
	}
	var globals []string
	for _, gl := range g.Globals {
		globals = append(globals, string(gl.Kind)+":"+gl.Path+gl.Pattern)
	}
	sort.Strings(globals)
	want := []string{"dir:.browserslistrc", "dir:babel.config.json", "file:nx.json", "file:package.json", "file:workspace.json", "glob:tools/eslint-rules/**/*"}
	if !reflect.DeepEqual(globals, want) {
		t.Errorf("globals = %v, want %v", globals, want)
	}
}

func TestPackageJSONProjectsAndAliases(t *testing.T) {
	files := map[string]string{
		"package.json": `{"workspaces":["packages/*"]}`,
		"nx.json":      `{}`,
		"tsconfig.base.json": `{
			// integrated libraries without package.json
			"compilerOptions": {"baseUrl": ".", "paths": {"@acme/ui": ["libs/ui/src/index.ts"], "@acme/util/*": ["libs/util/src/*"], "@x/out": ["../outside"]}}
		}`,
		"packages/web/package.json":   `{"name":"web","nx":{"name":"web-app","implicitDependencies":["lib-a"],"tags":["t"]}}`,
		"packages/lib-a/package.json": `{"name":"lib-a"}`,
		"packages/lib-b/package.json": `{"name":"lib-b"}`,
		"libs/ui/project.json":        `{"name":"ui"}`,
		"libs/util/project.json":      `{"name":"util"}`,
		"apps/site/project.json":      `{"name":"site"}`,
	}
	g := rt.Resolve(t, tree.NewMem(files))
	if got := graph.SortedKeys(g.Closure([]string{"packages/web"})); !reflect.DeepEqual(got, []string{"libs/ui", "libs/util", "packages/lib-a", "packages/web"}) {
		t.Errorf("web closure = %v", got)
	}
	if got := graph.SortedKeys(g.Closure([]string{"apps/site"})); !reflect.DeepEqual(got, []string{"apps/site", "libs/ui", "libs/util"}) {
		t.Errorf("site closure = %v", got)
	}
	if ps := g.ByName("web-app"); len(ps) != 1 || ps[0].Dir != "packages/web" {
		t.Errorf("nx.name alias: %v", ps)
	}
}

func TestStandaloneRootProject(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(map[string]string{
		"package.json":            `{"name":"app"}`,
		"project.json":            `{"name":"app","implicitDependencies":["lib"]}`,
		"libs/lib/project.json":   `{"name":"lib"}`,
		"libs/other/project.json": `{"name":"other"}`,
	}))
	if got := graph.SortedKeys(g.Closure([]string{""})); !reflect.DeepEqual(got, []string{"", "libs/lib"}) {
		t.Errorf("closure = %v", got)
	}
}

func TestWidening(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"unparseable nx.json":        {"nx.json": `{`},
		"unparseable project.json":   {"nx.json": `{}`, "a/project.json": `{"name":`},
		"implicitDependencies type":  {"nx.json": `{}`, "a/project.json": `{"implicitDependencies":"x"}`},
		"bad dependsOn entry":        {"nx.json": `{}`, "a/project.json": `{"targets":{"b":{"dependsOn":[1]}}}`},
		"bad input entry":            {"nx.json": `{"namedInputs":{"x":[1]}}`},
		"input escapes":              {"nx.json": `{"namedInputs":{"x":["{workspaceRoot}/../y"]}}`},
		"unparseable package nx":     {"package.json": `{"workspaces":["a"]}`, "nx.json": `{}`, "a/package.json": `{"nx":[]}`},
		"unparseable beside package": {"nx.json": `{}`, "a/project.json": `{}`, "a/package.json": `{`},
		"unparseable tsconfig.base":  {"nx.json": `{}`, "tsconfig.base.json": `{"compilerOptions":`},
		"broken js workspace":        {"package.json": `{"workspaces":["a"]}`, "nx.json": `{}`, "a/package.json": `{`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := nx.New().Resolve(context.Background(), tree.NewMem(files)); err == nil {
				t.Errorf("want an error")
			}
		})
	}
	tr := tree.NewMem(map[string]string{"nx.json": `{}`, "a/project.json": `{}`}).WithBudget(&tree.Budget{MaxFiles: 1})
	if _, err := nx.New().Resolve(context.Background(), tr); err == nil {
		t.Errorf("budget: want an error")
	}
}

func TestReadBatches(t *testing.T) {
	tr := &rt.Counting{Tree: tree.NewMem(repo)}
	if _, err := nx.New().Resolve(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	// js.Load: the root package.json (no workspaces, so no member batch); nx: one batch.
	if len(tr.Batches) != 2 {
		t.Errorf("batches = %d: %v", len(tr.Batches), tr.Batches)
	}
}

func TestAnchor(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(repo))
	r := nx.New()
	tests := []struct {
		script string
		dir    string
		kind   resolver.AnchorKind
		dirs   []string
	}{
		{"nx build web", "", resolver.Anchored, []string{"apps/web"}},
		{"npx nx build web --prod", "", resolver.Anchored, []string{"apps/web"}},
		{"pnpm nx run api:build:production", "", resolver.Anchored, []string{"apps/api"}},
		{"nx run api:build --configuration=production", "", resolver.Anchored, []string{"apps/api"}},
		{"nx api:build", "", resolver.Anchored, []string{"apps/api"}},
		{"nx build --project=web", "", resolver.Anchored, []string{"apps/web"}},
		{"nx build @acme/admin", "", resolver.Anchored, []string{"apps/admin"}},
		{"nx run-many -t build -p web,api", "", resolver.Anchored, []string{"apps/api", "apps/web"}},
		{"nx run-many --target=build --projects=web", "", resolver.Anchored, []string{"apps/web"}},
		{"nx build", "apps/web", resolver.Anchored, []string{"apps/web"}},
		{"nx build", "", resolver.Unknown, nil},
		{"nx build", "?", resolver.Unknown, nil},
		{"nx build nope", "", resolver.Unknown, nil},
		{"nx run web", "", resolver.Unknown, nil},
		{"nx run-many -t build", "", resolver.Unknown, nil},
		{"nx run-many -t build -p 'shared-*'", "", resolver.Unknown, nil},
		{"nx affected -t build", "", resolver.Unknown, nil},
		{"nx graph", "", resolver.Unknown, nil},
		{"nx reset", "", resolver.Neutral, nil},
		{"nx", "", resolver.Neutral, nil},
		{"pnpm build", "", resolver.NotMine, nil},
	}
	for _, tt := range tests {
		t.Run(tt.script, func(t *testing.T) {
			dir, known := tt.dir, true
			if dir == "?" {
				dir, known = "", false
			}
			cmds := command.SplitFrom(tt.script, dir, known)
			a := r.AnchorFromCommand(cmds[0], g)
			if a.Kind != tt.kind || !reflect.DeepEqual(a.Dirs, tt.dirs) {
				t.Errorf("got %v %v (%s), want %v %v", a.Kind, a.Dirs, a.Reason, tt.kind, tt.dirs)
			}
		})
	}
}

func TestPathspecsDetect(t *testing.T) {
	r := nx.New()
	for p, want := range map[string]bool{"nx.json": true, "apps/x/project.json": true, "tsconfig.base.json": true, "apps/x/package.json": true, "node_modules/a/project.json": false, "apps/x/tsconfig.json": true, "apps/x/tsconfig.app.json": true, "node_modules/a/tsconfig.json": false, "apps/x/base.json": false} {
		if r.Pathspecs(p) != want {
			t.Errorf("Pathspecs(%q) != %v", p, want)
		}
	}
	if !r.Detect(tree.NewMem(map[string]string{"libs/a/project.json": "{}"})) || r.Detect(tree.NewMem(map[string]string{"package.json": "{}"})) {
		t.Errorf("Detect")
	}
}

// Wildcard alias targets point into every project under their literal
// directory; a project's own tsconfig files (and what they extend) add edges
// from that project. Every file read is covered by Pathspecs.
func TestAliasWildcardsAndProjectTSConfigs(t *testing.T) {
	files := map[string]string{
		"nx.json":                    `{}`,
		"tsconfig.base.json":         `{"compilerOptions":{"baseUrl":".","paths":{"@acme/*":["libs/*/src/index.ts"]}}}`,
		"apps/web/project.json":      `{"name":"web"}`,
		"libs/ui/project.json":       `{"name":"ui"}`,
		"libs/data/project.json":     `{"name":"data"}`,
		"tools/gen/project.json":     `{"name":"gen"}`,
		"pkgs/a/project.json":        `{"name":"a"}`,
		"pkgs/b/project.json":        `{"name":"b"}`,
		"apps/api/project.json":      `{"name":"api"}`,
		"apps/api/tsconfig.app.json": `{"extends":"./tsconfig.json"}`,
		"apps/api/tsconfig.json":     `{"extends":"../../configs/tsconfig.paths.json"}`,
		// No baseUrl: relative to this file.
		"configs/tsconfig.paths.json": `{"compilerOptions":{"paths":{"@p/*":["../pkgs/*"]}}}`,
		"apps/odd/project.json":       `{"name":"odd"}`,
		"apps/odd/tsconfig.json":      `{"extends":"./base"}`,
		"apps/odd/base.json":          `{"compilerOptions":{"paths":{"x":["../../tools/gen"]}}}`,
	}
	tr := &rt.Counting{Tree: tree.NewMem(files)}
	g, err := nx.New().Resolve(context.Background(), tr)
	if err != nil {
		t.Fatal(err)
	}
	g.Normalize()
	r := nx.New()
	for _, b := range tr.Batches {
		for _, p := range b {
			if !r.Pathspecs(p) {
				t.Errorf("read %s, which Pathspecs does not cover", p)
			}
		}
	}
	all := []string{"apps/api", "apps/odd", "apps/web", "libs/data", "libs/ui", "pkgs/a", "pkgs/b", "tools/gen"}
	for anchor, want := range map[string][]string{
		"apps/web": {"apps/web", "libs/data", "libs/ui"},
		"apps/api": {"apps/api", "libs/data", "libs/ui", "pkgs/a", "pkgs/b"},
		// base.json is not a tsconfig*.json file: unread, it may alias anything.
		"apps/odd": all,
	} {
		if got := graph.SortedKeys(g.Closure([]string{anchor})); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: closure = %v, want %v", anchor, got, want)
		}
	}
}
