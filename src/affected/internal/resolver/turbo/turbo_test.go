package turbo_test

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	rt "github.com/coollabsio/coolify-helper/src/affected/internal/resolver/resolvertest"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/turbo"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

var repo = map[string]string{
	"package.json":      `{"workspaces":["apps/*","packages/*"]}`,
	"package-lock.json": "{}",
	"turbo.json": `{
		// comments and trailing commas are allowed
		"$schema": "https://turbo.build/schema.json",
		"globalDependencies": ["**/.env.*local", ".env", "config/{a,b}.json", "!ignored/**", "scripts/[xy].sh",],
		"globalDotEnv": [".env.shared"],
		"globalEnv": ["NODE_ENV"],
		"tasks": {
			"build": {"dependsOn": ["^build", "@acme/codegen#generate"], "inputs": ["$TURBO_DEFAULT$", "$TURBO_ROOT$/shared/tsconfig.json", "!README.md"]},
			"web#build": {"dependsOn": ["docs#build"]},
			"lint": {}
		}
	}`,
	"apps/web/package.json":         `{"name":"web"}`,
	"apps/api/package.json":         `{"name":"api"}`,
	"apps/api/turbo.json":           `{"extends":["//"],"tasks":{"build":{"dependsOn":["schema#build"],"inputs":["../../proto/**"]}}}`,
	"packages/docs/package.json":    `{"name":"docs"}`,
	"packages/schema/package.json":  `{"name":"schema"}`,
	"packages/codegen/package.json": `{"name":"@acme/codegen"}`,
	"packages/other/package.json":   `{"name":"other"}`,
}

func globals(g *graph.Graph) []string {
	var out []string
	for _, gl := range g.Globals {
		out = append(out, string(gl.Kind)+":"+gl.Path+gl.Pattern)
	}
	sort.Strings(out)
	return out
}

func TestResolve(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(repo))
	if got := graph.SortedKeys(g.Closure([]string{"apps/web"})); !reflect.DeepEqual(got, []string{"apps/web", "packages/codegen", "packages/docs"}) {
		t.Errorf("web closure = %v", got)
	}
	if got := graph.SortedKeys(g.Closure([]string{"apps/api"})); !reflect.DeepEqual(got, []string{"apps/api", "packages/codegen", "packages/schema"}) {
		t.Errorf("api closure = %v", got)
	}
	got := globals(g)
	for _, w := range []string{"dir:.env", "dir:.env.shared", "dir:config/a.json", "dir:config/b.json", "dir:shared/tsconfig.json",
		"file:turbo.json", "glob:**/.env.*local", "glob:proto/**", "glob:scripts/?.sh"} {
		if !contains(got, w) {
			t.Errorf("global %s missing from %v", w, got)
		}
	}
	for _, g := range got {
		if strings.Contains(g, "ignored") {
			t.Errorf("negated globalDependencies must be ignored: %v", got)
		}
	}
	if m := g.PackageAt("apps/api").Manifests; !contains(m, "apps/api/turbo.json") {
		t.Errorf("package configuration is not a member manifest: %v", m)
	}
	if !contains(g.Resolvers, "turbo") || !contains(g.Resolvers, "npm") {
		t.Errorf("resolvers = %v", g.Resolvers)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestLegacyPipeline(t *testing.T) {
	files := map[string]string{
		"package.json":          `{"workspaces":["apps/*"]}`,
		"turbo.json":            `{"pipeline":{"web#build":{"dependsOn":["ui#build"]}},"globalDependencies":["tsconfig.json"]}`,
		"apps/web/package.json": `{"name":"web"}`,
		"apps/ui/package.json":  `{"name":"ui"}`,
	}
	g := rt.Resolve(t, tree.NewMem(files))
	if got := graph.SortedKeys(g.Closure([]string{"apps/web"})); !reflect.DeepEqual(got, []string{"apps/ui", "apps/web"}) {
		t.Errorf("closure = %v", got)
	}
}

func TestWidening(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"unparseable turbo.json":   {"package.json": `{}`, "turbo.json": `{"tasks":`},
		"wrong dependsOn type":     {"package.json": `{}`, "turbo.json": `{"tasks":{"build":{"dependsOn":"x"}}}`},
		"glob escapes the repo":    {"package.json": `{}`, "turbo.json": `{"globalDependencies":["../x"]}`},
		"extglob globalDependency": {"package.json": `{}`, "turbo.json": `{"globalDependencies":["@(a|b).json"]}`},
		"unparseable package configuration": {
			"package.json": `{"workspaces":["a"]}`, "turbo.json": `{}`, "a/package.json": `{}`, "a/turbo.json": `{`,
		},
		"broken js workspace": {"package.json": `{"workspaces":["a"]}`, "turbo.json": `{}`, "a/package.json": `{`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := turbo.New().Resolve(context.Background(), tree.NewMem(files)); err == nil {
				t.Errorf("want an error")
			}
		})
	}
	tr := tree.NewMem(map[string]string{"package.json": `{}`, "turbo.json": `{}`}).WithBudget(&tree.Budget{MaxFiles: 1})
	if _, err := turbo.New().Resolve(context.Background(), tr); err == nil {
		t.Errorf("budget: want an error")
	}
}

