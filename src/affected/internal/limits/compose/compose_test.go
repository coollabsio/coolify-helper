package compose

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

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

func compile(t *testing.T, files map[string]string, in Input) Output {
	t.Helper()
	return Compile(context.Background(), tree.NewMem(files), in)
}

func mustApply(t *testing.T, out Output) {
	t.Helper()
	if out.Failed || out.Ruleset == nil {
		t.Fatalf("compose limit does not apply: %+v", out.Widened)
	}
	if out.Ruleset.Limit != "compose" || out.Ruleset.Default != match.Exclude {
		t.Fatalf("ruleset %+v", out.Ruleset)
	}
	for _, r := range out.Ruleset.Rules {
		if r.Origin == nil || r.Origin.Rule != "compose" {
			t.Errorf("rule %+v has origin %+v", r, r.Origin)
		}
	}
}

func check(t *testing.T, out Output, in, notIn []string) {
	t.Helper()
	mustApply(t, out)
	for _, p := range in {
		if !includes(out.Ruleset, p) {
			t.Errorf("%q: excluded, want included", p)
		}
	}
	for _, p := range notIn {
		if includes(out.Ruleset, p) {
			t.Errorf("%q: included, want excluded", p)
		}
	}
	if t.Failed() {
		for _, r := range out.Ruleset.Rules {
			t.Logf("  %s %s %s%s", r.Action, r.Kind, r.Path, r.Pattern)
		}
	}
}

func mustFail(t *testing.T, out Output, reason string) {
	t.Helper()
	if !out.Failed || out.Ruleset != nil {
		t.Fatalf("compose limit applies, want failure containing %q", reason)
	}
	last := out.Widened[len(out.Widened)-1]
	if last.Scope != "limit:compose" || !strings.Contains(last.Reason, reason) {
		t.Errorf("widened %+v, want a reason containing %q", out.Widened, reason)
	}
}

func TestSharedContextBetweenServices(t *testing.T) {
	files := map[string]string{
		"docker-compose.yml": `
services:
  web:
    build:
      context: .
      dockerfile: apps/web/Dockerfile
  api:
    build:
      context: .
      dockerfile: apps/api/Dockerfile
  db:
    image: postgres:17
`,
		"apps/web/Dockerfile": "FROM node\nCOPY package.json ./\nCOPY apps/web ./apps/web\nRUN pnpm --filter web build\n",
		"apps/api/Dockerfile": "FROM node\nCOPY package.json ./\nCOPY apps/api ./apps/api\nCOPY packages ./packages\n",
		".dockerignore":       "**/node_modules\n**/*.md\n",
		"package.json":        "{}",
	}
	out := compile(t, files, Input{BaseDir: "", ComposePath: "docker-compose.yml"})
	check(t, out,
		[]string{"package.json", "apps/web/src/a.ts", "apps/api/src/a.ts", "packages/ui/x.ts", "apps/web/Dockerfile", "apps/api/Dockerfile", ".dockerignore", "docker-compose.yml", ".env"},
		[]string{"apps/web/README.md", "apps/web/node_modules/x/y.js", "docs/x", "apps/worker/a", "README.md"})
	if len(out.Widened) != 0 {
		t.Errorf("widened %+v", out.Widened)
	}
	want := []Service{
		{Name: "api", ContextDir: "", DockerfilePath: "apps/api/Dockerfile"},
		{Name: "web", ContextDir: "", DockerfilePath: "apps/web/Dockerfile", RunCommands: []string{"pnpm --filter web build"}},
	}
	if !reflect.DeepEqual(out.Services, want) {
		t.Errorf("services %+v, want %+v", out.Services, want)
	}
}

