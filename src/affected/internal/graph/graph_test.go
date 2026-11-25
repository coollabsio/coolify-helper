package graph

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
)

func sample() *Graph {
	g := New()
	g.AddPackage(Package{Dir: "apps/web", Names: []string{"@acme/web"}, Resolvers: []string{"pnpm"}, Deps: []string{"@acme/ui", "react"}})
	g.AddPackage(Package{Dir: "apps/api", Names: []string{"api"}, Resolvers: []string{"pnpm"}, Deps: []string{"@acme/utils"}})
	g.AddPackage(Package{Dir: "packages/ui", Names: []string{"@acme/ui"}, Resolvers: []string{"pnpm"}, Deps: []string{"@acme/utils"}})
	g.AddPackage(Package{Dir: "packages/utils", Names: []string{"@acme/utils"}, Resolvers: []string{"pnpm"}})
	g.AddPackage(Package{Dir: "packages/ui/nested", Resolvers: []string{"pnpm"}, DepDirs: []string{"apps/web"}})
	return g
}

func TestAddPackageMerges(t *testing.T) {
	g := sample()
	g.AddPackage(Package{Dir: "apps/web", Names: []string{"web"}, Resolvers: []string{"nx"}, Manifests: []string{"apps/web/project.json"}, DepDirs: []string{"packages/utils"}, Lifecycle: true})
	p := g.PackageAt("apps/web")
	if !reflect.DeepEqual(p.Names, []string{"@acme/web", "web"}) || !reflect.DeepEqual(p.Resolvers, []string{"nx", "pnpm"}) || !p.Lifecycle {
		t.Errorf("merged = %+v", p)
	}
	if ps := g.ByName("web"); len(ps) != 1 || ps[0].Dir != "apps/web" {
		t.Errorf("ByName(web) = %v", ps)
	}
	if len(g.Packages) != 5 {
		t.Errorf("packages = %d", len(g.Packages))
	}
}

func TestClosure(t *testing.T) {
	g := sample()
	got := g.Closure([]string{"apps/web"})
	want := map[string][]string{
		"apps/web":       {"apps/web (@acme/web)"},
		"packages/ui":    {"apps/web (@acme/web)", "@acme/ui"},
		"packages/utils": {"apps/web (@acme/web)", "@acme/ui", "@acme/utils"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("closure = %v", got)
	}
	// Several anchors, an unknown one, a duplicate, and a cycle.
	got = g.Closure([]string{"packages/ui/nested", "apps/api", "nope", "apps/api"})
	if keys := SortedKeys(got); !reflect.DeepEqual(keys, []string{"apps/api", "apps/web", "packages/ui", "packages/ui/nested", "packages/utils"}) {
		t.Errorf("keys = %v", keys)
	}
	if got["packages/ui/nested"][0] != "packages/ui/nested" {
		t.Errorf("label of an unnamed anchor = %v", got["packages/ui/nested"])
	}
	if got["packages/utils"][0] != "apps/api (api)" || len(got["packages/utils"]) != 2 {
		t.Errorf("shortest chain = %v", got["packages/utils"])
	}
	if c := g.Closure(nil); len(c) != 0 {
		t.Errorf("empty closure = %v", c)
	}
}

func TestDependents(t *testing.T) {
	g := sample()
	if got := g.Dependents("packages/utils"); !reflect.DeepEqual(got, []string{"apps/api", "apps/web", "packages/ui", "packages/ui/nested"}) {
		t.Errorf("dependents = %v", got)
	}
	if got := g.Dependents("packages/ui/nested"); got != nil {
		t.Errorf("dependents of a leaf = %v", got)
	}
}

func TestInnermost(t *testing.T) {
	g := sample()
	for p, want := range map[string]string{
		"packages/ui/src/x.ts":      "packages/ui",
		"packages/ui/nested/a/b.ts": "packages/ui/nested",
		"packages/ui":               "packages/ui",
		"packages/uix/a":            "",
		"README.md":                 "",
		"":                          "",
	} {
		m := g.Innermost(p)
		got := ""
		if m != nil {
			got = m.Dir
		}
		if got != want {
			t.Errorf("Innermost(%q) = %q, want %q", p, got, want)
		}
	}
	g.AddPackage(Package{Dir: ""})
	if m := g.Innermost("README.md"); m == nil || m.Dir != "" {
		t.Errorf("root member not found")
	}
}

func TestGlobalsLockfilesMerge(t *testing.T) {
	g := New()
	g.AddGlobal(match.File, "package.json", "pnpm")
	g.AddGlobal(match.File, "package.json", "turbo")
	g.AddGlobal(match.Glob, "tsconfig*.json", "pnpm")
	g.AddLockfile("pnpm-lock.yaml", "pnpm")
	g.AddLockfile("pnpm-lock.yaml", "pnpm")
	if len(g.Globals) != 3 || len(g.Lockfiles) != 1 {
		t.Errorf("globals = %v, lockfiles = %v", g.Globals, g.Lockfiles)
	}
	o := sample()
	o.Resolvers = []string{"pnpm"}
	o.NotModeled = []string{"bazel"}
	o.AddWiden("resolver:x", "why")
	o.AddLockfile("yarn.lock", "yarn")
	g.Merge(o)
	g.Merge(nil)
	g.Normalize()
	if len(g.Packages) != 5 || g.Packages[0].Dir != "apps/api" || !reflect.DeepEqual(g.Lockfiles, []string{"pnpm-lock.yaml", "yarn.lock"}) ||
		!reflect.DeepEqual(g.NotModeled, []string{"bazel"}) || len(g.Widened) != 1 || len(g.Globals) != 4 {
		t.Errorf("merged = %+v", g)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	g := sample()
	g.Normalize()
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	var h Graph
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	// The index is rebuilt lazily.
	if p := h.PackageAt("packages/ui"); p == nil || len(h.ByName("@acme/utils")) != 1 {
		t.Errorf("index after unmarshal")
	}
	if !reflect.DeepEqual(h.Closure([]string{"apps/web"}), g.Closure([]string{"apps/web"})) {
		t.Errorf("closure differs after a round trip")
	}
}

func TestLabelAndTrim(t *testing.T) {
	if l := (&Package{Dir: ""}).Label(); l != "/" {
		t.Errorf("root label = %q", l)
	}
	if TrimSlash("./a/b/") != "a/b" {
		t.Errorf("TrimSlash")
	}
}
