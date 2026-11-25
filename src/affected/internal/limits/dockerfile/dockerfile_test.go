package dockerfile

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

func includes(rs *match.Ruleset, path string) bool {
	in := rs.Default == match.Include
	for _, r := range rs.Rules {
		if ruleMatches(r, path) {
			in = r.Action == match.Include
		}
	}
	return in
}

func ruleMatches(r match.Rule, path string) bool {
	switch r.Kind {
	case match.Dir:
		return path == r.Path || strings.HasPrefix(path, r.Path+"/")
	case match.File:
		return path == r.Path
	case match.Glob:
		return match.GlobMatch(r.Pattern, path)
	}
	return false
}

func analyzeIn(t *testing.T, df, ctx string, named map[string]string, in, out []string) Analysis {
	t.Helper()
	a := Analyze([]byte(df), ctx, ctx+"/Dockerfile", named)
	if !a.Applies {
		t.Fatalf("limit does not apply: %+v", a.Widened)
	}
	rs := &match.Ruleset{Default: match.Exclude, Rules: a.Includes}
	for _, p := range in {
		if !includes(rs, p) {
			t.Errorf("%q: excluded, want included (rules %+v)", p, a.Includes)
		}
	}
	for _, p := range out {
		if includes(rs, p) {
			t.Errorf("%q: included, want excluded (rules %+v)", p, a.Includes)
		}
	}
	return a
}

func TestSelectiveCopy(t *testing.T) {
	df := `FROM node:22 AS deps
WORKDIR /app
COPY package.json pnpm-lock.yaml ./
COPY packages/ui/package.json ./packages/ui/
RUN pnpm install

FROM deps AS build
COPY src ./src
COPY ["public", "/app/public"]
ADD --chmod=644 config/app.toml /etc/app.toml
COPY "quoted" 'single' /q/
RUN --mount=type=bind,source=scripts,target=/scripts sh /scripts/build.sh
RUN --mount=type=cache,target=/root/.cache true
COPY --from=deps /app/node_modules ./node_modules
COPY --from=0 /x /x
COPY --from=nginx:latest /etc/nginx /etc/nginx

FROM nginx
COPY --from=build /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/nginx.conf
`
	a := analyzeIn(t, df, "apps/web", nil,
		[]string{"apps/web/package.json", "apps/web/pnpm-lock.yaml", "apps/web/packages/ui/package.json", "apps/web/src/a.ts",
			"apps/web/public/x.png", "apps/web/config/app.toml", "apps/web/quoted/x", "apps/web/single", "apps/web/scripts/build.sh", "apps/web/nginx.conf"},
		[]string{"apps/web/README.md", "apps/web/packages/ui/src/x.ts", "apps/web/node_modules/x", "apps/web/x", "apps/web/dist/a", "apps/web/srcx", "apps/api/src/a"})
	if len(a.RunCommands) != 3 || a.RunCommands[0] != "pnpm install" || !strings.Contains(a.RunCommands[1], "sh /scripts/build.sh") {
		t.Errorf("run commands %q", a.RunCommands)
	}
	for _, r := range a.Includes {
		if r.Origin == nil || r.Origin.Rule != "dockerfile" {
			t.Errorf("rule %+v origin %+v", r, r.Origin)
		}
	}
}

func TestWildcardSources(t *testing.T) {
	df := "FROM x\nCOPY package*.json ./\nCOPY *.json ./\nCOPY packages/*/package.json ./\nCOPY src/**/x.ts ./\nCOPY a?c ./\n"
	analyzeIn(t, df, "", nil,
		[]string{"package.json", "package-lock.json", "tsconfig.json", "packages/ui/package.json", "src/a/b/x.ts", "src/x.ts", "abc", "abc/d"},
		[]string{"packages/ui/index.ts", "README.md", "src/a/y.ts", "x/package.json", "a/b.json"})
}

func TestSourcesInRootContext(t *testing.T) {
	analyzeIn(t, "FROM x\nCOPY /abs/x ./\nCOPY ./rel/ ./\nCOPY a/../b ./\n", "", nil,
		[]string{"abs/x", "rel/y", "b/c"}, []string{"a/x", "abs"})
}