func TestOverlappingContextsDropExclusions(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  root:
    build: .
  web:
    build: ./web
`,
		"Dockerfile":         "FROM x\nCOPY . .\n",
		".dockerignore":      "web\n",
		"web/Dockerfile":     "FROM x\nCOPY src ./src\n",
		"web/.dockerignore":  "src/*.test.ts\n",
		"web/src/a.ts":       "",
		"web/src/a.test.ts":  "",
		"other/a":            "",
		"web/README.md":      "",
		"Dockerfile.ignored": "",
	}
	out := compile(t, files, Input{BaseDir: "", ComposePath: "compose.yaml"})
	// The root context excludes web/, but the web service sends it: the
	// exclusions of both overlapping services are dropped.
	check(t, out, []string{"web/src/a.ts", "web/src/a.test.ts", "other/a", "web/README.md"}, nil)
	if len(out.Widened) < 2 {
		t.Errorf("want widenings for both services, got %+v", out.Widened)
	}
}

func TestNonOverlappingContextsKeepExclusions(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  web:
    build: ./web
  api:
    build:
      context: ./api
      dockerfile: Dockerfile.prod
`,
		"web/Dockerfile":                   "FROM x\nCOPY . .\n",
		"web/.dockerignore":                "*.md\n!README.md\n",
		"api/Dockerfile.prod":              "FROM x\nCOPY src ./src\n",
		"api/Dockerfile.prod.dockerignore": "src/fixtures\n",
		"api/.dockerignore":                "src\n",
	}
	out := compile(t, files, Input{BaseDir: "", ComposePath: "compose.yaml"})
	check(t, out,
		[]string{"web/a.ts", "web/README.md", "web/.dockerignore", "api/src/a.go", "api/Dockerfile.prod", "api/Dockerfile.prod.dockerignore", "api/.dockerignore"},
		[]string{"web/CHANGELOG.md", "api/src/fixtures/x", "api/other.go", "webx/a"})
}

func TestComposeFileInSubdirectory(t *testing.T) {
	files := map[string]string{
		"apps/stack/deploy/docker-compose.yml": `
services:
  app:
    build:
      context: ./app
    volumes:
      - ./config:/config
`,
		"apps/stack/app/Dockerfile":        "FROM x\nCOPY src ./src\n",
		"apps/stack/deploy/app/Dockerfile": "FROM x\nCOPY lib ./lib\n",
	}
	out := compile(t, files, Input{BaseDir: "apps/stack", ComposePath: "apps/stack/deploy/docker-compose.yml"})
	// Resolved against the base directory (the project directory) and the
	// directory of the Compose file: the union.
	check(t, out,
		[]string{"apps/stack/app/src/a", "apps/stack/deploy/app/lib/a", "apps/stack/config/x", "apps/stack/deploy/config/x", "apps/stack/deploy/docker-compose.yml"},
		[]string{"apps/stack/app/lib/a", "apps/stack/README.md"})
}

