package js_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/js"
	rt "github.com/coollabsio/coolify-helper/src/affected/internal/resolver/resolvertest"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

func resolveJS(t *testing.T, files map[string]string) *graph.Graph {
	t.Helper()
	g, err := js.New().Resolve(context.Background(), tree.NewMem(files))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	g.Normalize()
	return g
}

func resolveErr(t *testing.T, files map[string]string) error {
	t.Helper()
	_, err := js.New().Resolve(context.Background(), tree.NewMem(files))
	if err == nil {
		t.Fatalf("Resolve: want an error")
	}
	return err
}

func closure(g *graph.Graph, anchors ...string) map[string][]string { return g.Closure(anchors) }

func globals(g *graph.Graph) []string {
	var out []string
	for _, gl := range g.Globals {
		out = append(out, string(gl.Kind)+":"+gl.Path+gl.Pattern)
	}
	sort.Strings(out)
	return out
}

var pnpmRepo = map[string]string{
	"package.json":          `{"name":"root","private":true,"devDependencies":{"turbo":"^2"}}`,
	"pnpm-workspace.yaml":   "packages:\n  - 'apps/*'\n  - 'packages/*'\n  - '!packages/legacy'\n",
	"pnpm-lock.yaml":        "lockfileVersion: '9.0'\n",
	".npmrc":                "auto-install-peers=true\n",
	"tsconfig.base.json":    "{}",
	"apps/web/package.json": `{"name":"@acme/web","dependencies":{"@acme/ui":"workspace:*","react":"^18"}}`,
	"apps/web/src/index.ts": "",
	"apps/api/package.json": `{"name":"@acme/api","dependencies":{"@acme/utils":"workspace:^"}}`,
	"apps/api/.npmrc":       "x=y\n",
	"apps/docs/README.md":   "no manifest: not a member",
	"packages/ui/package.json": `{"name":"@acme/ui","dependencies":{"@acme/utils":"^1.0.0"},
		"scripts":{"build":"tsc"}}`,
	"packages/utils/package.json":             `{"name":"@acme/utils","scripts":{"postinstall":"node gen.js"}}`,
	"packages/legacy/package.json":            `{"name":"@acme/legacy"}`,
	"packages/ui/node_modules/x/package.json": `{"name":"x"}`,
	"node_modules/y/package.json":             `{"name":"y"}`,
	"packages/.hidden/package.json":           `{"name":"hidden"}`,
}

