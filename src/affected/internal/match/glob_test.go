package match

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateGlob(t *testing.T) {
	valid := []string{"*", "**", "a", "a/b", "a/**", "**/x", "a/**/b", "*.md", "a?c", "[x]", "{a,b}", `a\b`, "a**b", "***", ".env*", "café/*"}
	for _, p := range valid {
		if err := ValidateGlob(p); err != nil {
			t.Errorf("ValidateGlob(%q) = %v, want nil", p, err)
		}
	}
	invalid := []string{"", "/", "/a", "a/", "a//b", "a/./b", "./a", "a/..", "a\x00b"}
	for _, p := range invalid {
		if err := ValidateGlob(p); err == nil {
			t.Errorf("ValidateGlob(%q) = nil, want an error", p)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		// "*" stays in one segment.
		{"*", "a", true},
		{"*", "a/b", false},
		{"*.md", "README.md", true},
		{"*.md", "docs/README.md", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXbY", false},
		{"a*", "a", true},
		// "**" as a whole segment is zero or more segments.
		{"**", "a", true},
		{"**", "a/b/c", true},
		{"a/**", "a", true},
		{"a/**", "a/b", true},
		{"a/**", "a/b/c", true},
		{"a/**", "ab", false},
		{"a/**", "b/a", false},
		{"**/x", "x", true},
		{"**/x", "d/x", true},
		{"**/x", "d/e/x", true},
		{"**/x", "xx", false},
		{"**/x", "d/xx", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
		{"a/**/b", "ab", false},
		{"**/node_modules/**", "node_modules", true},
		{"**/node_modules/**", "a/node_modules/b/c", true},
		{"**/node_modules/**", "a/node_modules2/b", false},
		{"**/**/x", "x", true},
		{"**/a/**/a/**/b", "a/a/b", true},
		{"**/a/**/a/**/b", "x/a/y/z/a/b", true},
		{"**/a/**/a/**/b", "x/a/y/z/b", false},
		// A "**" that is not a whole segment is two "*".
		{"a**b", "aXb", true},
		{"a**b", "a/b", false},
		{"a**", "a/b", false},
		{"**b", "a/b", false},
		{"***", "abc", true},
		{"***", "a/b", false},
		{"x/***/y", "x/y", false},
		{"x/***/y", "x/z/y", true},
		// "?" is one character except "/".
		{"?", "a", true},
		{"?", "", false},
		{"?", "ab", false},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"caf?", "café", true}, // é is one character (2 bytes)
		{"caf??", "café", false},
		{"caf?", "café", false}, // NFD: e + combining acute accent
		{"caf??", "café", true},
		{"?", "😀", true}, // 4 bytes
		{"??", "😀", false},
		{"?", "\xff", true},   // an invalid byte is one character
		{"??", "\xc3", false}, // a truncated sequence is one byte
		{"*?", "\xf0\x9f\x98\x80", true},
		// No classes, braces or escapes.
		{"[ab].txt", "[ab].txt", true},
		{"[ab].txt", "a.txt", false},
		{"{a,b}", "{a,b}", true},
		{"{a,b}", "a", false},
		{`a\*`, `a\*`, true},
		{`a\*`, `a\x`, true}, // "\" is literal and "*" still a wildcard
		{`a\*`, "a*", false},
		{"#*", "#1", true},
		{"pages/[id]/*.tsx", "pages/[id]/index.tsx", true},
		// Byte-exact and case-sensitive.
		{"README.md", "readme.md", false},
		{"café", "café", false},
		// Invalid patterns match nothing.
		{"", "", false},
		{"/a", "/a", false},
		{"a/", "a/", false},
	}
	for _, tt := range tests {
		if got := GlobMatch(tt.pattern, tt.path); got != tt.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
		if got := refGlobMatch(tt.pattern, tt.path); got != tt.want && ValidateGlob(tt.pattern) == nil {
			t.Errorf("refGlobMatch(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestGlobMatchLinear(t *testing.T) {
	// Patterns that make a naive backtracking matcher exponential.
	path := strings.Repeat("a/", 200) + "c"
	if GlobMatch(strings.Repeat("**/a/", 20)+"b", path) {
		t.Error("deep ** pattern matched")
	}
	seg := strings.Repeat("a", 5000)
	if GlobMatch(strings.Repeat("*a", 30)+"b", seg) {
		t.Error("star pattern matched")
	}
	if GlobMatch(strings.Repeat("*a?", 30)+"b", seg) {
		t.Error("star and question pattern matched")
	}
}

// refGlobMatch is an independent, deliberately naive glob matcher for the
// tests: it works on the whole path, byte by byte, with plain backtracking.
// Exponential in the worst case: keep inputs short.
func refGlobMatch(pattern, path string) bool {
	if ValidateGlob(pattern) != nil {
		return false
	}
	return refMatch(refTokens(pattern), path)
}

type refToken struct {
	kind byte // 'l' literal byte, '*' star, '?' any, 'A' all ("**" alone), 'P' "**/" prefix, 'S' "/**" suffix
	b    byte
}

func refTokens(pattern string) []refToken {
	var segs []string
	for _, s := range strings.Split(pattern, "/") {
		if s == "**" && len(segs) > 0 && segs[len(segs)-1] == "**" {
			continue // "**/**" is "**"
		}
		segs = append(segs, s)
	}
	var toks []refToken
	for i, s := range segs {
		last := i == len(segs)-1
		if s == "**" {
			switch {
			case len(segs) == 1:
				toks = append(toks, refToken{kind: 'A'})
			case last:
				// Replace the "/" written after the previous segment.
				toks[len(toks)-1] = refToken{kind: 'S'}
			default:
				toks = append(toks, refToken{kind: 'P'})
			}
			continue
		}
		for j := 0; j < len(s); j++ {
			switch s[j] {
			case '*', '?':
				toks = append(toks, refToken{kind: s[j]})
			default:
				toks = append(toks, refToken{kind: 'l', b: s[j]})
			}
		}
		if !last {
			toks = append(toks, refToken{kind: 'l', b: '/'})
		}
	}
	return toks
}

func refMatch(toks []refToken, s string) bool {
	if len(toks) == 0 {
		return s == ""
	}
	t, rest := toks[0], toks[1:]
	switch t.kind {
	case 'l':
		return s != "" && s[0] == t.b && refMatch(rest, s[1:])
	case '?':
		if s == "" || s[0] == '/' {
			return false
		}
		_, w := utf8.DecodeRuneInString(s)
		return refMatch(rest, s[w:])
	case '*':
		for i := 0; ; i++ {
			if refMatch(rest, s[i:]) {
				return true
			}
			if i == len(s) || s[i] == '/' {
				return false
			}
		}
	case 'A':
		return true
	case 'P':
		// Zero or more segments, each with its trailing "/".
		if refMatch(rest, s) {
			return true
		}
		for i := 0; i < len(s); i++ {
			if s[i] == '/' && refMatch(rest, s[i+1:]) {
				return true
			}
		}
		return false
	case 'S':
		// The "/" before a final "**" is optional: "a/**" matches "a".
		return s == "" || s[0] == '/'
	}
	panic("unknown token")
}