func TestInclude(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
include:
  - infra/db.yaml
  - path: services/worker/compose.yaml
    env_file: services/worker/.env.include
  - path:
      - shared/a.yaml
      - shared/a.override.yaml
    project_directory: shared
services:
  web:
    image: nginx
`,
		"infra/db.yaml": `
services:
  db:
    image: postgres
    volumes:
      - ./init.sql:/docker-entrypoint-initdb.d/init.sql
`,
		"infra/init.sql":               "",
		"services/worker/compose.yaml": "services:\n  worker:\n    build: .\n",
		"services/worker/Dockerfile":   "FROM x\nCOPY main.go ./\n",
		"services/worker/.env.include": "A=1\n",
		"shared/a.yaml":                "services:\n  a:\n    build: ./a\n",
		"shared/a.override.yaml":       "services:\n  a:\n    env_file: a.env\n",
		"shared/a/Dockerfile":          "FROM x\nCOPY . .\n",
		"shared/a.env":                 "",
	}
	out := compile(t, files, Input{BaseDir: "", ComposePath: "compose.yaml"})
	check(t, out,
		[]string{"infra/db.yaml", "infra/init.sql", "services/worker/compose.yaml", "services/worker/main.go", "services/worker/.env.include",
			"services/worker/.env", "shared/a.yaml", "shared/a.override.yaml", "shared/a/anything", "shared/a.env", "infra/.env"},
		[]string{"services/worker/other.go", "infra/other.sql", "shared/b/x"})
}

func TestIncludeCycleWidens(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "include:\n  - a.yaml\nservices:\n  x:\n    image: x\n",
		"a.yaml":       "include:\n  - b.yaml\n",
		"b.yaml":       "include:\n  - a.yaml\n",
	}
	mustFail(t, compile(t, files, Input{ComposePath: "compose.yaml"}), "include cycle")
}

func TestIncludeWidens(t *testing.T) {
	cases := map[string]struct{ include, reason string }{
		"missing":   {"include:\n  - missing.yaml\n", "does not exist"},
		"absolute":  {"include:\n  - /etc/compose.yaml\n", "absolute path"},
		"home":      {"include:\n  - ~/compose.yaml\n", "absolute path"},
		"remote":    {"include:\n  - oci://registry/compose:1\n", "remote resource"},
		"git":       {"include:\n  - git@github.com:a/b.git\n", "remote resource"},
		"escape":    {"include:\n  - ../../outside.yaml\n", "outside the repository"},
		"variable":  {"include:\n  - ${DIR}/a.yaml\n", "include uses a variable"},
		"bad entry": {"include:\n  - [a]\n", "neither a path nor a mapping"},
		"env abs":   {"include:\n  - path: a.yaml\n    env_file: /etc/shadow\n", "absolute path"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"app/compose.yaml": c.include + "services:\n  x:\n    image: x\n",
				"app/a.yaml":       "services:\n  a:\n    image: a\n",
			}
			mustFail(t, compile(t, files, Input{BaseDir: "app", ComposePath: "app/compose.yaml"}), c.reason)
		})
	}
}

func TestExtends(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  base:
    build: ./base
    env_file: base.env
  web:
    extends: base
  api:
    extends:
      service: common
      file: common/services.yaml
`,
		"base/Dockerfile":       "FROM x\nCOPY app ./app\n",
		"common/services.yaml":  "services:\n  common:\n    build: ./api\n    volumes:\n      - ./data:/data\n",
		"common/api/Dockerfile": "FROM x\nCOPY cmd ./cmd\n",
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out,
		[]string{"base/app/x", "base.env", "common/services.yaml", "common/api/cmd/main.go", "common/data/x", "common/api/Dockerfile"},
		[]string{"base/other", "common/api/internal/x", "api/cmd/x"})
}

func TestExtendsMissingFileWidens(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  a:\n    extends:\n      service: b\n      file: other.yaml\n",
	}
	mustFail(t, compile(t, files, Input{ComposePath: "compose.yaml"}), "does not exist")
}

func TestAdditionalContexts(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  web:
    build:
      context: apps/web
      additional_contexts:
        shared: ./libs/shared
        img: docker-image://alpine:3
        assets: ./assets
`,
		"apps/web/Dockerfile":    "FROM x\nCOPY --from=shared /src /shared\nCOPY src ./src\n",
		"apps/web/.dockerignore": "src/generated\n",
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out,
		[]string{"apps/web/src/a", "libs/shared/x", "assets/logo.png"},
		[]string{"apps/web/src/generated/x", "apps/web/README.md", "libs/other/x"})
}

func TestNamedContextInsideContextIsNotIgnored(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  web:
    build:
      context: .
      additional_contexts:
        vendor: ./vendor
`,
		"Dockerfile":    "FROM x\nCOPY --from=vendor / /vendor\nCOPY src ./src\n",
		".dockerignore": "vendor\n",
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out, []string{"vendor/a", "src/a"}, []string{"other/a"})
}

