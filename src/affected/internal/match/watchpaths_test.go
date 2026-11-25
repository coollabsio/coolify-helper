package match

import (
	"slices"
	"testing"
)

// matchWatchPaths mirrors the helper of Coolify's tests/Unit/ApplicationWatchPathsTest.php:
// Application::matchPaths over raw (not normalized) patterns. A nil list is PHP's null.
func matchWatchPaths(files []string, patterns []string) []string {
	if patterns == nil {
		return []string{}
	}
	out := []string{}
	for _, f := range files {
		if matchPatterns(patterns, f) {
			out = append(out, f)
		}
	}
	return out
}

type watchExpect struct {
	files []string
	want  []string // nil: expect no match; otherwise exactly these
	any   bool     // expect a non-empty result
}

// The tests of Coolify's tests/Unit/ApplicationWatchPathsTest.php, ported.
func TestWatchPathsCoolifyUnitTests(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		checks   []watchExpect
	}{
		{"returns false when watch paths is null", nil, []watchExpect{
			{files: []string{"docker-compose.yml", "README.md"}},
		}},
		{"triggers with exact match", []string{"docker-compose.yml", "Dockerfile"}, []watchExpect{
			{files: []string{"docker-compose.yml"}, want: []string{"docker-compose.yml"}},
			{files: []string{"Dockerfile"}, want: []string{"Dockerfile"}},
			{files: []string{"README.md"}},
		}},
		{"triggers with wildcard patterns", []string{"*.yml", "src/**/*.php", "config/*"}, []watchExpect{
			{files: []string{"docker-compose.yml"}, any: true},
			{files: []string{"production.yml"}, any: true},
			{files: []string{"src/Controllers/UserController.php"}, any: true},
			{files: []string{"src/Models/User.php"}, any: true},
			{files: []string{"config/app.php"}, any: true},
			{files: []string{"README.md"}},
			{files: []string{"src/index.js"}},
			{files: []string{"configurations/deep/file.php"}},
		}},
		{"triggers with multiple files", []string{"docker-compose.yml", "*.env"}, []watchExpect{
			{files: []string{"README.md", "docker-compose.yml", "package.json"}, want: []string{"docker-compose.yml"}},
			{files: []string{"README.md", "package.json", "src/index.js"}},
		}},
		{"handles leading slash include", []string{"/docs/**"}, []watchExpect{
			{files: []string{"docs/index.md"}},
		}},
		{"handles leading slash negation", []string{"!/docs/**"}, []watchExpect{
			{files: []string{"docs/index.md"}, want: []string{"docs/index.md"}},
			{files: []string{"src/app.ts"}, want: []string{"src/app.ts"}},
		}},
		{"triggers with complex patterns", []string{"**/*.js", "**/*.jsx", "**/*.ts", "**/*.tsx"}, []watchExpect{
			{files: []string{"src/index.js"}, any: true},
			{files: []string{"components/Button.jsx"}, any: true},
			{files: []string{"types/user.ts"}, any: true},
			{files: []string{"pages/Home.tsx"}, any: true},
			{files: []string{"src/components/ui/Button.tsx"}, any: true},
			{files: []string{"README.md"}},
			{files: []string{"package.json"}},
		}},
		{"triggers with question mark pattern", []string{"test?.txt", "file-?.yml"}, []watchExpect{
			{files: []string{"test1.txt"}, any: true},
			{files: []string{"testA.txt"}, any: true},
			{files: []string{"file-1.yml"}, any: true},
			{files: []string{"file-B.yml"}, any: true},
			{files: []string{"test.txt"}},
			{files: []string{"test12.txt"}},
			{files: []string{"file.yml"}},
		}},
		{"triggers with character set pattern", []string{"[abc]test.txt", "file[0-9].yml"}, []watchExpect{
			{files: []string{"atest.txt"}, any: true},
			{files: []string{"btest.txt"}, any: true},
			{files: []string{"ctest.txt"}, any: true},
			{files: []string{"file1.yml"}, any: true},
			{files: []string{"file9.yml"}, any: true},
			{files: []string{"dtest.txt"}},
			{files: []string{"test.txt"}},
			{files: []string{"fileA.yml"}},
		}},
		{"triggers with empty watch paths", []string{}, []watchExpect{
			{files: []string{"any-file.txt"}},
		}},
		{"triggers with whitespace only patterns", []string{"", "  ", "\t"}, []watchExpect{
			{files: []string{"any-file.txt"}},
		}},
		{"triggers for docker compose typical patterns", []string{"docker-compose*.yml", ".env*", "Dockerfile*", "services/**"}, []watchExpect{
			{files: []string{"docker-compose.yml"}, any: true},
			{files: []string{"docker-compose.prod.yml"}, any: true},
			{files: []string{"docker-compose-dev.yml"}, any: true},
			{files: []string{".env"}, any: true},
			{files: []string{".env.local"}, any: true},
			{files: []string{".env.production"}, any: true},
			{files: []string{"Dockerfile"}, any: true},
			{files: []string{"Dockerfile.prod"}, any: true},
			{files: []string{"services/api/app.js"}, any: true},
			{files: []string{"services/web/index.html"}, any: true},
			{files: []string{"README.md"}},
			{files: []string{"package.json"}},
			{files: []string{"config/nginx.conf"}},
		}},
		{"handles negation pattern with non matching file", []string{"!docker-compose-test/**"}, []watchExpect{
			{files: []string{"docker-compose/index.ts"}, want: []string{"docker-compose/index.ts"}},
			{files: []string{"docker-compose-test/index.ts"}},
			{files: []string{"docker-compose-test/sub/dir/file.ts"}},
		}},
		{"handles mixed inclusion and exclusion patterns", []string{"**/*.js", "!**/*test*/**"}, []watchExpect{
			{files: []string{"src/index.js"}, any: true},
			{files: []string{"components/Button.js"}, any: true},
			{files: []string{"test/unit/app.js"}},
			{files: []string{"src/test-utils/helper.js"}},
			{files: []string{"docker-compose-test/index.js"}},
			{files: []string{"README.md"}},
		}},
		{"handles multiple negation patterns", []string{"!tests/**", "!docs/**", "!*.md"}, []watchExpect{
			{files: []string{"src/index.js"}, any: true},
			{files: []string{"docker-compose.yml"}, any: true},
			{files: []string{"tests/unit/test.js"}},
			{files: []string{"docs/api.html"}},
			{files: []string{"README.md"}},
			{files: []string{"CHANGELOG.md"}},
		}},
		{"order based matching: exclude then include", []string{"!docker-compose/**", "docker-compose/**"}, []watchExpect{
			{files: []string{"docker-compose/index.ts"}, want: []string{"docker-compose/index.ts"}},
		}},
		{"order based matching: include then exclude", []string{"docker-compose/**", "!docker-compose/**"}, []watchExpect{
			{files: []string{"docker-compose/index.ts"}},
		}},
		{"order based matching with multiple overlapping patterns", []string{"**/*.js", "!**/test/**", "src/test/unit.js"}, []watchExpect{
			{
				files: []string{"src/test/unit.js", "src/components/Button.js", "test/integration.js"},
				want:  []string{"src/test/unit.js", "src/components/Button.js"},
			},
		}},
		{"order based matching with specific overrides", []string{"!docs/**", "docs/internal/**", "src/**"}, []watchExpect{
			{
				files: []string{"docs/api.md", "docs/guide.md", "docs/internal/secret.md", "src/index.js"},
				want:  []string{"docs/internal/secret.md", "src/index.js"},
			},
		}},
		{"preserves order precedence: include wins", []string{"**/*.json", "!app/**", "app/*.json"}, []watchExpect{
			{files: []string{"app/config.json"}, want: []string{"app/config.json"}},
		}},
		{"preserves order precedence: exclude wins", []string{"**/*.json", "app/*.json", "!app/**"}, []watchExpect{
			{files: []string{"app/config.json"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, c := range tt.checks {
				got := matchWatchPaths(c.files, tt.patterns)
				switch {
				case c.any:
					if len(got) == 0 {
						t.Errorf("files %q: no match, want some", c.files)
					}
				case c.want == nil:
					if len(got) != 0 {
						t.Errorf("files %q: matched %q, want none", c.files, got)
					}
				default:
					if !slices.Equal(got, c.want) {
						t.Errorf("files %q: matched %q, want %q", c.files, got, c.want)
					}
				}
			}
		})
	}
}

func TestParseWatchPaths(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", false},
		{"0", "", false},
		{"a", "a", true},
		{"  a  \n\n b \r\n", "a\nb", true},
		{"/a\n//b\n///", "a\nb", true},
		{"! /docs/**", "!docs/**", true},
		{"!", "!", true},
		{"!//", "!", true},
		{"/ x\n/ y", "x\n y", true}, // only the first line's spaces go, by the final trim()
		{"/ !x", "!x", true},
		{"/0", "0", true},
		{"\x00a\x0b", "a", true},
		{"\fa", "\fa", true}, // trim() does not strip a form feed
	}
	for _, tt := range tests {
		got, ok := parseWatchPaths(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseWatchPaths(%q) = %q, %v, want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestEffectiveWatchPaths(t *testing.T) {
	tests := map[string]string{
		"a\nb":       "a\nb", // already normalized
		"/a":         "a",    // normalized once
		"/ /x":       "x",    // normalized twice (the set mutator parses again)
		"/ / /x":     "/x",   // and no more
		"0":          "",     // null
		"/0":         "",     // normalizes to "0": the set mutator stores null
		"0\n":        "",     // normalizes to "0" too
		"a\n0":       "a\n0", // "0" is skipped by matchPaths but counts as an inclusion
		"  ":         "",     // blank: the caller deploys
		"!a\n  /b  ": "!a\nb",
	}
	for in, want := range tests {
		if got := effectiveWatchPaths(in); got != want {
			t.Errorf("effectiveWatchPaths(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGlobToRegex(t *testing.T) {
	tests := map[string]string{
		"a/**/b":       "#^a/.*b$#",
		"apps/**":      "#^apps/.*$#",
		"*.yml":        "#^[^/]*\\.yml$#",
		"test?.txt":    "#^test[^/]\\.txt$#",
		"[!a]x":        "#^[^a]x$#",
		"[^a]x":        "#^[^a]x$#",
		"a]":           "#^a\\]$#",
		"[a]]":         "#^[a]\\]$#",
		"(a)+{b}$^|\\": "#^\\(a\\)\\+\\{b\\}\\$\\^\\|\\\\$#",
		"#x":           "#^#x$#",
		"":             "#^$#",
	}
	for in, want := range tests {
		if got := globToRegex(in); got != want {
			t.Errorf("globToRegex(%q) = %q, want %q", in, got, want)
		}
	}
}

// Quirks of the PHP implementation that the port keeps. The PHP parity tests
// check all of these against real PHP too (see the conformance fixture).
func TestWatchPathsQuirks(t *testing.T) {
	tests := []struct {
		name  string
		value string
		file  string
		want  bool
	}{
		{"** does not match the directory itself", "apps/**", "apps", false},
		{"** then / is consumed", "a/**/b", "a/b", true},
		{"** inside a segment crosses /", "a**b", "a/x/b", true},
		{"**.js crosses /", "**.js", "a/b.js", true},
		{"* stays in a segment", "*.yml", "a/b.yml", false},
		{"$ matches before a final newline", "a", "a\n", true},
		{"but not before other bytes", "a", "a\nb", false},
		{"** does not cross a newline", "a**b", "a\nb", false},
		{"* crosses a newline", "a*b", "a\nb", true},
		{"? is one byte", "caf?", "café", false},
		{"?? is two bytes", "caf??", "café", true},
		{"a class is bytes", "caf[é]", "café", false},
		{"# breaks the pattern", "c#/**", "c#/x", false},
		{"# pattern still counts as an inclusion", "c#/**\n!docs/**", "src/x", false},
		{"exclusions only include the rest", "!docs/**", "src/x", true},
		{"line 0 is skipped", "a\n0", "0", false},
		{"line 0 still counts as an inclusion", "!a\n0", "b", false},
		{"value /0 never matches", "/0", "0", false},
		{"! alone matches the empty path only", "!", "x", true},
		{"!! is an exclusion of !", "!!x", "!x", false},
		{"unterminated class never matches", "[ab", "[ab", false},
		{"reversed range never matches", "[z-a]", "m", false},
		{"] first in a class is literal", "[]?", "]", true},
		{"] first in a negated class is literal", "[^]?", "b", true},
		{"[]a] is unterminated: the last ] is escaped", "[]a]", "]", false},
		{"class swallows the ? expansion", "[a?]", "/]", true},
		{"class swallows the * expansion", "[a*]", "a]", true},
		{"class swallows the * expansion, repeated", "[a*]", "aa^/]", true},
		{"range of escaped bytes", "[.-0]x", "/x", true},
		{"dash after a range is literal", "[a-c-e]", "-", true},
		{"backslash is literal", `a\b`, `a\b`, true},
		{"regex specials are literal", "(a)+{b}|$^", "(a)+{b}|$^", true},
		{"CRLF value", "a\r\nb\r\n", "b", true},
		{"leading slashes stripped", "///docs/**", "docs/x", true},
		{"negation spaces and slashes stripped", "! //docs/**", "docs/x", false},
		{"case-sensitive", "Docs/**", "docs/x", false},
		{"nfc vs nfd", "café/**", "café/x", false},
		{"blank is not a match", "   ", "x", false},
	}
	for _, tt := range tests {
		w := CompileWatchPaths(tt.value)
		if w.Widened() {
			t.Errorf("%s: widened", tt.name)
		}
		if got := w.Match(tt.file); got != tt.want {
			t.Errorf("%s: CompileWatchPaths(%q).Match(%q) = %v, want %v", tt.name, tt.value, tt.file, got, tt.want)
		}
	}
}

func TestWatchPathsBlank(t *testing.T) {
	for _, v := range []string{"", " ", "\n\t\r\x00\x0b"} {
		if w := CompileWatchPaths(v); !w.Blank() || w.Match("x") {
			t.Errorf("CompileWatchPaths(%q): Blank %v, Match %v", v, w.Blank(), w.Match("x"))
		}
	}
	for _, v := range []string{"0", "/", "a", "\f"} {
		if CompileWatchPaths(v).Blank() {
			t.Errorf("CompileWatchPaths(%q).Blank() = true", v)
		}
	}
}

func TestWatchPathsWidened(t *testing.T) {
	for _, v := range []string{"[[:digit:]]", "[[=a=]]", "x/[:a:]", "[**]", "!**/[[:alpha:]]*"} {
		w := CompileWatchPaths(v)
		if !w.Widened() || !w.Match("anything") {
			t.Errorf("CompileWatchPaths(%q): Widened %v", v, w.Widened())
		}
	}
}
