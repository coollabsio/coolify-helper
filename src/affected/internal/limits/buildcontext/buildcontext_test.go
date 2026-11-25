package buildcontext

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// includes evaluates a ruleset: the last matching rule wins, else the default.
func includes(rs *match.Ruleset, path string) bool {
	in := rs.Default == match.Include
	for _, r := range rs.Rules {
		var m bool
		switch r.Kind {
		case match.Dir:
			m = path == r.Path || strings.HasPrefix(path, r.Path+"/")
		case match.File:
			m = path == r.Path
		case match.Glob:
			m = match.GlobMatch(r.Pattern, path)
		}
		if m {
			in = r.Action == match.Include
		}
	}
	return in
}

func check(t *testing.T, rs *match.Ruleset, in, out []string) {
	t.Helper()
	if rs == nil {
		t.Fatal("no ruleset")
	}
	for _, p := range in {
		if !includes(rs, p) {
			t.Errorf("%q: excluded, want included (rules %+v)", p, rs.Rules)
		}
	}
	for _, p := range out {
		if includes(rs, p) {
			t.Errorf("%q: included, want excluded (rules %+v)", p, rs.Rules)
		}
	}
}

func TestRootBaseDirectory(t *testing.T) {
	tr := tree.NewMem(map[string]string{"a": "", "b/c": ""})
	res := Compile(context.Background(), tr, Input{BaseDir: "", BuildPack: "nixpacks"})
	check(t, res.Ruleset, []string{"a", "b/c", "anything"}, nil)
	if res.Ruleset.Limit != "build_context" || len(res.Widened) != 0 || len(res.Reads) != 0 {
		t.Errorf("unexpected result %+v", res)
	}
}

func TestBaseDirectory(t *testing.T) {
	tr := tree.NewMem(map[string]string{"apps/api/x": "", "apps/web/.dockerignore": "*"})
	for _, pack := range []string{"railpack", "nixpacks", "static", "dockercompose"} {
		res := Compile(context.Background(), tr, Input{BaseDir: "apps/api", BuildPack: pack})
		check(t, res.Ruleset, []string{"apps/api", "apps/api/x", "apps/api/a/b"}, []string{"apps/apix", "apps/web/x", "README.md"})
		if len(res.Reads) != 0 {
			t.Errorf("%s: reads %v", pack, res.Reads)
		}
	}
}