func TestBindMounts(t *testing.T) {
	files := map[string]string{
		"stack/compose.yaml": `
services:
  app:
    image: nginx
    volumes:
      - ./html:/usr/share/nginx/html:ro
      - ../shared/conf:/etc/conf
      - /var/run/docker.sock:/var/run/docker.sock
      - data:/data
      - type: bind
        source: ./certs
        target: /certs
      - type: volume
        source: cache
        target: /cache
      - type: bind
        source: /etc/hosts
        target: /etc/hosts
volumes:
  data:
  cache:
  local:
    driver: local
    driver_opts:
      type: none
      o: bind
      device: ./bound
`,
	}
	out := compile(t, files, Input{BaseDir: "stack", ComposePath: "stack/compose.yaml"})
	check(t, out,
		[]string{"stack/html/index.html", "shared/conf/a", "stack/certs/a.pem", "stack/bound/x", "stack/compose.yaml"},
		[]string{"stack/data/x", "stack/cache/x", "data/x", "var/run/docker.sock", "etc/hosts", "stack/other"})
	if !reflect.DeepEqual(out.OutsideBase, []string{"shared/conf"}) {
		t.Errorf("outside base %v", out.OutsideBase)
	}
	if len(out.Services) != 0 {
		t.Errorf("services %+v", out.Services)
	}
}

func TestEnvFiles(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  a:
    image: x
    env_file: a.env
  b:
    image: x
    env_file:
      - b1.env
      - ./env/b2.env
  c:
    image: x
    env_file:
      - path: c.env
        required: false
      - path: ./c.override.env
`,
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out,
		[]string{"a.env", "b1.env", "env/b2.env", "c.env", "c.override.env", ".env"},
		[]string{"d.env", "env/other.env"})
}

func TestConfigsAndSecrets(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  a:
    image: x
    configs: [nginx]
    secrets: [key]
configs:
  nginx:
    file: ./nginx.conf
  env:
    environment: FOO
secrets:
  key:
    file: secrets/key.pem
  ext:
    external: true
`,
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out, []string{"nginx.conf", "secrets/key.pem"}, []string{"secrets/other.pem", "FOO"})
}