func TestPnpmTransitive(t *testing.T) {
	g := resolveJS(t, pnpmRepo)
	if got, want := rt.Dirs(g), []string{"apps/api", "apps/web", "packages/ui", "packages/utils"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(g.Resolvers, []string{"pnpm"}) {
		t.Errorf("resolvers = %v", g.Resolvers)
	}
	c := closure(g, "apps/web")
	want := map[string][]string{
		"apps/web":       {"apps/web (@acme/web)"},
		"packages/ui":    {"apps/web (@acme/web)", "@acme/ui"},
		"packages/utils": {"apps/web (@acme/web)", "@acme/ui", "@acme/utils"},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("closure = %v, want %v", c, want)
	}
	if !g.PackageAt("packages/utils").Lifecycle || g.PackageAt("packages/ui").Lifecycle {
		t.Errorf("lifecycle: utils must be set (postinstall), ui not (build)")
	}
	if got := g.PackageAt("apps/api").Manifests; !reflect.DeepEqual(got, []string{"apps/api/.npmrc", "apps/api/package.json"}) {
		t.Errorf("api manifests = %v", got)
	}
	if !reflect.DeepEqual(g.Lockfiles, []string{"pnpm-lock.yaml"}) {
		t.Errorf("lockfiles = %v", g.Lockfiles)
	}
	wantGlobals := []string{"file:.npmrc", "file:package.json", "file:pnpm-lock.yaml", "file:pnpm-workspace.yaml", "glob:tsconfig*.json"}
	if got := globals(g); !reflect.DeepEqual(got, wantGlobals) {
		t.Errorf("globals = %v, want %v", got, wantGlobals)
	}
	if d := g.Dependents("packages/utils"); !reflect.DeepEqual(d, []string{"apps/api", "apps/web", "packages/ui"}) {
		t.Errorf("dependents = %v", d)
	}
}

func TestReadBatches(t *testing.T) {
	tr := &rt.Counting{Tree: tree.NewMem(pnpmRepo)}
	if _, err := js.Load(context.Background(), tr); err != nil {
		t.Fatal(err)
	}
	if len(tr.Batches) != 2 {
		t.Fatalf("batches = %v, want 2 (root manifests, then members)", tr.Batches)
	}
	if !reflect.DeepEqual(tr.Batches[0], []string{"package.json", "pnpm-workspace.yaml"}) {
		t.Errorf("first batch = %v", tr.Batches[0])
	}
	// Load is kept for the last tree: the turbo and nx resolvers read nothing again.
	if _, err := js.Load(context.Background(), tr); err != nil || len(tr.Batches) != 2 {
		t.Errorf("second Load read again: %v", tr.Batches)
	}
}

func TestPackageManagers(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		pms   []string
		dirs  []string
	}{
		{"npm workspaces array", map[string]string{
			"package.json":             `{"workspaces":["packages/*","./tools/cli"]}`,
			"package-lock.json":        "{}",
			"packages/a/package.json":  `{"name":"a"}`,
			"tools/cli/package.json":   `{"name":"cli"}`,
			"tools/other/package.json": `{"name":"other"}`,
		}, []string{"npm"}, []string{"packages/a", "tools/cli"}},
		{"yarn berry object form", map[string]string{
			"package.json":            `{"packageManager":"yarn@4.1.0","workspaces":{"packages":["apps/*"],"nohoist":["**/x"]}}`,
			"yarn.lock":               "__metadata:\n",
			".yarnrc.yml":             "nodeLinker: node-modules\n",
			".yarn/releases/yarn.cjs": "",
			"apps/a/package.json":     `{"name":"a"}`,
		}, []string{"yarn"}, []string{"apps/a"}},
		{"bun globstar", map[string]string{
			"package.json":              `{"workspaces":["packages/**"]}`,
			"bun.lock":                  "{}",
			"packages/package.json":     `{"name":"group"}`,
			"packages/a/package.json":   `{"name":"a"}`,
			"packages/a/b/package.json": `{"name":"b"}`,
		}, []string{"bun"}, []string{"packages", "packages/a", "packages/a/b"}},
		{"default npm, no workspaces", map[string]string{"package.json": `{"name":"single"}`}, []string{"npm"}, nil},
		{"pnpm braces and classes", map[string]string{
			"pnpm-workspace.yaml":   "packages: ['{apps,libs}/*', 'tools/[ab]*']\n",
			"apps/x/package.json":   `{}`,
			"libs/y/package.json":   `{}`,
			"tools/a1/package.json": `{}`,
			"tools/c1/package.json": `{}`,
		}, []string{"pnpm"}, []string{"apps/x", "libs/y", "tools/a1"}},
		{"pnpm workspace yaml without packages", map[string]string{
			"package.json":        `{}`,
			"pnpm-workspace.yaml": "catalog:\n  react: ^18\n",
			"a/package.json":      `{}`,
		}, []string{"pnpm"}, nil},
		{"pnpm yaml wins with a pnpm lockfile", map[string]string{
			"package.json":         `{"workspaces":["other/*"]}`,
			"pnpm-workspace.yaml":  "packages: [apps/*]\n",
			"pnpm-lock.yaml":       "",
			"apps/a/package.json":  `{}`,
			"other/b/package.json": `{}`,
		}, []string{"pnpm"}, []string{"apps/a"}},
		{"package.json wins with a yarn lockfile", map[string]string{
			"package.json":         `{"workspaces":["other/*"]}`,
			"pnpm-workspace.yaml":  "packages: [apps/*]\n",
			"yarn.lock":            "",
			"apps/a/package.json":  `{}`,
			"other/b/package.json": `{}`,
		}, []string{"pnpm", "yarn"}, []string{"other/b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := resolveJS(t, tt.files)
			if !reflect.DeepEqual(g.Resolvers, tt.pms) {
				t.Errorf("resolvers = %v, want %v", g.Resolvers, tt.pms)
			}
			if got := rt.Dirs(g); !reflect.DeepEqual(got, tt.dirs) {
				t.Errorf("members = %v, want %v", got, tt.dirs)
			}
		})
	}
}