func TestNamedContexts(t *testing.T) {
	named := map[string]string{"shared": "libs/shared", "base": "images/base", "assets": "", "tools": "tools"}
	df := `FROM base AS b
FROM node AS shared
FROM node
COPY --from=shared /x /x
RUN --mount=type=bind,from=tools,target=/t true
COPY --from=docker.io/library/assets:latest / /a
COPY app ./
`
	a := analyzeIn(t, df, "apps/web", named,
		[]string{"images/base/Dockerfile", "libs/shared/a", "tools/x", "anything/at/all", "apps/web/app/x"}, nil)
	want := []string{"images/base", "libs/shared", "tools", ""}
	if !reflect.DeepEqual(a.NamedContexts, want) {
		t.Errorf("named contexts %q, want %q", a.NamedContexts, want)
	}
}

func TestNamedContextNotUsed(t *testing.T) {
	analyzeIn(t, "FROM node\nCOPY --from=builder /x /x\nCOPY a ./\n", "", map[string]string{"shared": "libs/shared"},
		[]string{"a/x"}, []string{"libs/shared/a"})
}

func TestRunHeredocIsFine(t *testing.T) {
	a := analyzeIn(t, "FROM x\nRUN <<EOF\npnpm --filter web build\nEOF\nCOPY a ./\n", "", nil, []string{"a"}, []string{"b"})
	if len(a.RunCommands) != 1 || !strings.Contains(a.RunCommands[0], "pnpm --filter web build") {
		t.Errorf("run commands %q", a.RunCommands)
	}
}

func TestSyntaxDirectiveStandard(t *testing.T) {
	for _, s := range []string{"docker/dockerfile:1", "docker/dockerfile:1-labs", "docker/dockerfile:1.7", "docker.io/docker/dockerfile:1", "docker/dockerfile", "docker/dockerfile:1@sha256:abc"} {
		analyzeIn(t, "# syntax="+s+"\nFROM x\nCOPY a ./\n", "", nil, []string{"a"}, []string{"b"})
	}
}