func TestProfiles(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  a:
    image: x
  debug:
    profiles: [debug]
    build: ./debug
`,
		"debug/Dockerfile": "FROM x\nCOPY tools ./tools\n",
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out, []string{"debug/tools/x"}, []string{"debug/other"})
	if len(out.Services) != 1 || out.Services[0].Name != "debug" {
		t.Errorf("services %+v", out.Services)
	}
}

func TestDockerfileThatDoesNotApplyIncludesTheContext(t *testing.T) {
	files := map[string]string{
		"compose.yaml":     "services:\n  a:\n    build: ./a\n  b:\n    build: ./b\n",
		"a/Dockerfile":     "FROM x\nCOPY . .\n",
		"a/.dockerignore":  "*.md\n",
		"b/Dockerfile.dev": "",
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out, []string{"a/anything", "b/anything"}, []string{"a/README.md", "c/x"})
	var reasons []string
	for _, w := range out.Widened {
		reasons = append(reasons, w.Reason)
	}
	joined := strings.Join(reasons, "\n")
	if !strings.Contains(joined, "service a: a/Dockerfile:2: COPY reads the whole build context") || !strings.Contains(joined, "service b: the Dockerfile b/Dockerfile does not exist") {
		t.Errorf("widened %q", reasons)
	}
}

func TestDockerfileInline(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  a:\n    build:\n      context: ./a\n      dockerfile_inline: |\n        FROM x\n        COPY src ./src\n        RUN make\n",
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	check(t, out, []string{"a/src/x"}, []string{"a/other"})
	if len(out.Services) != 1 || out.Services[0].DockerfilePath != "" || !reflect.DeepEqual(out.Services[0].RunCommands, []string{"make"}) {
		t.Errorf("services %+v", out.Services)
	}
}

func TestOutsideBase(t *testing.T) {
	files := map[string]string{
		"apps/web/compose.yaml": "services:\n  web:\n    build:\n      context: ..\n      dockerfile: web/Dockerfile\n",
		"apps/web/Dockerfile":   "FROM x\nCOPY web ./web\nCOPY shared ./shared\n",
	}
	out := compile(t, files, Input{BaseDir: "apps/web", ComposePath: "apps/web/compose.yaml"})
	check(t, out, []string{"apps/web/a", "apps/shared/x"}, []string{"apps/other/x"})
	if !reflect.DeepEqual(out.OutsideBase, []string{"apps"}) {
		t.Errorf("outside base %v", out.OutsideBase)
	}
	if len(out.Services) != 1 || out.Services[0].ContextDir != "apps" || out.Services[0].DockerfilePath != "apps/web/Dockerfile" {
		t.Errorf("services %+v", out.Services)
	}
}

func TestVariablesWiden(t *testing.T) {
	cases := map[string]string{
		"build string":       "services:\n  a:\n    build: ${CTX}\n",
		"build context":      "services:\n  a:\n    build:\n      context: ./${APP}\n",
		"dockerfile":         "services:\n  a:\n    build:\n      context: .\n      dockerfile: $DF\n",
		"additional context": "services:\n  a:\n    build:\n      context: .\n      additional_contexts:\n        x: ${X}\n",
		"volume":             "services:\n  a:\n    image: x\n    volumes:\n      - ${DATA:-./data}:/data\n",
		"volume long":        "services:\n  a:\n    image: x\n    volumes:\n      - type: bind\n        source: ${SRC}\n        target: /x\n",
		"env file":           "services:\n  a:\n    image: x\n    env_file: ${ENV}.env\n",
		"extends":            "services:\n  a:\n    extends:\n      service: b\n      file: ${F}\n",
		"config":             "services:\n  a:\n    image: x\nconfigs:\n  c:\n    file: ${C}\n",
		"secret":             "services:\n  a:\n    image: x\nsecrets:\n  s:\n    file: ./${S}\n",
		"merged":             "x-build: &b\n  context: ${CTX}\nservices:\n  a:\n    build:\n      <<: *b\n",
		"alias":              "x-vol: &v ${V}:/v\nservices:\n  a:\n    image: x\n    volumes: [*v]\n",
		"driver opts":        "services:\n  a:\n    image: x\nvolumes:\n  v:\n    driver_opts:\n      device: ${D}\n      o: bind\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			mustFail(t, compile(t, map[string]string{"compose.yaml": content}, Input{ComposePath: "compose.yaml"}), "uses a variable")
		})
	}
}

func TestVariablesElsewhereAreFine(t *testing.T) {
	files := map[string]string{
		"compose.yaml": `
services:
  a:
    image: nginx:${TAG:-latest}
    environment:
      URL: ${SERVICE_FQDN_A}
    ports:
      - "${PORT:-80}:80"
    build:
      context: ./a
      args:
        VERSION: ${VERSION}
`,
		"a/Dockerfile": "FROM x\nCOPY src ./src\n",
	}
	check(t, compile(t, files, Input{ComposePath: "compose.yaml"}), []string{"a/src/x"}, []string{"a/other"})
}

func TestRemoteContextWidens(t *testing.T) {
	for _, ctx := range []string{"https://github.com/acme/app.git#main", "git@github.com:acme/app.git", "github.com/acme/app"} {
		files := map[string]string{"compose.yaml": "services:\n  a:\n    build: " + ctx + "\n"}
		mustFail(t, compile(t, files, Input{ComposePath: "compose.yaml"}), "is remote")
	}
}

func TestRemoteAdditionalContextIsIgnored(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "services:\n  a:\n    build:\n      context: ./a\n      additional_contexts:\n        r: https://github.com/acme/x.git\n",
		"a/Dockerfile": "FROM x\nCOPY --from=r / /r\nCOPY src ./src\n",
	}
	check(t, compile(t, files, Input{ComposePath: "compose.yaml"}), []string{"a/src/x"}, []string{"a/other"})
}

func TestAliasBombWidens(t *testing.T) {
	bomb := `x-a: &a ["lol","lol","lol","lol","lol","lol","lol","lol","lol"]
x-b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a]
x-c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b]
x-d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c]
x-e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d]
x-f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e]
x-g: &g [*f,*f,*f,*f,*f,*f,*f,*f,*f]
x-h: &h [*g,*g,*g,*g,*g,*g,*g,*g,*g]
x-i: &i [*h,*h,*h,*h,*h,*h,*h,*h,*h]
services:
  a:
    image: x
