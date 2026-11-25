package match

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustCompile(t testing.TB, s Set) *Matcher {
	t.Helper()
	m, err := Compile(s)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return m
}

func set(rulesets ...Ruleset) Set {
	if rulesets == nil {
		rulesets = []Ruleset{}
	}
	return Set{Version: SetVersion, AllOf: rulesets, Lockfiles: []string{}}
}

func ruleset(limit string, def Action, rules ...Rule) Ruleset {
	if rules == nil {
		rules = []Rule{}
	}
	return Ruleset{Limit: limit, Default: def, Rules: rules}
}

func TestMatcher(t *testing.T) {
	tests := []struct {
		name  string
		set   Set
		paths map[string]bool
	}{
		{
			name: "empty all_of matches everything",
			set:  set(),
			paths: map[string]bool{
				"a": true, "a/b/c": true, "README.md": true,
			},
		},
		{
			name: "dir rule matches the directory and below, not siblings",
			set:  set(ruleset("build_context", Exclude, IncludeDir("apps/web", nil))),
			paths: map[string]bool{
				"apps/web":            true, // a submodule gitlink
				"apps/web/index.ts":   true,
				"apps/web/a/b/c.ts":   true,
				"apps/web2/index.ts":  false,
				"apps/webx":           false,
				"apps":                false,
				"apps/api/index.ts":   false,
				"Apps/web/index.ts":   false,
				"x/apps/web/index.ts": false,
			},
		},
		{
			name: "file rule is exact",
			set:  set(ruleset("dockerfile", Exclude, IncludeFile("package.json", nil))),
			paths: map[string]bool{
				"package.json":   true,
				"package.json/x": false,
				"a/package.json": false,
				"package.json5":  false,
				"Package.json":   false,
				"package.jso":    false,
			},
		},
		{
			name: "last matching rule wins",
			set: set(ruleset("workspace", Exclude,
				IncludeDir("apps", nil),
				ExcludeDir("apps/api", nil),
				IncludeFile("apps/api/package.json", nil),
				ExcludeGlob("**/*.md", nil),
			)),
			paths: map[string]bool{
				"apps/web/x.ts":         true,
				"apps/api/x.ts":         false,
				"apps/api/package.json": true,
				"apps/web/README.md":    false,
				"apps/api/README.md":    false,
				"README.md":             false,
				"lib/x.ts":              false,
			},
		},
		{
			name: "an earlier rule for the same path never decides",
			set: set(ruleset("workspace", Include,
				ExcludeDir("a", nil),
				IncludeDir("a", nil),
				ExcludeFile("f", nil),
				IncludeFile("f", nil),
				IncludeFile("g", nil),
				ExcludeFile("g", nil),
			)),
			paths: map[string]bool{"a/x": true, "f": true, "g": false, "h": true},
		},
		{
			name: "glob before a dir rule does not decide",
			set: set(ruleset("workspace", Include,
				IncludeGlob("**/*.ts", nil),
				ExcludeDir("packages/b", nil),
			)),
			paths: map[string]bool{"packages/b/x.ts": false, "packages/a/x.ts": true},
		},
		{
			name: "all_of: every ruleset must include",
			set: set(
				ruleset("workspace", Include, ExcludeDir("docs", nil)),
				ruleset("dockerfile", Exclude, IncludeDir("apps/web", nil), IncludeDir("docs", nil), IncludeFile("package.json", nil)),
			),
			paths: map[string]bool{
				"apps/web/x":   true,
				"package.json": true,
				"docs/x.md":    false,
				"lib/x":        false,
			},
		},
		{
			name:  "default exclude with no rules matches nothing",
			set:   set(ruleset("workspace", Include), ruleset("dockerfile", Exclude)),
			paths: map[string]bool{"a": false, "a/b": false},
		},
		{
			name:  "rules that all agree with the default",
			set:   set(ruleset("x", Exclude, ExcludeDir("a", nil), ExcludeGlob("*", nil))),
			paths: map[string]bool{"a": false, "b": false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := mustCompile(t, tt.set)
			for p, want := range tt.paths {
				if got := m.Match(p); got != want {
					t.Errorf("Match(%q) = %v, want %v", p, got, want)
				}
				if got := m.MatchPath(NewPath(p)); got != want {
					t.Errorf("MatchPath(%q) = %v, want %v", p, got, want)
				}
				if got, _ := m.Explain(p); got != want {
					t.Errorf("Explain(%q) = %v, want %v", p, got, want)
				}
				if got := refSetMatch(tt.set, p); got != want {
					t.Errorf("refSetMatch(%q) = %v, want %v", p, got, want)
				}
			}
		})
	}
}

