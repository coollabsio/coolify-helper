package triggerset

import (
	"context"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

func build(t *testing.T, files map[string]string, a api.Application) *Resolved {
	t.Helper()
	if a.ID == "" {
		a.ID = "app"
	}
	a.Mode = api.ModeSmart
	tr := tree.NewMem(files)
	ws := ResolveWorkspace(context.Background(), tr, []*api.Application{&a}, nil)
	rs, err := Build(context.Background(), tr, ws, &a)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func str(s string) *string { return &s }

func expect(t *testing.T, rs *Resolved, in, out []string) {
	t.Helper()
	for _, p := range in {
		if !rs.Matcher.Match(p) {
			t.Errorf("%s is excluded, want included (widened %v)", p, rs.TriggerSet.Widened)
		}
	}
	for _, p := range out {
		if rs.Matcher.Match(p) {
			t.Errorf("%s is included, want excluded", p)
		}
	}
}

func TestSymlinkInParentOfCopySource(t *testing.T) {
	rs := build(t, map[string]string{
		"Dockerfile":         "FROM x\nCOPY lib/foo /x\n",
		"lib":                "symlink:vendor/lib",
		"vendor/lib/foo/a.c": "1",
		"docs/x.md":          "1",
	}, api.Application{BuildPack: api.BuildPackDockerfile, DockerfileLocation: str("Dockerfile")})
	expect(t, rs, []string{"vendor/lib/foo/a.c"}, []string{"docs/x.md"})
}

func TestComposeSymlinkTargetOutsideBaseDir(t *testing.T) {
	rs := build(t, map[string]string{
		"deploy/compose.yaml":   "services:\n  web:\n    image: nginx\n    volumes:\n      - ./conf:/etc/nginx/conf.d\n",
		"deploy/conf":           "symlink:../shared/conf",
		"shared/conf/site.conf": "1",
		"other/x":               "1",
	}, api.Application{BuildPack: api.BuildPackDockerCompose, BaseDirectory: "deploy", DockerComposeLocation: str("compose.yaml")})
	expect(t, rs, []string{"shared/conf/site.conf", "deploy/compose.yaml"}, []string{"other/x"})
}

func TestSymlinkedBaseDirectory(t *testing.T) {
	for _, pack := range []string{api.BuildPackNixpacks, api.BuildPackDockerfile} {
		rs := build(t, map[string]string{
			"apps/web":               "symlink:web-v2",
			"apps/web-v2/index.js":   "1",
			"apps/web-v2/Dockerfile": "FROM x\nCOPY index.js /\n",
		}, api.Application{BuildPack: pack, BaseDirectory: "apps/web", DockerfileLocation: str("Dockerfile")})
		expect(t, rs, []string{"apps/web-v2/index.js"}, nil)
	}
}

func TestManagedConfigIsInEveryLimit(t *testing.T) {
	rs := build(t, map[string]string{
		"apps/web/Dockerfile": "FROM x\nCOPY src /src\n",
		"apps/web/src/a":      "1",
		"coolify.json":        "{}",
	}, api.Application{BuildPack: api.BuildPackDockerfile, BaseDirectory: "apps/web", DockerfileLocation: str("Dockerfile"), ManagedConfigPaths: []string{"coolify.json"}})
	expect(t, rs, []string{"coolify.json", "apps/web/src/a"}, []string{"apps/web/README.md"})
}

func TestDockerfileCopyBeatsWorkspaceExcludes(t *testing.T) {
	// The Dockerfile copies a root meta file and a member outside web's closure:
	// both must stay in the set although the workspace limit excludes them.
	rs := build(t, map[string]string{
		"package.json":                 `{"name":"root","workspaces":["apps/*","packages/*"]}`,
		"apps/web/package.json":        `{"name":"web"}`,
		"apps/web/Dockerfile":          "FROM node:22\nCOPY CHANGELOG.md packages/other apps/web /app/\nRUN npm run build -w web\n",
		"apps/web/index.js":            "1",
		"packages/other/package.json":  `{"name":"other"}`,
		"packages/other/x.js":          "1",
		"packages/unused/package.json": `{"name":"unused"}`,
		"packages/unused/x.js":         "1",
		"CHANGELOG.md":                 "1",
		"README.md":                    "1",
	}, api.Application{BuildPack: api.BuildPackDockerfile, DockerfileLocation: str("apps/web/Dockerfile")})
	if len(rs.TriggerSet.Anchors) == 0 {
		t.Fatalf("no anchor: %v", rs.TriggerSet.Widened)
	}
	expect(t, rs, []string{"CHANGELOG.md", "packages/other/x.js", "apps/web/index.js"}, []string{"README.md", "packages/unused/x.js"})
}

func TestInputPatterns(t *testing.T) {
	files := map[string]string{
		"package.json":          `{"name":"root","workspaces":["apps/*"]}`,
		"apps/web/package.json": `{"name":"web"}`,
		"apps/web/Dockerfile":   "FROM node:22\nCOPY src /src\n",
		"apps/web/src/index.js": "1",
		"apps/web/shared":       "symlink:../../shared",
		"shared/x.js":           "1",
		"stack/compose.yaml":    "include:\n  - ./extra.yaml\nservices:\n  a:\n    image: nginx\n",
		"stack/extra.yaml":      "services:\n  b:\n    image: redis\n",
		"stack/notes.txt":       "1",
	}
	m := func(t *testing.T, a api.Application) *match.Matcher {
		t.Helper()
		rs := build(t, files, a)
		mt, err := match.Compile(rs.TriggerSet.InputPatterns)
		if err != nil {
			t.Fatal(err)
		}
		return mt
	}
	check := func(t *testing.T, mt *match.Matcher, in, out []string) {
		t.Helper()
		for _, p := range in {
			if !mt.Match(p) {
				t.Errorf("%s is not an input", p)
			}
		}
		for _, p := range out {
			if mt.Match(p) {
				t.Errorf("%s is an input", p)
			}
		}
	}
	t.Run("dockerfile in a workspace", func(t *testing.T) {
		mt := m(t, api.Application{BuildPack: api.BuildPackDockerfile, BaseDirectory: "apps/web", DockerfileLocation: str("Dockerfile")})
		check(t, mt,
			[]string{"apps/web/Dockerfile", "apps/web/.dockerignore", "apps/web/Dockerfile.dockerignore", "apps/web/dockerfile",
				"package.json", "apps/new/package.json", "pnpm-lock.yaml", "packages/x/project.json", "turbo.json", "nx.json",
				"apps/web/shared", "apps/web", "apps", "MODULE.bazel", "a\nb/c.txt"},
			[]string{"apps/web/src/index.js", "shared/x.js", "README.md"})
	})
	t.Run("compose includes", func(t *testing.T) {
		mt := m(t, api.Application{BuildPack: api.BuildPackDockerCompose, BaseDirectory: "stack", DockerComposeLocation: str("compose.yaml")})
		check(t, mt, []string{"stack/compose.yaml", "stack/extra.yaml", "stack/.env"}, []string{"stack/notes.txt"})
	})
}