func TestYarnBerryGlobals(t *testing.T) {
	g := resolveJS(t, map[string]string{
		"package.json":            `{"packageManager":"yarn@4.1.0","workspaces":["apps/*"]}`,
		"yarn.lock":               "",
		".yarnrc.yml":             "",
		".yarn/releases/yarn.cjs": "",
		".nvmrc":                  "20",
		"apps/a/package.json":     `{"name":"a","dependencies":{"left-pad":"patch:left-pad@npm%3A1.3.0#~/.yarn/patches/left-pad.patch"}}`,
	})
	want := []string{"dir:.yarn", "file:.nvmrc", "file:.yarn/patches/left-pad.patch", "file:.yarnrc.yml", "file:package.json", "file:yarn.lock"}
	if got := globals(g); !reflect.DeepEqual(got, want) {
		t.Errorf("globals = %v, want %v", got, want)
	}
}

func TestSpecifiers(t *testing.T) {
	g := resolveJS(t, map[string]string{
		"package.json": `{"workspaces":["apps/*","packages/*"],"pnpm":{"patchedDependencies":{"x@1":"patches/x.patch"}}}`,
		"apps/web/package.json": `{"name":"web","dependencies":{
			"ui-alias":"npm:@acme/ui@^1.0.0",
			"linked":"file:../../packages/linked",
			"portal":"link:../../packages/portal/dist",
			"rel":"../../packages/rel",
			"ws-alias":"workspace:@acme/wsalias@*",
			"outside":"file:../../vendor/lib",
			"abs":"file:/opt/x",
			"escape":"file:../../../x"
		},"devDependencies":{"@acme/dev":"*"},"peerDependencies":{"@acme/peer":">=1"},"optionalDependencies":{"@acme/opt":"1.0.0"}}`,
		"packages/ui/package.json":      `{"name":"@acme/ui"}`,
		"packages/linked/package.json":  `{"name":"linked-real-name"}`,
		"packages/portal/package.json":  `{"name":"portal-x"}`,
		"packages/rel/package.json":     `{"name":"rel-x"}`,
		"packages/wsalias/package.json": `{"name":"@acme/wsalias"}`,
		"packages/dev/package.json":     `{"name":"@acme/dev"}`,
		"packages/peer/package.json":    `{"name":"@acme/peer"}`,
		"packages/opt/package.json":     `{"name":"@acme/opt"}`,
		"packages/far/package.json":     `{"name":"@acme/far"}`,
		"vendor/lib/package.json":       `{"name":"lib","dependencies":{"@acme/far":"*"}}`,
	})
	got := graph.SortedKeys(closure(g, "apps/web"))
	want := []string{"apps/web", "packages/dev", "packages/far", "packages/linked", "packages/opt", "packages/peer", "packages/portal", "packages/rel", "packages/ui", "packages/wsalias"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("closure = %v\nwant      %v", got, want)
	}
	found := false
	for _, gl := range g.Globals {
		if gl.Path == "patches/x.patch" {
			found = true
		}
	}
	if !found {
		t.Errorf("patched dependency file is not global: %v", globals(g))
	}
}

func TestOverridesAndNested(t *testing.T) {
	g := resolveJS(t, map[string]string{
		"package.json":                      `{"workspaces":["apps/*","packages/*"],"overrides":{"x":{"y":"file:./packages/forked"}}}`,
		"apps/web/package.json":             `{"name":"web"}`,
		"packages/forked/package.json":      `{"name":"y"}`,
		"packages/group/package.json":       `{"name":"group","workspaces":["*"]}`,
		"packages/group/inner/package.json": `{"name":"inner"}`,
	})
	if got := graph.SortedKeys(closure(g, "apps/web")); !reflect.DeepEqual(got, []string{"apps/web", "packages/forked"}) {
		t.Errorf("override edge: closure = %v", got)
	}
	if got := graph.SortedKeys(closure(g, "packages/group")); !reflect.DeepEqual(got, []string{"packages/forked", "packages/group", "packages/group/inner"}) {
		t.Errorf("nested worktree: closure = %v", got)
	}
}

