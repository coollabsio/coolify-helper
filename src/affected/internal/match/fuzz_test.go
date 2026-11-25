package match

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// FuzzGlobMatch checks the compiled glob matcher against refGlobMatch, an
// independent naive implementation.
func FuzzGlobMatch(f *testing.F) {
	seeds := [][2]string{
		{"**", "a/b"}, {"a/**", "a"}, {"**/x", "x"}, {"a/**/b", "a/x/y/b"}, {"a**b", "a/b"},
		{"*.md", "README.md"}, {"?", "é"}, {"??", "é"}, {"*?", "\xf0\x9f\x98\x80"},
		{"*?*\x80", "\xf0\x9f\x98\x80"}, {"**/**/x", "x"}, {"[a]", "[a]"}, {"a/*/b", "a//b"},
		{"*", ""}, {"**", "/a"}, {"x/**", "x/"}, {"?*?", "\xc3\xa9\xc3"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		if len(pattern) > 40 || len(path) > 48 || strings.Count(pattern, "*") > 8 {
			return // refGlobMatch backtracks exponentially
		}
		want := refGlobMatch(pattern, path)
		if got := GlobMatch(pattern, path); got != want {
			t.Fatalf("GlobMatch(%q, %q) = %v, reference %v", pattern, path, got, want)
		}
		if ValidateGlob(pattern) != nil {
			return
		}
		inc := mustCompile(t, set(ruleset("x", Exclude, IncludeGlob(pattern, nil))))
		if got := inc.MatchPath(NewPath(path)); got != want {
			t.Fatalf("include %q: MatchPath(%q) = %v, reference %v", pattern, path, got, want)
		}
		exc := mustCompile(t, set(ruleset("x", Include, ExcludeGlob(pattern, nil))))
		if got := exc.Match(path); got == want {
			t.Fatalf("exclude %q: Match(%q) = %v, reference %v", pattern, path, got, !want)
		}
	})
}

// FuzzCompiledSet checks the trie matcher against refSetMatch, the naive
// rule-by-rule evaluation, for sets decoded from the fuzz input.
func FuzzCompiledSet(f *testing.F) {
	f.Add([]byte{0x12, 0x34, 0x56, 0x78, 0x9a}, "a/b")
	f.Add([]byte{0xff, 0x00, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70}, "ab/a/b")
	f.Add([]byte("some longer seed with many rules in it"), "a/é/b")
	f.Fuzz(func(t *testing.T, data []byte, path string) {
		if len(path) > 48 {
			return
		}
		s := setFromBytes(data)
		m, err := Compile(s)
		if err != nil {
			t.Fatalf("Compile(%+v): %v", s, err)
		}
		want := refSetMatch(s, path)
		if got := m.MatchPath(NewPath(path)); got != want {
			t.Fatalf("set %+v: MatchPath(%q) = %v, reference %v", s, path, got, want)
		}
		got, ds := m.Explain(path)
		if got != want || len(ds) != len(s.AllOf) {
			t.Fatalf("set %+v: Explain(%q) = %v, reference %v", s, path, got, want)
		}
		for i, d := range ds {
			if d.Included != refSetMatch(Set{Version: 1, AllOf: s.AllOf[i : i+1]}, path) {
				t.Fatalf("set %+v: Explain(%q) decision %d = %+v", s, path, i, d)
			}
		}
	})
}

var fuzzSegs = []string{"a", "b", "ab", "é", "é", ".x", "*", "**", "?", "a*", "*b", "?b", "a**", "\xc3"}

// setFromBytes decodes a small compiled set from fuzz bytes.
func setFromBytes(data []byte) Set {
	next := func() int {
		if len(data) == 0 {
			return 0
		}
		c := data[0]
		data = data[1:]
		return int(c)
	}
	s := set()
	for range next() % 4 {
		rs := ruleset("x", []Action{Include, Exclude}[next()%2])
		for range next() % 6 {
			c := next()
			r := Rule{Action: []Action{Include, Exclude}[c%2], Kind: []Kind{Dir, File, Glob}[c/2%3]}
			var segs []string
			for range 1 + next()%3 {
				seg := fuzzSegs[next()%len(fuzzSegs)]
				if r.Kind != Glob {
					seg = strings.NewReplacer("*", "s", "?", "q").Replace(seg)
				}
				segs = append(segs, seg)
			}
			if r.Kind == Glob {
				r.Pattern = strings.Join(segs, "/")
			} else {
				r.Path = strings.Join(segs, "/")
			}
			rs.Rules = append(rs.Rules, r)
		}
		s.AllOf = append(s.AllOf, rs)
	}
	return s
}

// FuzzWatchPaths checks that the watch paths port does not panic, and that it
// agrees with a direct translation of each glob to a Go regexp for patterns
// without brackets (whose PCRE parsing is checked against PHP instead).
func FuzzWatchPaths(f *testing.F) {
	for _, s := range watchFuzzSeeds {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, value, file string) {
		if len(value) > 256 || len(file) > 128 {
			return // real values are short; long ones only slow the fuzzer down
		}
		w := CompileWatchPaths(value)
		got := w.Match(file)
		_ = w.Blank()
		if w.Widened() {
			if !got {
				t.Fatalf("widened %q: Match(%q) = false", value, file)
			}
			return
		}
		want, ok := directWatchMatch(value, file)
		if ok && got != want {
			t.Fatalf("watch paths %q: Match(%q) = %v, direct translation %v", value, file, got, want)
		}
	})
}

var watchFuzzSeeds = [][2]string{
	{"apps/**", "apps"}, {"a/**/b", "a/b"}, {"!docs/**", "src/x"}, {"*.yml\n!a.yml", "a.yml"},
	{"a", "a\n"}, {"a**b", "a\nb"}, {"caf?", "café"}, {"c#/**", "c#/x"}, {"[a-c-e]", "-"},
	{"[]?", "]"}, {"[^]?", "b"}, {"[a?]", "/]"}, {"/ / /x", "/x"}, {"0", "0"}, {"a\n0", "0"},
	{"!", ""}, {"[[:digit:]]", "1"}, {"[.-0]", "/"}, {"\x00a\x00", "a"}, {"(a)+{b}|$^\\", "x"},
}

// directWatchMatch evaluates watch paths with a direct glob to Go regexp
// translation. ok is false when a pattern has "[" or "]".
func directWatchMatch(value, file string) (match, ok bool) {
	lines := strings.Split(effectiveWatchPaths(value), "\n")
	subject := latin1(file)
	var decided, include, hasInclusion bool
	for _, line := range lines {
		p := strings.Trim(line, phpTrimSet)
		if !strings.HasPrefix(p, "!") {
			hasInclusion = true
		}
		if p == "" || p == "0" {
			continue
		}
		exclude := strings.HasPrefix(p, "!")
		if exclude {
			p = p[1:]
		}
		if strings.ContainsAny(p, "[]") {
			return false, false
		}
		if strings.Contains(p, "#") {
			continue // the PHP pattern is invalid: never matches
		}
		if directRegexp(p).MatchString(subject) {
			decided, include = true, !exclude
		}
	}
	if !decided {
		return !hasInclusion, true
	}
	return include, true
}

func directRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`^(?:`)
	for i := 0; i < len(glob); i++ {
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString(`[^\n]*`)
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(`[^\n]*`)
			i++
		case glob[i] == '*':
			b.WriteString(`[^/]*`)
		case glob[i] == '?':
			b.WriteString(`[^/]`)
		default:
			fmt.Fprintf(&b, `\x{%x}`, glob[i])
		}
	}
	b.WriteString(`)\n?$`)
	return regexp.MustCompile(b.String())
}
