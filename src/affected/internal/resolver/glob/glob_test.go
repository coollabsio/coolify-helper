package glob

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"packages/*", "packages/a", true},
		{"packages/*", "packages/a/b", false},
		{"packages/*", "packages", false},
		{"packages/*", "packages/.hidden", false},
		{"packages/.*", "packages/.hidden", true},
		{"packages/**", "packages", true},
		{"packages/**", "packages/a/b/c", true},
		{"packages/**", "packages/.x/a", false},
		{"**/test/**", "packages/test", true},
		{"**/test/**", "test/a", true},
		{"apps/*-web", "apps/acme-web", true},
		{"apps/?", "apps/a", true},
		{"apps/?", "apps/ab", false},
		{"apps/[ab]*", "apps/alpha", true},
		{"apps/[!ab]*", "apps/alpha", false},
		{"apps/[^ab]*", "apps/zeta", true},
		{"apps/[a-c]", "apps/b", true},
		{"apps/[]]", "apps/]", true},
		{"apps/é*", "apps/éa", true},
		{"tools/x", "tools/x", true},
		{"**", "", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/**/b", "a/x/b", true},
	}
	for _, tt := range tests {
		got, err := Match(tt.pattern, tt.path)
		if err != nil || got != tt.want {
			t.Errorf("Match(%q, %q) = %v, %v; want %v", tt.pattern, tt.path, got, err, tt.want)
		}
	}
	for _, p := range []string{"a/@(b|c)", "a/\\*", "a/[b", "a/[[:alpha:]]", "a/!(b)", "a/+(b)"} {
		if _, err := Match(p, "a/b"); err == nil {
			t.Errorf("Match(%q): want an error", p)
		}
	}
}

func TestExpand(t *testing.T) {
	got, err := Expand("{apps,libs}/{a,b{1,2}}")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"apps/a", "apps/b1", "apps/b2", "libs/a", "libs/b1", "libs/b2"}
	if len(got) != len(want) {
		t.Fatalf("Expand = %v", got)
	}
	m := map[string]bool{}
	for _, g := range got {
		m[g] = true
	}
	for _, w := range want {
		if !m[w] {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	if got, _ := Expand("{a}"); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Expand({a}) = %v", got)
	}
	if _, err := Expand("{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}{a,b}"); err == nil {
		t.Errorf("want an expansion cap error")
	}
}

func TestNameMatch(t *testing.T) {
	for _, tt := range []struct {
		p, n string
		want bool
	}{
		{"@acme/*", "@acme/ui", true}, {"@acme/*", "@other/ui", false}, {"*web*", "my-web-app", true},
		{"web", "web", true}, {"web", "webx", false}, {"a*b*c", "aXbYc", true}, {"a*b*c", "aXc", false},
	} {
		if got := NameMatch(tt.p, tt.n); got != tt.want {
			t.Errorf("NameMatch(%q, %q) = %v", tt.p, tt.n, got)
		}
	}
}

func TestToRules(t *testing.T) {
	tests := []struct {
		dir, pattern string
		want         []Rule
	}{
		{"", ".env", []Rule{{match.Dir, ".env"}}},
		{"", "config/", []Rule{{match.Dir, "config"}}},
		{"", "**/.env.*local", []Rule{{match.Glob, "**/.env.*local"}}},
		{"", "{a,b}.json", []Rule{{match.Dir, "a.json"}, {match.Dir, "b.json"}}},
		{"", "scripts/[xy].sh", []Rule{{match.Glob, "scripts/?.sh"}}},
		{"", "a/b**c/d", []Rule{{match.Glob, "a/b*c/d"}}},
		{"apps/api", "../../proto/**", []Rule{{match.Glob, "proto/**"}}},
	}
	for _, tt := range tests {
		got, err := ToRules(tt.dir, tt.pattern)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ToRules(%q, %q) = %v, %v; want %v", tt.dir, tt.pattern, got, err, tt.want)
		}
	}
	for _, p := range []string{"../x", "/abs", "@(a|b)", "a\x00b"} {
		if _, err := ToRules("", p); err == nil {
			t.Errorf("ToRules(%q): want an error", p)
		}
	}
}

// Patterns that make a backtracking matcher exponential finish quickly.
func TestMatchPathological(t *testing.T) {
	cases := []struct{ pattern, path string }{
		{"p/*a*a*a*a*a*a*a*a*a*a*a*a*a*a*b", "p/" + strings.Repeat("a", 40)},
		{"**/a/**/a/**/a/**/a/**/a/**/b", strings.TrimSuffix(strings.Repeat("a/", 40), "/")},
		{"*?*?*?*?*?*?*?*?*?*?*?*?*b", strings.Repeat("a", 60)},
		{"*[a]*[a]*[a]*[a]*[a]*[a]*[a]*[a]*[a]*[a]*b", strings.Repeat("a", 60)},
	}
	for _, c := range cases {
		done := make(chan bool, 1)
		start := time.Now()
		go func() {
			ok, _ := Match(c.pattern, c.path)
			done <- ok
		}()
		select {
		case ok := <-done:
			if ok {
				t.Errorf("%s matched", c.pattern)
			}
			if d := time.Since(start); d > 100*time.Millisecond {
				t.Errorf("%s took %v", c.pattern, d)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s hangs", c.pattern)
		}
	}
	// Still exact.
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"p/*a*b", "p/xxaxxb", true}, {"p/*a*b", "p/xxbxxa", false}, {"a/**/b", "a/.x/b", false}, {"a/**/b", "a/x/y/b", true},
		{"?é?", "aéb", true}, {"[!a]x", "bx", true}, {"[!a]x", "ax", false}, {"*.ts", ".x.ts", false},
	} {
		if got, err := Match(c.pattern, c.path); err != nil || got != c.want {
			t.Errorf("Match(%q, %q) = %v, %v", c.pattern, c.path, got, err)
		}
	}
}
