package workspace

import (
	"strings"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
)

// A member nested in a closure member (an unnamed package.json that only sets
// "type") is part of the closure member's files: no exclude rule for it.
func TestCompileMemberNestedInClosureMember(t *testing.T) {
	files := map[string]string{
		"package.json":                       `{"name":"root"}`,
		"pnpm-workspace.yaml":                "packages: ['apps/*', 'packages/**']\n",
		"apps/web/package.json":              `{"name":"web","dependencies":{"@acme/ui":"workspace:*"}}`,
		"packages/ui/package.json":           `{"name":"@acme/ui"}`,
		"packages/ui/src/icons/package.json": `{"type":"module"}`,
		"packages/other/package.json":        `{"name":"other"}`,
		"packages/other/deep/package.json":   `{"name":"deep"}`,
	}
	rs := compileFor(t, files, "apps/web/Dockerfile", nil, nil).Ruleset
	for p, want := range map[string]string{
		"packages/ui/src/icons/index.ts": "include:workspace",
		"packages/ui/src/index.ts":       "include:workspace",
		"packages/other/x.ts":            "exclude",
		"packages/other/deep/x.ts":       "exclude", // nested in a member that is not in the closure
	} {
		if got := decide(rs, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

// Member scripts that build other members widen the closure, through every
// resolver's command parser (pnpm in the js resolver, turbo, nx).
func TestCompileMemberScripts(t *testing.T) {
	files := map[string]string{
		"package.json":                 `{"name":"root","workspaces":["apps/*","packages/*"]}`,
		"turbo.json":                   `{"tasks":{"build":{}}}`,
		"nx.json":                      `{}`,
		"apps/web/package.json":        `{"name":"web","scripts":{"build":"pnpm -F docs build && next build"}}`,
		"apps/api/package.json":        `{"name":"api","scripts":{"build":"turbo run build --filter=schema && tsc"}}`,
		"apps/cli/package.json":        `{"name":"cli","scripts":{"build":"nx build kit"}}`,
		"packages/docs/package.json":   `{"name":"docs"}`,
		"packages/schema/package.json": `{"name":"schema"}`,
		"packages/kit/package.json":    `{"name":"kit"}`,
		"packages/other/package.json":  `{"name":"other"}`,
	}
	for anchor, path := range map[string]string{
		"apps/web": "packages/docs/content/a.md",
		"apps/api": "packages/schema/index.ts",
		"apps/cli": "packages/kit/index.ts",
	} {
		rs := compileFor(t, files, anchor+"/Dockerfile", nil, nil).Ruleset
		if rs == nil {
			t.Fatalf("%s: no ruleset", anchor)
		}
		if got := decide(rs, path); got != "include:workspace" {
			t.Errorf("%s: %s: %s, want include:workspace", anchor, path, got)
		}
		if got := decide(rs, "packages/other/x.ts"); got != "exclude" {
			t.Errorf("%s: packages/other/x.ts: %s, want exclude", anchor, got)
		}
	}
}

// The workspace dependencies of a member with lifecycle scripts are global:
// its postinstall may use them.
func TestCompileLifecycleClosure(t *testing.T) {
	files := map[string]string{
		"package.json":                 `{"name":"root","workspaces":["apps/*","packages/*"]}`,
		"apps/web/package.json":        `{"name":"web"}`,
		"packages/db/package.json":     `{"name":"db","scripts":{"postinstall":"prisma generate"},"dependencies":{"schema":"*"}}`,
		"packages/schema/package.json": `{"name":"schema","dependencies":{"base":"*"}}`,
		"packages/base/package.json":   `{"name":"base"}`,
		"packages/other/package.json":  `{"name":"other"}`,
	}
	rs := compileFor(t, files, "apps/web/Dockerfile", nil, nil).Ruleset
	for p, want := range map[string]string{
		"packages/db/schema.prisma":   "include:global",
		"packages/schema/index.ts":    "include:global",
		"packages/base/index.ts":      "include:global",
		"packages/other/index.ts":     "exclude",
		"packages/other/package.json": "include:global",
	} {
		if got := decide(rs, p); got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
}

// Includes (symlink expansion) matches globs in linear time.
func TestIncludesPathologicalGlob(t *testing.T) {
	rs := &match.Ruleset{Default: match.Exclude, Rules: []match.Rule{
		match.IncludeGlob("p/*a*a*a*a*a*a*a*a*a*a*a*a*a*a*b", nil),
		match.IncludeGlob("**/a/**/a/**/a/**/a/**/a/**/b", nil),
	}}
	long := "p/" + strings.Repeat("a", 40)
	deep := strings.TrimSuffix(strings.Repeat("a/", 40), "/")
	done := make(chan bool, 1)
	start := time.Now()
	go func() { done <- Includes(rs, long) || Includes(rs, deep) }()
	select {
	case got := <-done:
		if got {
			t.Errorf("matched")
		}
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Errorf("took %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Includes hangs on a pathological glob")
	}
}