func TestDockerfileWithoutIgnoreFile(t *testing.T) {
	tr := tree.NewMem(map[string]string{"apps/web/Dockerfile": "FROM x", "apps/web/src/a": ""})
	res := Compile(context.Background(), tr, Input{BaseDir: "apps/web", BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile"})
	check(t, res.Ruleset, []string{"apps/web/src/a", "apps/web/Dockerfile"}, []string{"apps/api/a"})
	want := []string{"apps/web/.dockerignore", "apps/web/Dockerfile.dockerignore"}
	if !reflect.DeepEqual(res.Reads, want) {
		t.Errorf("reads %v, want %v", res.Reads, want)
	}
}

func TestGenericIgnoreFile(t *testing.T) {
	tr := tree.NewMem(map[string]string{
		"apps/web/Dockerfile":    "FROM x",
		"apps/web/.dockerignore": "*.md\n!README.md\nnode_modules\nDockerfile\n.dockerignore\n",
	})
	res := Compile(context.Background(), tr, Input{BaseDir: "apps/web", BuildPack: "dockerfile", DockerfilePath: "apps/web/Dockerfile"})
	check(t, res.Ruleset,
		[]string{"apps/web/src/a.ts", "apps/web/README.md", "apps/web/Dockerfile", "apps/web/.dockerignore"},
		[]string{"apps/web/CHANGELOG.md", "apps/web/node_modules/x/index.js", "apps/other/x"})
	if len(res.Widened) != 0 {
		t.Errorf("widened %v", res.Widened)
	}
}

func TestDockerfileSpecificIgnoreFileWins(t *testing.T) {
	tr := tree.NewMem(map[string]string{
		"Dockerfile.web":              "FROM x",
		"Dockerfile.web.dockerignore": "*\n!apps/web\n",
		".dockerignore":               "apps/web\n",
	})
	res := Compile(context.Background(), tr, Input{BaseDir: "", BuildPack: "dockerfile", DockerfilePath: "Dockerfile.web"})
	// The specific file's rules apply (apps/web is in, the rest out). The generic
	// .dockerignore stays included: deleting the specific file switches to it.
	check(t, res.Ruleset,
		[]string{"apps/web/a", "Dockerfile.web", "Dockerfile.web.dockerignore", ".dockerignore"},
		[]string{"apps/api/a", "README.md"})
}

func TestIgnoreCandidatesAndLowercaseDockerfileAreAlwaysIncluded(t *testing.T) {
	// "*" would exclude both: a deleted Dockerfile.dockerignore and BuildKit's
	// fallback to a lowercase dockerfile both change the build.
	tr := tree.NewMem(map[string]string{
		"Dockerfile":    "FROM x",
		".dockerignore": "*\n!src\n",
	})
	res := Compile(context.Background(), tr, Input{BuildPack: "dockerfile", DockerfilePath: "Dockerfile"})
	check(t, res.Ruleset,
		[]string{"src/a", "Dockerfile", "Dockerfile.dockerignore", ".dockerignore", "dockerfile", "dockerfile.dockerignore"},
		[]string{"docs/a"})
}

func TestSymlinkedIgnoreFileOrDockerfileWidens(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"ignore file": {"Dockerfile": "FROM x", ".dockerignore": "symlink:docker/ignore", "docker/ignore": "docs\n"},
		"dockerfile":  {"Dockerfile": "symlink:deploy/Dockerfile.prod", "deploy/Dockerfile.prod": "FROM x", ".dockerignore": "deploy\n"},
	} {
		res := Compile(context.Background(), tree.NewMem(files), Input{BuildPack: "dockerfile", DockerfilePath: "Dockerfile"})
		if len(res.Widened) == 0 {
			t.Errorf("%s: no widening", name)
		}
		check(t, res.Ruleset, []string{"docker/ignore", "deploy/Dockerfile.prod", "docs/a"}, nil)
	}
}

func TestSymlinkedOrMissingBaseDirWidens(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"base is a link":   {"apps/web": "symlink:web-v2", "apps/web-v2/index.js": "1"},
		"parent is a link": {"apps": "symlink:src", "src/web/index.js": "1"},
		"base is missing":  {"other/index.js": "1"},
	} {
		res := Compile(context.Background(), tree.NewMem(files), Input{BaseDir: "apps/web", BuildPack: "nixpacks"})
		if res.Ruleset != nil || len(res.Widened) == 0 {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestDockerfileSpecificIgnoreFileInSubdirectory(t *testing.T) {
	// The Dockerfile-specific ignore file sits next to the Dockerfile, but its
	// patterns are relative to the context.
	tr := tree.NewMem(map[string]string{
		"docker/web.Dockerfile":              "FROM x",
		"docker/web.Dockerfile.dockerignore": "docs\n",
	})
	res := Compile(context.Background(), tr, Input{BaseDir: "", BuildPack: "dockerfile", DockerfilePath: "docker/web.Dockerfile"})
	check(t, res.Ruleset, []string{"src/a", "docker/web.Dockerfile", "docker/web.Dockerfile.dockerignore"}, []string{"docs/a"})
}

func TestUncompilableIgnoreFileWidens(t *testing.T) {
	tr := tree.NewMem(map[string]string{
		"app/Dockerfile":    "FROM x",
		"app/.dockerignore": "*\n![a-z]rc\n",
	})
	res := Compile(context.Background(), tr, Input{BaseDir: "app", BuildPack: "dockerfile", DockerfilePath: "app/Dockerfile"})
	check(t, res.Ruleset, []string{"app/src/a", "app/Dockerfile", "app/README.md"}, []string{"other/a"})
	if len(res.Widened) != 1 || res.Widened[0].Scope != "limit:build_context" || !strings.HasPrefix(res.Widened[0].Reason, "app/.dockerignore: ") {
		t.Errorf("widened %+v", res.Widened)
	}
}

func TestBudgetWidens(t *testing.T) {
	tr := tree.NewMem(map[string]string{
		"app/Dockerfile":    "FROM x",
		"app/.dockerignore": "src\n",
	}).WithBudget(&tree.Budget{MaxBytes: 1})
	res := Compile(context.Background(), tr, Input{BaseDir: "app", BuildPack: "dockerfile", DockerfilePath: "app/Dockerfile"})
	check(t, res.Ruleset, []string{"app/src/a", "app/Dockerfile"}, []string{"other/a"})
	if len(res.Widened) != 1 || !strings.Contains(res.Widened[0].Reason, "cap") {
		t.Errorf("widened %+v", res.Widened)
	}
}

func TestOrigins(t *testing.T) {
	tr := tree.NewMem(map[string]string{"app/Dockerfile": "", "app/.dockerignore": "x\n"})
	res := Compile(context.Background(), tr, Input{BaseDir: "app", BuildPack: "dockerfile", DockerfilePath: "app/Dockerfile"})
	for _, r := range res.Ruleset.Rules {
		if r.Origin == nil || r.Origin.Rule != "build_context" {
			t.Errorf("rule %+v has origin %+v", r, r.Origin)
		}
	}
}