func TestExplain(t *testing.T) {
	ws := &Origin{Rule: "workspace", Via: []string{"apps/web", "packages/ui"}}
	global := &Origin{Rule: "global"}
	def := &Origin{Rule: "build_context"}
	s := set(
		Ruleset{Limit: "build_context", Default: Include, DefaultOrigin: def},
		ruleset("workspace", Exclude,
			IncludeDir("packages/ui", ws),
			IncludeFile("package.json", global),
			ExcludeGlob("**/*.md", nil),
		),
	)
	m := mustCompile(t, s)

	ok, ds := m.Explain("packages/ui/button.tsx")
	if !ok || len(ds) != 2 {
		t.Fatalf("Explain = %v, %d decisions", ok, len(ds))
	}
	if ds[0].Limit != "build_context" || !ds[0].Included || ds[0].Rule != nil || ds[0].Origin != def {
		t.Errorf("decision 0 = %+v", ds[0])
	}
	if ds[1].Limit != "workspace" || !ds[1].Included || ds[1].Rule == nil || ds[1].Rule.Path != "packages/ui" || ds[1].Origin != ws {
		t.Errorf("decision 1 = %+v", ds[1])
	}

	ok, ds = m.Explain("packages/ui/README.md")
	if ok || ds[1].Included || ds[1].Rule == nil || ds[1].Rule.Pattern != "**/*.md" || ds[1].Origin != nil {
		t.Errorf("Explain(README) = %v, %+v", ok, ds[1])
	}

	ok, ds = m.Explain("lib/x")
	if ok || ds[1].Included || ds[1].Rule != nil || ds[1].Origin != nil {
		t.Errorf("Explain(lib/x) = %v, %+v", ok, ds[1])
	}

	// The matcher keeps its own copy of the set.
	s.AllOf[1].Rules[0].Path = "changed"
	if !m.Match("packages/ui/x") {
		t.Error("the matcher changed with the caller's set")
	}
}

func TestCompileErrors(t *testing.T) {
	tests := map[string]Set{
		"version 0":         {AllOf: []Ruleset{}},
		"version 2":         {Version: 2},
		"bad default":       set(ruleset("x", "maybe")),
		"bad action":        set(ruleset("x", Include, Rule{Action: "skip", Kind: Dir, Path: "a"})),
		"bad kind":          set(ruleset("x", Include, Rule{Action: Include, Kind: "regex", Pattern: "a"})),
		"empty dir":         set(ruleset("x", Include, IncludeDir("", nil))),
		"dir slash":         set(ruleset("x", Include, IncludeDir("a/", nil))),
		"dir leading slash": set(ruleset("x", Include, IncludeDir("/a", nil))),
		"dir dot":           set(ruleset("x", Include, IncludeDir("./a", nil))),
		"dir dotdot":        set(ruleset("x", Include, IncludeDir("a/../b", nil))),
		"file double slash": set(ruleset("x", Include, IncludeFile("a//b", nil))),
		"file NUL":          set(ruleset("x", Include, IncludeFile("a\x00", nil))),
		"file with pattern": set(ruleset("x", Include, Rule{Action: Include, Kind: File, Path: "a", Pattern: "*"})),
		"glob with path":    set(ruleset("x", Include, Rule{Action: Include, Kind: Glob, Path: "a", Pattern: "*"})),
		"empty glob":        set(ruleset("x", Include, IncludeGlob("", nil))),
		"glob slash":        set(ruleset("x", Include, IncludeGlob("a/", nil))),
		"bad lockfile":      {Version: SetVersion, Lockfiles: []string{"/pnpm-lock.yaml"}},
	}
	for name, s := range tests {
		if _, err := Compile(s); err == nil {
			t.Errorf("%s: Compile succeeded", name)
		}
	}
}

func TestSetJSON(t *testing.T) {
	const doc = `{"version":1,"all_of":[{"limit":"workspace","default":"include","rules":[` +
		`{"action":"exclude","kind":"dir","path":"apps/api"},` +
		`{"action":"include","kind":"glob","pattern":"packages/*/package.json"}]}],"lockfiles":["pnpm-lock.yaml"]}`
	var s Set
	if err := json.Unmarshal([]byte(doc), &s); err != nil {
		t.Fatal(err)
	}
	m := mustCompile(t, s)
	for p, want := range map[string]bool{"apps/api/x": false, "apps/web/x": true, "packages/a/package.json": true} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	out, err := json.Marshal(s)
	if err != nil || string(out) != doc {
		t.Errorf("round trip = %s, %v", out, err)
	}
}

// refSetMatch is the naive evaluation: every rule of every ruleset, in order.
// It mirrors testdata/php/compiled_matcher.php.
func refSetMatch(s Set, path string) bool {
	for _, rs := range s.AllOf {
		action := rs.Default
		for _, r := range rs.Rules {
			var ok bool
			switch r.Kind {
			case Dir:
				ok = path == r.Path || strings.HasPrefix(path, r.Path+"/")
			case File:
				ok = path == r.Path
			case Glob:
				ok = refGlobMatch(r.Pattern, path)
			}
			if ok {
				action = r.Action
			}
		}
		if action != Include {
			return false
		}
	}
	return true
}
