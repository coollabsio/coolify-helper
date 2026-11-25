package pathx

import "testing"

func TestClean(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{"/", "", true},
		{"/apps/web", "apps/web", true},
		{"apps/web/", "apps/web", true},
		{"./apps//web/.", "apps/web", true},
		{"apps/../web", "web", true},
		{"..", "", false},
		{"../x", "", false},
		{"apps/../../x", "", false},
		{"a\x00b", "", false},
		{"//etc/passwd", "etc/passwd", true},
	}
	for _, c := range cases {
		got, ok := Clean(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("Clean(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCleanStrictRejectsDotDot(t *testing.T) {
	for _, p := range []string{"apps/../web", "..", "a/..", "a\x00"} {
		if _, ok := CleanStrict(p); ok {
			t.Errorf("CleanStrict(%q) accepted", p)
		}
	}
	if got, ok := CleanStrict("/apps/web/Dockerfile"); !ok || got != "apps/web/Dockerfile" {
		t.Errorf("CleanStrict = %q, %v", got, ok)
	}
}

func TestJoin(t *testing.T) {
	cases := []struct {
		dir, rel, want string
		ok             bool
	}{
		{"apps/web", "../../packages/ui", "packages/ui", true},
		{"apps/web", "../../../x", "", false},
		{"", "a/b", "a/b", true},
		{"apps/web", "/abs", "abs", true},
		{"apps", ".", "apps", true},
	}
	for _, c := range cases {
		got, ok := Join(c.dir, c.rel)
		if got != c.want || ok != c.ok {
			t.Errorf("Join(%q, %q) = %q, %v; want %q, %v", c.dir, c.rel, got, ok, c.want, c.ok)
		}
	}
}

func TestWithinAndRel(t *testing.T) {
	if !Within("apps/web/x", "apps/web") || !Within("apps/web", "apps/web") || Within("apps/web2", "apps/web") || !Within("x", "") {
		t.Error("Within")
	}
	if Rel("apps/web/x", "apps/web") != "x" || Rel("apps/web", "apps/web") != "" || Rel("a", "") != "a" {
		t.Error("Rel")
	}
	if Dir("a/b/c") != "a/b" || Dir("a") != "" || Display("") != "/" {
		t.Error("Dir/Display")
	}
}