// Every widening path of the resolver fails Resolve.
func TestWidening(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"unparseable root manifest", map[string]string{"package.json": `{"workspaces":`}, "package.json"},
		{"root manifest not an object", map[string]string{"package.json": `[]`}, "package.json"},
		{"bad workspaces field", map[string]string{"package.json": `{"workspaces":"apps/*"}`}, "workspaces"},
		{"unparseable member manifest", map[string]string{
			"package.json": `{"workspaces":["apps/*"]}`, "apps/a/package.json": `{"name": "a",<<<<<<< HEAD`,
		}, "apps/a/package.json"},
		{"member name not a string", map[string]string{
			"package.json": `{"workspaces":["apps/*"]}`, "apps/a/package.json": `{"name": 1}`,
		}, "apps/a/package.json"},
		{"bad dependencies", map[string]string{
			"package.json": `{"workspaces":["apps/*"]}`, "apps/a/package.json": `{"dependencies": ["x"]}`,
		}, "dependencies"},
		{"symlinked member manifest", map[string]string{
			"package.json": `{"workspaces":["apps/*"]}`, "apps/a/package.json": "symlink:../b/package.json",
		}, "apps/a/package.json"},
		{"unparseable pnpm-workspace.yaml", map[string]string{"pnpm-workspace.yaml": "packages: [a\n"}, "pnpm-workspace.yaml"},
		{"pnpm-workspace.yaml wrong type", map[string]string{"pnpm-workspace.yaml": "packages: a/*\n"}, "pnpm-workspace.yaml"},
		{"extglob pattern", map[string]string{
			"package.json": `{"workspaces":["apps/@(a|b)"]}`, "apps/a/package.json": `{}`,
		}, "extglob"},
		{"escaped pattern", map[string]string{
			"package.json": `{"workspaces":["apps/\\*"]}`, "apps/a/package.json": `{}`,
		}, "escapes"},
		{"ambiguous configuration", map[string]string{
			"package.json": `{"workspaces":["other/*"]}`, "pnpm-workspace.yaml": "packages: [apps/*]\n",
			"apps/a/package.json": `{}`, "other/b/package.json": `{}`,
		}, "ambiguous"},
		{"unparseable linked package", map[string]string{
			"package.json": `{"workspaces":["apps/*"]}`, "apps/a/package.json": `{"dependencies":{"v":"file:../../vendor/v"}}`,
			"vendor/v/package.json": `{`,
		}, "vendor/v/package.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := resolveErr(t, tt.files)
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestBudget(t *testing.T) {
	files := map[string]string{"package.json": `{"workspaces":["apps/*"]}`}
	for _, d := range []string{"a", "b", "c"} {
		files["apps/"+d+"/package.json"] = `{"name":"` + d + `"}`
	}
	tr := tree.NewMem(files).WithBudget(&tree.Budget{MaxFiles: 2})
	_, err := js.New().Resolve(context.Background(), tr)
	if !errors.Is(err, tree.ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
}

func TestLifecycleUnreadableScripts(t *testing.T) {
	g := resolveJS(t, map[string]string{
		"package.json":        `{"workspaces":["apps/*"]}`,
		"apps/a/package.json": `{"scripts":"weird"}`,
		"apps/b/package.json": `{"scripts":{"prepare":"husky"}}`,
	})
	if !g.PackageAt("apps/a").Lifecycle || !g.PackageAt("apps/b").Lifecycle {
		t.Errorf("lifecycle must be set for unreadable scripts and prepare")
	}
}

func TestPathspecs(t *testing.T) {
	r := js.New()
	for p, want := range map[string]bool{
		"package.json": true, "apps/web/package.json": true, "pnpm-workspace.yaml": true, ".npmrc": true,
		"apps/web/.npmrc": true, "turbo.json": true, ".nvmrc": true, "node_modules/x/package.json": false,
		"apps/web/node_modules/x/package.json": false, "pnpm-lock.yaml": false, "yarn.lock": false, "apps/x/pnpm-workspace.yaml": false,
		"apps/web/src/index.ts": false,
	} {
		if got := r.Pathspecs(p); got != want {
			t.Errorf("Pathspecs(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	r := js.New()
	if !r.Detect(tree.NewMem(map[string]string{"package.json": "{}"})) || !r.Detect(tree.NewMem(map[string]string{"pnpm-workspace.yaml": ""})) {
		t.Error("Detect must find package.json or pnpm-workspace.yaml at the root")
	}
	if r.Detect(tree.NewMem(map[string]string{"apps/web/package.json": "{}"})) {
		t.Error("Detect must ignore nested manifests without a root")
	}
}

func TestGlobalKinds(t *testing.T) {
	g := resolveJS(t, pnpmRepo)
	for _, gl := range g.Globals {
		if gl.Kind == match.Glob && gl.Pattern == "" || gl.Kind != match.Glob && gl.Path == "" {
			t.Errorf("malformed global %+v", gl)
		}
	}
}
