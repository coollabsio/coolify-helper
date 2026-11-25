package workspace

import (
	"reflect"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/all"
	rt "github.com/coollabsio/coolify-helper/src/affected/internal/resolver/resolvertest"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

var monorepo = map[string]string{
	"package.json":                        `{"name":"root","private":true}`,
	"pnpm-workspace.yaml":                 "packages: ['apps/*', 'packages/*', 'apps/web/examples/*']\n",
	"pnpm-lock.yaml":                      "",
	"turbo.json":                          `{"tasks":{"build":{}}}`,
	"README.md":                           "",
	"Dockerfile":                          "",
	"docker/web.Dockerfile":               "",
	"apps/web/package.json":               `{"name":"@acme/web","dependencies":{"@acme/ui":"workspace:*"}}`,
	"apps/web/Dockerfile":                 "",
	"apps/web/docker/Dockerfile":          "",
	"apps/web/examples/demo/package.json": `{"name":"demo","dependencies":{"@acme/web":"workspace:*"}}`,
	"apps/api/package.json":               `{"name":"api","dependencies":{"@acme/utils":"workspace:*"}}`,
	"apps/api/Dockerfile":                 "",
	"packages/ui/package.json":            `{"name":"@acme/ui","dependencies":{"@acme/utils":"workspace:*"}}`,
	"packages/utils/package.json":         `{"name":"@acme/utils","scripts":{"postinstall":"node x.js"}}`,
	"packages/cfg/package.json":           `{"name":"@acme/cfg"}`,
}

func monoGraph(t *testing.T) *graph.Graph {
	t.Helper()
	return rt.Resolve(t, tree.NewMem(monorepo))
}

func TestFindAnchor(t *testing.T) {
	g := monoGraph(t)
	rs := all.All()
	tests := []struct {
		name   string
		in     AnchorInput
		dirs   []string
		source string
		res    string
		reason string
	}{
		{"dockerfile in a member", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile"},
			[]string{"apps/web"}, SourceDockerfile, "pnpm", ""},
		{"dockerfile nested in a member", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/docker/Dockerfile"},
			[]string{"apps/web"}, SourceDockerfile, "pnpm", ""},
		{"dockerfile in a nested member", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/examples/demo/Dockerfile"},
			[]string{"apps/web/examples/demo"}, SourceDockerfile, "pnpm", ""},
		{"dockerfile location widened by RUN", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile",
			RunCommands: []string{"npm i -g turbo", "turbo prune api --docker", "pnpm install"}},
			[]string{"apps/api", "apps/web"}, SourceDockerfile, "pnpm", ""},
		// A RUN command in an unknown WORKDIR may be a root script that builds anything.
		{"dockerfile location ruled out by an unknown RUN", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile",
			RunCommands: []string{"pnpm install", "pnpm build"}},
			nil, "", "", "can't follow"},
		{"dockerfile location ruled out by a foreign RUN", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile",
			RunCommands: []string{"apk add --no-cache git", "node scripts/gen.mjs"}},
			nil, "", "", "runs node"},
		{"dockerfile location widened by the application's commands", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile",
			Commands: []string{"pnpm --filter api build"}},
			[]string{"apps/api", "apps/web"}, SourceDockerfile, "pnpm", ""},
		{"dockerfile location with a variable filter", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile",
			RunCommands: []string{"pnpm turbo run build --filter=$APP"}},
			nil, "", "", "variable"},
		{"root dockerfile falls through to RUN commands", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "Dockerfile",
			RunCommands: []string{"corepack enable", "pnpm install --frozen-lockfile", "pnpm turbo run build --filter=@acme/web..."}},
			[]string{"apps/web"}, SourceCommands, "turbo", ""},
		{"root dockerfile without telling commands", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "Dockerfile",
			RunCommands: []string{"pnpm install", "pnpm build"}},
			nil, "", "", "can't follow"},
		{"dockerfile outside members, base dir in a member", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "docker/web.Dockerfile", BaseDir: "apps/api"},
			[]string{"apps/api"}, SourceBaseDir, "pnpm", ""},
		{"compose services union", AnchorInput{BuildPack: "dockercompose", Services: []ComposeService{
			{Name: "web", ContextDir: "", DockerfilePath: "apps/web/Dockerfile"},
			{Name: "api", ContextDir: "apps/api", DockerfilePath: "docker/api.Dockerfile"},
		}}, []string{"apps/api", "apps/web"}, SourceCompose, "pnpm", ""},
		{"compose service anchored by RUN", AnchorInput{BuildPack: "dockercompose", Services: []ComposeService{
			{Name: "web", ContextDir: "apps/web", DockerfilePath: "apps/web/Dockerfile"},
			{Name: "worker", ContextDir: "", DockerfilePath: "docker/worker.Dockerfile", RunCommands: []string{"pnpm --filter api build"}},
		}}, []string{"apps/api", "apps/web"}, SourceCompose, "pnpm", ""},
		{"compose service without anchor falls through", AnchorInput{BuildPack: "dockercompose", BaseDir: "", Services: []ComposeService{
			{Name: "web", ContextDir: "apps/web", DockerfilePath: "apps/web/Dockerfile"},
			{Name: "worker", ContextDir: "", DockerfilePath: "docker/worker.Dockerfile", RunCommands: []string{"pnpm build"}},
		}}, nil, "", "", "can't follow"},
		{"compose without services uses commands", AnchorInput{BuildPack: "dockercompose", Commands: []string{"pnpm --filter api build"}},
			[]string{"apps/api"}, SourceCommands, "pnpm", ""},
		{"base directory in a member", AnchorInput{BuildPack: "nixpacks", BaseDir: "apps/web"}, []string{"apps/web"}, SourceBaseDir, "pnpm", ""},
		{"base directory widened by the commands", AnchorInput{BuildPack: "nixpacks", BaseDir: "apps/web", Commands: []string{
			"pnpm install", "cd ../.. && pnpm turbo run build --filter=@acme/web --filter=api", "pnpm start"}},
			[]string{"apps/api", "apps/web"}, SourceBaseDir, "pnpm", ""},
		{"base directory ruled out by an unknown command", AnchorInput{BuildPack: "nixpacks", BaseDir: "apps/web", Commands: []string{
			"pnpm install", "pnpm build", "node dist/server.js"}},
			nil, "", "", "runs node"},
		{"base directory ruled out by a root script", AnchorInput{BuildPack: "nixpacks", BaseDir: "apps/web", Commands: []string{"cd ../.. && pnpm build"}},
			nil, "", "", "root script"},
		{"compose service location ruled out by its RUN", AnchorInput{BuildPack: "dockercompose", Services: []ComposeService{
			{Name: "web", ContextDir: "apps/web", DockerfilePath: "apps/web/Dockerfile", RunCommands: []string{"npm run build"}},
		}}, nil, "", "", "can't follow"},
		{"compose commands are fine", AnchorInput{BuildPack: "dockercompose", Commands: []string{"docker compose -f compose.yml build", "docker-compose up -d"},
			Services: []ComposeService{{Name: "web", ContextDir: "apps/web", DockerfilePath: "apps/web/Dockerfile"}}},
			[]string{"apps/web"}, SourceCompose, "pnpm", ""},
		{"docker build is not", AnchorInput{BuildPack: "dockercompose", Commands: []string{"docker build -f apps/api/Dockerfile ."},
			Services: []ComposeService{{Name: "web", ContextDir: "apps/web", DockerfilePath: "apps/web/Dockerfile"}}},
			nil, "", "", "runs docker"},
		{"inert commands are ignored", AnchorInput{BuildPack: "nixpacks", Commands: []string{
			"apt-get update && apt-get install -y git", "corepack enable", "npm i -g pnpm@9", "mkdir -p out && chmod +x out",
			"export NODE_ENV=production; echo ok", "pnpm --filter api build"}},
			[]string{"apps/api"}, SourceCommands, "pnpm", ""},
		{"a here-document is read", AnchorInput{BuildPack: "nixpacks", Commands: []string{"bash <<EOF\npnpm --filter api build\nEOF"}},
			[]string{"apps/api"}, SourceCommands, "pnpm", ""},
		{"a shell reading a piped script", AnchorInput{BuildPack: "nixpacks", Commands: []string{`echo "pnpm build" | sh`, "pnpm --filter api build"}},
			nil, "", "", "standard input"},
		{"a shell reading a redirected script", AnchorInput{BuildPack: "nixpacks", Commands: []string{"sh < scripts/build.sh", "pnpm --filter api build"}},
			nil, "", "", "standard input"},
		{"base directory under a member", AnchorInput{BuildPack: "railpack", BaseDir: "apps/web/src"}, []string{"apps/web"}, SourceBaseDir, "pnpm", ""},
		{"base directory above members", AnchorInput{BuildPack: "nixpacks", BaseDir: "apps"}, nil, "", "", "no command"},
		{"commands with filters", AnchorInput{BuildPack: "nixpacks", Commands: []string{
			"pnpm install --frozen-lockfile", "pnpm turbo run build --filter=@acme/web", "pnpm --filter @acme/web start"}},
			[]string{"apps/web"}, SourceCommands, "turbo", ""},
		{"commands union", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter api build && pnpm --filter @acme/web build"}},
			[]string{"apps/api", "apps/web"}, SourceCommands, "pnpm", ""},
		{"commands with cd", AnchorInput{BuildPack: "nixpacks", Commands: []string{"cd apps/api && pnpm build"}},
			[]string{"apps/api"}, SourceCommands, "pnpm", ""},
		{"commands relative to the base directory", AnchorInput{BuildPack: "static", BaseDir: "apps", Commands: []string{"cd web && pnpm build"}},
			[]string{"apps/web"}, SourceCommands, "pnpm", ""},
		{"an unknown command rules out the anchor", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter api build", "pnpm start"}},
			nil, "", "", "root script"},
		{"a recursive run rules out the anchor", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter api build", "pnpm -r build"}},
			nil, "", "", "every package"},
		{"make rules out the anchor", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter api build", "make all"}},
			nil, "", "", "task runner"},
		{"a script rules out the anchor", AnchorInput{BuildPack: "nixpacks", Commands: []string{"./scripts/build.sh && pnpm --filter api build"}},
			nil, "", "", "script"},
		{"bash with a file rules out the anchor", AnchorInput{BuildPack: "nixpacks", Commands: []string{"bash build.sh", "pnpm --filter api build"}},
			nil, "", "", "shell script"},
		{"a piped installer is fine", AnchorInput{BuildPack: "dockerfile", DockerfilePath: "Dockerfile",
			RunCommands: []string{"curl -fsSL https://bun.sh/install | bash", "curl https://sh.rustup.rs | sh -s -- -y", "pnpm --filter api build"}},
			[]string{"apps/api"}, SourceCommands, "pnpm", ""},
		{"a node script", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter @acme/web build && node scripts/bundle-docs.mjs"}},
			nil, "", "", "runs node"},
		{"tsx", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter api build", "npx tsx x.ts"}}, nil, "", "", "runs tsx"},
		{"python", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter api build", "python3 build.py"}}, nil, "", "", "runs python3"},
		{"deno", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm --filter api build", "deno task build"}}, nil, "", "", "runs deno"},
		{"cp", AnchorInput{BuildPack: "nixpacks", Commands: []string{"cp -r packages/docs/content apps/api/content && pnpm --filter api build"}},
			nil, "", "", "runs cp"},
		{"eval", AnchorInput{BuildPack: "nixpacks", Commands: []string{`eval "$BUILD"`, "pnpm --filter api build"}}, nil, "", "", "runs eval"},
		{"concurrently", AnchorInput{BuildPack: "nixpacks", Commands: []string{`npx concurrently "pnpm -F api build" "pnpm -F @acme/web build"`}},
			nil, "", "", "runs concurrently"},
		{"xargs", AnchorInput{BuildPack: "nixpacks", Commands: []string{"echo api | xargs -I{} pnpm --filter {} build"}}, nil, "", "", "runs xargs"},
		{"only install commands", AnchorInput{BuildPack: "nixpacks", Commands: []string{"pnpm install", "echo done"}},
			nil, "", "", "no command"},
		{"no commands", AnchorInput{BuildPack: "nixpacks"}, nil, "", "", "no command"},
		{"nx and turbo names", AnchorInput{BuildPack: "nixpacks", Commands: []string{"turbo prune --scope=@acme/ui"}},
			[]string{"packages/ui"}, SourceCommands, "turbo", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Graph, tt.in.Resolvers = g, rs
			a := FindAnchor(tt.in)
			if !reflect.DeepEqual(a.Dirs, tt.dirs) {
				t.Fatalf("dirs = %v (%s), want %v", a.Dirs, a.Reason, tt.dirs)
			}
			if a.Source != tt.source || a.Resolver != tt.res {
				t.Errorf("source, resolver = %q, %q; want %q, %q", a.Source, a.Resolver, tt.source, tt.res)
			}
			if tt.reason != "" && !strings.Contains(a.Reason, tt.reason) {
				t.Errorf("reason %q does not mention %q", a.Reason, tt.reason)
			}
			if !a.Found() && !strings.HasPrefix(a.Reason, NoAnchor) {
				t.Errorf("reason %q does not start with %q", a.Reason, NoAnchor)
			}
			if a.Found() && len(a.Packages) != len(a.Dirs) {
				t.Errorf("packages = %v", a.Packages)
			}
		})
	}
}