func TestAnchor(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(repo))
	r := turbo.New()
	tests := []struct {
		script string
		dir    string
		kind   resolver.AnchorKind
		dirs   []string
	}{
		{"turbo run build --filter=web", "", resolver.Anchored, []string{"apps/web"}},
		{"pnpm turbo run build --filter=web", "", resolver.Anchored, []string{"apps/web"}},
		{"npx turbo run build --filter web", "", resolver.Anchored, []string{"apps/web"}},
		{"turbo run build -F api", "", resolver.Anchored, []string{"apps/api"}},
		{"turbo build --filter=web...", "", resolver.Anchored, []string{"apps/web"}},
		{"turbo build --filter=...schema", "", resolver.Anchored, []string{"apps/api", "packages/schema"}},
		{"turbo build --filter=./apps/*", "", resolver.Anchored, []string{"apps/api", "apps/web"}},
		{"turbo build --filter=./apps/web", "packages/docs", resolver.Anchored, []string{"apps/web"}},
		{"turbo run build lint --filter=web --filter=api", "", resolver.Anchored, []string{"apps/api", "apps/web"}},
		{"turbo run build --scope=web", "", resolver.Anchored, []string{"apps/web"}},
		{"turbo prune web --docker", "", resolver.Anchored, []string{"apps/web"}},
		{"turbo prune --scope=api --docker", "", resolver.Anchored, []string{"apps/api"}},
		{"turbo prune web api", "", resolver.Anchored, []string{"apps/api", "apps/web"}},
		{"turbo prune", "", resolver.Unknown, nil},
		{"turbo run build", "", resolver.Unknown, nil},
		{"turbo run build", "apps/web", resolver.Anchored, []string{"apps/web"}},
		{"turbo run build", "?", resolver.Unknown, nil},
		{"turbo run build --affected", "", resolver.Unknown, nil},
		{"turbo run build --filter=[main]", "", resolver.Unknown, nil},
		{"turbo run build --filter=!web", "", resolver.Unknown, nil},
		{"turbo run build --filter=$APP", "", resolver.Unknown, nil},
		{"turbo run build --filter=nope", "", resolver.Unknown, nil},
		{"turbo run build --cwd apps/web", "", resolver.Anchored, []string{"apps/web"}},
		{"turbo login", "", resolver.Neutral, nil},
		{"turbo telemetry disable", "", resolver.Neutral, nil},
		{"turbo", "", resolver.Neutral, nil},
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
			if a.Kind == resolver.Unknown && a.Reason == "" {
				t.Errorf("no reason")
			}
		})
	}
}

func TestPathspecsDetect(t *testing.T) {
	r := turbo.New()
	for p, want := range map[string]bool{"turbo.json": true, "apps/x/turbo.json": true, "turbo.jsonc": true, "apps/x/package.json": true, "node_modules/x/turbo.json": false, "README.md": false} {
		if r.Pathspecs(p) != want {
			t.Errorf("Pathspecs(%q) != %v", p, want)
		}
	}
	if r.Detect(tree.NewMem(map[string]string{"apps/x/turbo.json": "{}"})) || !r.Detect(tree.NewMem(map[string]string{"turbo.jsonc": "{}"})) {
		t.Errorf("Detect looks at the root configuration only")
	}
}
