package dockerignore

import (
	"bytes"
	"strings"
	"testing"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
)

// excluded evaluates rules the way the compiled set does (the last matching
// rule wins) and reports whether the last match is an exclude.
func excluded(rules []match.Rule, path string) bool {
	ex := false
	for _, r := range rules {
		if ruleMatches(r, path) {
			ex = r.Action == match.Exclude
		}
	}
	return ex
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

// reference is BuildKit's decision for a path relative to the context.
func reference(t testing.TB, content string, rel string) bool {
	t.Helper()
	lines, err := ignorefile.ReadAll(bytes.NewReader([]byte(content)))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	pm, err := patternmatcher.New(lines)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ex, err := pm.MatchesOrParentMatches(rel)
	if err != nil {
		t.Fatalf("MatchesOrParentMatches(%q): %v", rel, err)
	}
	return ex
}

var testSegments = []string{"a", "b", "foo", "foo.txt", "foo.md", ".git", "x y", "node_modules", "é", "bar.json", "src", "Dockerfile", ".dockerignore", "README.md", "ab", "a.b", "dist", "fo", "foobar", "$x", "(1)", "c+d"}

// testPaths returns every path of up to three segments of testSegments.
func testPaths() []string {
	var out []string
	for _, a := range testSegments {
		out = append(out, a)
		for _, b := range testSegments {
			out = append(out, a+"/"+b)
			for _, c := range []string{"a", "foo", "x.md", "é"} {
				out = append(out, a+"/"+b+"/"+c)
			}
		}
	}
	return out
}

func compare(t *testing.T, content string) {
	t.Helper()
	paths := testPaths()
	for _, ctx := range []string{"", "apps/web"} {
		rules, ok, reason := Compile(ctx, []byte(content))
		if !ok {
			t.Fatalf("Compile(%q, %q) failed: %s", ctx, content, reason)
		}
		for _, rel := range paths {
			full := rel
			if ctx != "" {
				full = ctx + "/" + rel
			}
			want := reference(t, content, rel)
			if got := excluded(rules, full); got != want {
				t.Errorf("content %q, context %q, path %q: excluded=%v, BuildKit=%v (rules %+v)", content, ctx, rel, got, want, rules)
			}
		}
		// Nothing outside the context is touched.
		for _, outside := range []string{"apps/webx", "apps", "other/a", "apps/web2/foo"} {
			if ctx != "" && excluded(rules, outside) {
				t.Errorf("content %q: path %q outside the context is excluded", content, outside)
			}
		}
	}
}

func TestCompileMatchesBuildKit(t *testing.T) {
	cases := []string{
		"",
		"# only a comment\n\n",
		"foo",
		"/foo",
		"foo/",
		"./foo",
		"foo/bar.json",
		"a/b",
		"*",
		"*.md",
		"**",
		"**/*.md",
		"**/foo",
		"**/foo/a",
		"foo/**",
		"foo/**/a",
		"a/*/**",
		"*/foo",
		"f?o",
		"fo?",
		"?",
		"*\n!foo",
		"*\n!foo\nfoo/a",
		"*.md\n!README.md",
		"**\n!src\n!Dockerfile",
		"node_modules\n.git\n*.md\n!README.md",
		"foo\n!foo/a",
		"foo/**\n!foo/a",
		"!foo",
		".",
		"/",
		"..",
		"../foo",
		"foo/../bar",
		"a/./b",
		"  foo  ",
		"!  foo",
		"\ufefffoo",
		"$x",
		"(1)",
		"c+d",
		"a|b",
		"a{1}",
		"a.b",
		"x y",
		"é",
		"*/*/*",
		"**/**",
		"**/**/a",
		"**/a/**",
		"foo*",
		"*foo*",
		"*.txt\n!foo.txt\nfoo.txt",
		"#comment\n  # not a comment\nfoo",
	}
	for _, c := range cases {
		compare(t, c)
	}
}

func TestCompileRules(t *testing.T) {
	rules, ok, _ := Compile("apps/web", []byte("node_modules\n*.md\n!README.md\nfoo/**\n**\n"))
	if !ok {
		t.Fatal("Compile failed")
	}
	want := []match.Rule{
		{Action: match.Exclude, Kind: match.Dir, Path: "apps/web/node_modules"},
		{Action: match.Exclude, Kind: match.Glob, Pattern: "apps/web/*.md/**"},
		{Action: match.Include, Kind: match.Dir, Path: "apps/web/README.md"},
		{Action: match.Exclude, Kind: match.Glob, Pattern: "apps/web/foo/*/**"},
		{Action: match.Exclude, Kind: match.Glob, Pattern: "apps/web/*/**"},
	}
	if len(rules) != len(want) {
		t.Fatalf("got %+v, want %+v", rules, want)
	}
	for i := range want {
		if rules[i] != want[i] {
			t.Errorf("rule %d: got %+v, want %+v", i, rules[i], want[i])
		}
	}
}

func TestCompileSkipsPatternsThatMatchNothing(t *testing.T) {
	rules, ok, _ := Compile("", []byte(".\n/\n..\n../foo\n!../bar\n"))
	if !ok || len(rules) != 0 {
		t.Fatalf("got ok=%v rules=%+v, want no rules", ok, rules)
	}
}

func TestCompileFails(t *testing.T) {
	cases := map[string]string{
		"class":            "[ab]",
		"class in segment": "foo/b[a-z]r",
		"bracket":          "foo]",
		"escape":           `foo\*`,
		"trailing escape":  `foo\`,
		"double star":      "foo**",
		"double star head": "**foo",
		"triple star":      "a/***/b",
		"caret":            "a^*",
		"pipe":             "|*",
		"brace":            "a{1}*",
		"lone bang":        "!",
		"nul":              "foo\x00",
		"invalid utf8":     "foo\xff*",
		"too long line":    strings.Repeat("a", 70000),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if rules, ok, reason := Compile("", []byte(content)); ok {
				t.Errorf("Compile(%q) = %+v, want failure", content, rules)
			} else if reason == "" {
				t.Error("no reason")
			}
		})
	}
	if _, ok, _ := Compile("", bytes.Repeat([]byte("a\n"), MaxPatterns+1)); ok {
		t.Error("too many patterns: want failure")
	}
	if _, ok, _ := Compile("", bytes.Repeat([]byte("a"), MaxBytes+1)); ok {
		t.Error("too large: want failure")
	}
	if _, ok, _ := Compile("we*rd", []byte("*.md")); ok {
		t.Error("wildcard context with a wildcard pattern: want failure")
	}
	if _, ok, _ := Compile("we*rd", []byte("foo.md")); !ok {
		t.Error("wildcard context with a literal pattern: want success")
	}
}

// FuzzDockerignore checks that whenever Compile succeeds, the rules agree with
// patternmatcher on the fuzzed path and on a fixed set of paths.
func FuzzDockerignore(f *testing.F) {
	f.Add("*.md\n!README.md", "README.md")
	f.Add("**/foo/**\n!a", "x/foo/a")
	f.Add("a/*/**", "a/b")
	f.Add("f?o\n**", "é/foo")
	f.Add("node_modules\n.git", "node_modules/x")
	fixed := []string{"a", "a/b", "foo", "foo/a", "x/foo", "x/foo/a/b", "README.md", "é", "a.md", "b/c.md"}
	f.Fuzz(func(t *testing.T, content, path string) {
		rules, ok, _ := Compile("", []byte(content))
		if !ok {
			return
		}
		for _, r := range rules {
			if r.Kind == match.Glob {
				if err := match.ValidateGlob(r.Pattern); err != nil {
					t.Fatalf("invalid glob %q: %v", r.Pattern, err)
				}
			}
		}
		lines, err := ignorefile.ReadAll(bytes.NewReader([]byte(content)))
		if err != nil {
			t.Fatalf("Compile succeeded but ReadAll failed: %v", err)
		}
		pm, err := patternmatcher.New(lines)
		if err != nil {
			t.Fatalf("Compile succeeded but New failed: %v", err)
		}
		paths := fixed
		if validPath(path) {
			paths = append([]string{path}, fixed...)
		}
		for _, p := range paths {
			want, err := pm.MatchesOrParentMatches(p)
			if err != nil {
				continue
			}
			if got := excluded(rules, p); got != want {
				t.Fatalf("content %q path %q: excluded=%v, BuildKit=%v (rules %+v)", content, p, got, want, rules)
			}
		}
	})
}

func validPath(p string) bool {
	if p == "" || strings.IndexByte(p, 0) >= 0 || strings.ContainsRune(p, '\\') {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return true
}