func TestDoesNotApply(t *testing.T) {
	cases := map[string]string{
		"copy dot":                "FROM x\nCOPY . .\n",
		"copy dot slash":          "FROM x\nCOPY ./ /app\n",
		"copy slash":              "FROM x\nCOPY / /app\n",
		"copy empty":              "FROM x\nCOPY \"\" /app\n",
		"add dot":                 "FROM x\nADD . /app\n",
		"root wildcard":           "FROM x\nCOPY * /app/\n",
		"root double star":        "FROM x\nCOPY **/package.json /app/\n",
		"root question":           "FROM x\nCOPY ?/a /app/\n",
		"root star dir":           "FROM x\nCOPY */package.json /app/\n",
		"variable":                "FROM x\nARG SRC=app\nCOPY ${SRC} /app\n",
		"variable plain":          "FROM x\nCOPY $SRC /app\n",
		"from variable":           "FROM x\nARG S=build\nCOPY --from=$S /a /a\n",
		"url":                     "FROM x\nADD https://example.com/a.tar.gz /a\n",
		"http":                    "FROM x\nADD http://example.com/a /a\n",
		"git ssh":                 "FROM x\nADD git@github.com:acme/repo.git /src\n",
		"git suffix":              "FROM x\nADD https://github.com/acme/repo.git#main /src\n",
		"git protocol":            "FROM x\nADD git://example.com/repo /src\n",
		"scp git":                 "FROM x\nADD deploy@example.com:acme/repo /src\n",
		"github shorthand":        "FROM x\nADD github.com/acme/repo /src\n",
		"dot git":                 "FROM x\nADD vendor/lib.git /src\n",
		"keep git dir":            "FROM x\nADD --keep-git-dir=true foo /src\n",
		"copy heredoc":            "FROM x\nCOPY <<EOF /a\nhello\nEOF\n",
		"add heredoc":             "FROM x\nADD <<EOF /a\nhello\nEOF\n",
		"class":                   "FROM x\nCOPY src/[ab].ts /a/\n",
		"escape":                  "FROM x\nCOPY src/a\\*b /a/\n",
		"parse error":             "FROM x\nCOPY\n",
		"unknown instruction":     "FROM x\nFROBNICATE a\n",
		"no instructions":         "# only a comment\n",
		"unterminated heredoc":    "FROM x\nRUN <<EOF\necho\n",
		"custom frontend":         "# syntax=example.com/my/frontend:1\nFROM x\nCOPY a ./\n",
		"upstream frontend":       "# syntax=docker/dockerfile-upstream:master\nFROM x\nCOPY a ./\n",
		"onbuild":                 "FROM x\nONBUILD COPY a ./\nCOPY b ./\n",
		"escapes context":         "FROM x\nCOPY ../shared /s\n",
		"escapes context deep":    "FROM x\nCOPY a/../../b /s\n",
		"bind mount default":      "FROM x\nRUN --mount=type=bind,target=/src make\n",
		"bind mount dot":          "FROM x\nRUN --mount=type=bind,source=.,target=/src make\n",
		"bind mount variable":     "FROM x\nRUN --mount=type=bind,source=$DIR,target=/src make\n",
		"bind mount escape":       "FROM x\nRUN --mount=type=bind,source=../x,target=/src make\n",
		"bad mount":               "FROM x\nRUN --mount=type=bind,bogus=1 make\n",
		"from variable named":     "ARG BASE=x\nFROM $BASE\nCOPY a ./\n",
		"escape token":            "# escape=`\nFROM x\nCOPY a`b ./\n",
		"unterminated quote":      "FROM x\nCOPY \"a b\" ./\n",
		"too large":               "FROM x\n" + strings.Repeat("# padding\n", MaxBytes/10+1),
		"windows style abs path":  "FROM x\nCOPY c:\\\\x ./\n",
		"onbuild in later stage":  "FROM x AS a\nCOPY a ./\nFROM a\nONBUILD RUN true\n",
		"copy dot in later stage": "FROM x AS a\nCOPY a ./\nFROM a\nCOPY . .\n",
	}
	named := map[string]string(nil)
	for name, df := range cases {
		t.Run(name, func(t *testing.T) {
			n := named
			if name == "from variable named" {
				n = map[string]string{"shared": "libs/shared"}
			}
			a := Analyze([]byte(df), "app", "app/Dockerfile", n)
			if a.Applies {
				t.Fatalf("limit applies with includes %+v", a.Includes)
			}
			if len(a.Widened) == 0 || a.Widened[0].Scope != "limit:dockerfile" || !strings.HasPrefix(a.Widened[0].Reason, "app/Dockerfile") {
				t.Errorf("widened %+v", a.Widened)
			}
			if a.Includes != nil {
				t.Errorf("includes %+v", a.Includes)
			}
		})
	}
}

func TestReasonHasLine(t *testing.T) {
	a := Analyze([]byte("FROM x\nCOPY a ./\nCOPY . .\n"), "", "Dockerfile", nil)
	if a.Applies || len(a.Widened) != 1 || a.Widened[0].Reason != "Dockerfile:3: COPY reads the whole build context" {
		t.Errorf("got %+v", a)
	}
}

func TestRunCommandsWhenNotApplying(t *testing.T) {
	a := Analyze([]byte("FROM x\nCOPY . .\nRUN [\"npm\", \"run\", \"build\"]\n"), "", "Dockerfile", nil)
	if a.Applies || len(a.RunCommands) != 1 || a.RunCommands[0] != "npm run build" {
		t.Errorf("got %+v", a)
	}
}

func TestCompile(t *testing.T) {
	tr := tree.NewMem(map[string]string{
		"apps/web/Dockerfile": "FROM x\nCOPY src ./src\nRUN make\n",
		"apps/web/src/a":      "",
	})
	out := Compile(context.Background(), tr, Input{ContextDir: "apps/web", DockerfilePath: "apps/web/Dockerfile"})
	if out.Ruleset == nil {
		t.Fatalf("no ruleset: %+v", out.Widened)
	}
	if out.Ruleset.Limit != "dockerfile" || out.Ruleset.Default != match.Exclude {
		t.Errorf("ruleset %+v", out.Ruleset)
	}
	for _, p := range []string{"apps/web/src/a", "apps/web/Dockerfile", "apps/web/.dockerignore", "apps/web/Dockerfile.dockerignore"} {
		if !includes(out.Ruleset, p) {
			t.Errorf("%q excluded", p)
		}
	}
	for _, p := range []string{"apps/web/README.md", "apps/web/srcx"} {
		if includes(out.Ruleset, p) {
			t.Errorf("%q included", p)
		}
	}
	if !reflect.DeepEqual(out.Reads, []string{"apps/web/Dockerfile"}) || !reflect.DeepEqual(out.RunCommands, []string{"make"}) {
		t.Errorf("reads %v run %v", out.Reads, out.RunCommands)
	}
}

