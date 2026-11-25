package triggerset

import (
	"context"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
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