func TestFindAnchorLabels(t *testing.T) {
	a := FindAnchor(AnchorInput{Graph: monoGraph(t), Resolvers: all.All(), BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile"})
	if !reflect.DeepEqual(a.Packages, []string{"@acme/web"}) {
		t.Errorf("packages = %v", a.Packages)
	}
}

func TestFindAnchorNotModeled(t *testing.T) {
	files := map[string]string{"MODULE.bazel": "", "pants.toml": ""}
	for k, v := range monorepo {
		files[k] = v
	}
	g := rt.Resolve(t, tree.NewMem(files))
	a := FindAnchor(AnchorInput{Graph: g, Resolvers: all.All(), BuildPack: "nixpacks", BaseDir: "apps/web"})
	if a.Found() || a.Reason != "Bazel and Pants are not modeled: the workspace limit doesn't apply." {
		t.Errorf("anchor = %+v", a)
	}
}

func TestFindAnchorNoMembers(t *testing.T) {
	g := rt.Resolve(t, tree.NewMem(map[string]string{"package.json": `{"name":"app"}`}))
	a := FindAnchor(AnchorInput{Graph: g, Resolvers: all.All(), BuildPack: "nixpacks", Commands: []string{"npm run build"}})
	if a.Found() || !strings.Contains(a.Reason, "no workspace packages") {
		t.Errorf("anchor = %+v", a)
	}
	if a := FindAnchor(AnchorInput{}); a.Found() || a.Reason == "" {
		t.Errorf("nil graph: %+v", a)
	}
}

func TestFindAnchorRootMember(t *testing.T) {
	// A standalone Nx project at the root never anchors by location.
	g := rt.Resolve(t, tree.NewMem(map[string]string{
		"nx.json": `{}`, "project.json": `{"name":"app"}`, "libs/a/project.json": `{"name":"a"}`,
	}))
	a := FindAnchor(AnchorInput{Graph: g, Resolvers: all.All(), BuildPack: "dockerfile", DockerfilePath: "Dockerfile", RunCommands: []string{"npx nx build app"}})
	if !reflect.DeepEqual(a.Dirs, []string{""}) || a.Source != SourceCommands || a.Resolver != "nx" {
		t.Errorf("anchor = %+v", a)
	}
}