`
	mustFail(t, compile(t, map[string]string{"compose.yaml": bomb}, Input{ComposePath: "compose.yaml"}), "too large once its aliases are expanded")
}

func TestOtherFailures(t *testing.T) {
	mustFail(t, compile(t, map[string]string{}, Input{ComposePath: "compose.yaml"}), "does not exist")
	mustFail(t, compile(t, map[string]string{"compose.yaml": "services: [\n"}, Input{ComposePath: "compose.yaml"}), "does not parse")
	mustFail(t, compile(t, map[string]string{"compose.yaml": "services:\n  a:\n    build: .\n    ports: [{target: x}]\n"}, Input{ComposePath: "compose.yaml"}), "compose-go cannot load")
	mustFail(t, compile(t, map[string]string{"compose.yaml": strings.Repeat("#", MaxFileBytes+1)}, Input{ComposePath: "compose.yaml"}), "larger than")
	mustFail(t, compile(t, map[string]string{"compose.yaml": "services:\n  a:\n    build:\n      context: .\n      additional_contexts:\n        x: oci-layout://./layout\n"}, Input{ComposePath: "compose.yaml"}), "OCI layout")
	budget := Compile(context.Background(), tree.NewMem(map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n"}).WithBudget(&tree.Budget{MaxBytes: 1}), Input{ComposePath: "compose.yaml"})
	mustFail(t, budget, "cap")
	deep := map[string]string{}
	for i := 0; i <= MaxDepth+1; i++ {
		deep[name(i)] = "include:\n  - " + name(i+1) + "\n"
	}
	deep[name(MaxDepth+2)] = "services:\n  a:\n    image: x\n"
	mustFail(t, compile(t, deep, Input{ComposePath: name(0)}), "nested deeper")
}

func name(i int) string { return "c" + strings.Repeat("x", i) + ".yaml" }

func TestDockerfileBudgetWidensPerService(t *testing.T) {
	files := map[string]string{
		"compose.yaml":    "services:\n  a:\n    build: ./a\n",
		"a/Dockerfile":    "FROM x\nCOPY src ./src\n" + strings.Repeat("# pad\n", 100),
		"a/.dockerignore": "x\n",
	}
	tr := tree.NewMem(files).WithBudget(&tree.Budget{MaxFiles: 2})
	out := Compile(context.Background(), tr, Input{ComposePath: "compose.yaml"})
	check(t, out, []string{"a/anything", "a/x"}, []string{"b/x"})
}

func TestReads(t *testing.T) {
	files := map[string]string{
		"compose.yaml": "include:\n  - inc.yaml\nservices:\n  a:\n    build: ./a\n",
		"inc.yaml":     "services:\n  b:\n    image: x\n",
		"a/Dockerfile": "FROM x\nCOPY . .\n",
	}
	out := compile(t, files, Input{ComposePath: "compose.yaml"})
	mustApply(t, out)
	want := []string{".env", "a/.dockerignore", "a/Dockerfile", "a/Dockerfile.dockerignore", "compose.yaml", "inc.yaml"}
	if !reflect.DeepEqual(out.Reads, want) {
		t.Errorf("reads %v, want %v", out.Reads, want)
	}
}

func TestTemporaryDirectoryIsRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	out := compile(t, map[string]string{"compose.yaml": "services:\n  a:\n    image: x\n"}, Input{ComposePath: "compose.yaml"})
	mustApply(t, out)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("left %v in %s", entries, filepath.Clean(dir))
	}
}