func TestCompileWidens(t *testing.T) {
	ctx := context.Background()
	missing := Compile(ctx, tree.NewMem(map[string]string{}), Input{ContextDir: "", DockerfilePath: "Dockerfile"})
	if missing.Ruleset != nil || len(missing.Widened) != 1 || !strings.Contains(missing.Widened[0].Reason, "does not exist") {
		t.Errorf("missing: %+v", missing)
	}
	budget := Compile(ctx, tree.NewMem(map[string]string{"Dockerfile": "FROM x\nCOPY a ./\n"}).WithBudget(&tree.Budget{MaxFiles: -1, MaxBytes: 1}), Input{DockerfilePath: "Dockerfile"})
	if budget.Ruleset != nil || len(budget.Widened) != 1 || !strings.Contains(budget.Widened[0].Reason, "cap") {
		t.Errorf("budget: %+v", budget)
	}
	dot := Compile(ctx, tree.NewMem(map[string]string{"Dockerfile": "FROM x\nCOPY . .\n"}), Input{DockerfilePath: "Dockerfile"})
	if dot.Ruleset != nil || len(dot.Widened) != 1 {
		t.Errorf("dot: %+v", dot)
	}
}

func TestStandardFrontend(t *testing.T) {
	for s, want := range map[string]bool{
		"docker/dockerfile:1":                     true,
		"docker/dockerfile:1.7-labs":              true,
		"docker.io/docker/dockerfile:1":           true,
		"docker/dockerfile@sha256:0123":           true,
		"docker/dockerfile-upstream:1":            false,
		"ghcr.io/docker/dockerfile:1":             false,
		"localhost:5000/docker/dockerfile":        false,
		"docker/dockerfile:1/evil":                false,
		"index.docker.io/docker/dockerfile:1.4.0": false,
	} {
		if got := standardFrontend(s); got != want {
			t.Errorf("standardFrontend(%q) = %v, want %v", s, got, want)
		}
	}
}

// FuzzDockerfile checks that Analyze never panics and that every include is a
// valid, clean path or glob inside the repository.
func FuzzDockerfile(f *testing.F) {
	f.Add("FROM x\nCOPY a b ./\n", "apps/web")
	f.Add("FROM x AS b\nCOPY --from=shared / /\nRUN --mount=type=bind,source=s,target=/s make\n", "")
	f.Add("# syntax=docker/dockerfile:1\nFROM x\nCOPY src/**/*.ts ./\nADD a?c ./\n", "ctx")
	f.Add("FROM x\nCOPY <<EOF /a\nx\nEOF\nONBUILD RUN true\n", "")
	f.Add("# escape=`\nFROM x\nCOPY \"a b\" 'c' ./\n", "")
	named := map[string]string{"shared": "libs/shared", "root": ""}
	f.Fuzz(func(t *testing.T, df, ctx string) {
		if strings.IndexByte(ctx, 0) >= 0 || strings.HasPrefix(ctx, "/") || strings.Contains(ctx, "..") {
			return
		}
		for _, s := range strings.Split(ctx, "/") {
			if ctx != "" && (s == "" || s == ".") {
				return
			}
		}
		a := Analyze([]byte(df), ctx, "Dockerfile", named)
		if !a.Applies {
			if len(a.Widened) == 0 || a.Includes != nil {
				t.Fatalf("does not apply without a reason or with includes: %+v", a)
			}
			return
		}
		for _, r := range a.Includes {
			if r.Action != match.Include {
				t.Fatalf("rule %+v is not an include", r)
			}
			switch r.Kind {
			case match.Dir:
				if !cleanPath(r.Path) {
					t.Fatalf("unclean dir %q", r.Path)
				}
			case match.Glob:
				if err := match.ValidateGlob(r.Pattern); err != nil {
					t.Fatalf("invalid glob %q: %v", r.Pattern, err)
				}
			default:
				t.Fatalf("unexpected rule %+v", r)
			}
		}
	})
}

func cleanPath(p string) bool {
	if p == "" || strings.IndexByte(p, 0) >= 0 {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}
