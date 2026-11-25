// Package glob matches the glob patterns that workspace manifests use
// (pnpm-workspace.yaml, package.json workspaces, turbo.json, nx.json), and
// converts them to the small glob language of the compiled set.
//
// Matching is exact for what it supports: "*", "?", "[…]" classes, "**" as a
// whole segment, and braces (expanded). A "*", "?" or class does not match a
// leading "." of a segment unless the pattern segment starts with "." (the
// default of the tools' glob libraries). Anything else (extglobs, escapes) is an
// error: the caller can't tell what the pattern selects.
package glob

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
)

// maxExpansions caps brace expansion.
const maxExpansions = 256

// ErrUnsupported is returned for pattern syntax this package does not model.
var ErrUnsupported = errors.New("unsupported glob syntax")

// Expand expands braces: "a/{b,c}" → "a/b", "a/c". Nested braces work.
func Expand(p string) ([]string, error) {
	out := []string{p}
	for i := 0; i < maxExpansions; i++ {
		done := true
		var next []string
		for _, s := range out {
			open, close, ok := innermostBraces(s)
			if !ok {
				next = append(next, s)
				continue
			}
			done = false
			body := s[open+1 : close]
			if !strings.Contains(body, ",") {
				// "{a}" is literal in most libraries; treat it as the single choice.
				next = append(next, s[:open]+body+s[close+1:])
				continue
			}
			for _, alt := range strings.Split(body, ",") {
				next = append(next, s[:open]+alt+s[close+1:])
			}
			if len(next) > maxExpansions {
				return nil, fmt.Errorf("%w: too many brace expansions in %q", ErrUnsupported, p)
			}
		}
		out = dedupe(next)
		if done {
			return out, nil
		}
	}
	return nil, fmt.Errorf("%w: too many brace expansions in %q", ErrUnsupported, p)
}

func dedupe(list []string) []string {
	seen := map[string]bool{}
	out := list[:0]
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// innermostBraces finds the first "{…}" that contains no other brace.
func innermostBraces(s string) (int, int, bool) {
	open := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '{':
			open = i
		case '}':
			if open >= 0 {
				return open, i, true
			}
		}
	}
	return 0, 0, false
}

// HasMeta reports whether a pattern has glob syntax.
func HasMeta(p string) bool { return strings.ContainsAny(p, "*?[]{}\\!@+()") }

// checkSupported rejects syntax Match does not model.
func checkSupported(p string) error {
	if strings.Contains(p, "\\") {
		return fmt.Errorf("%w: escapes in %q", ErrUnsupported, p)
	}
	for _, x := range []string{"@(", "!(", "+(", "?(", "*("} {
		if strings.Contains(p, x) {
			return fmt.Errorf("%w: extglob in %q", ErrUnsupported, p)
		}
	}
	return nil
}

// Match reports whether a cleaned, slash-separated path matches a pattern
// (braces must already be expanded).
func Match(pattern, p string) (bool, error) {
	if err := checkSupported(pattern); err != nil {
		return false, err
	}
	var ps []string
	if pattern != "" {
		ps = strings.Split(pattern, "/")
	}
	var ss []string
	if p != "" {
		ss = strings.Split(p, "/")
	}
	return matchSegs(ps, ss)
}

func matchSegs(ps, ss []string) (bool, error) {
	for len(ps) > 0 {
		if ps[0] == "**" {
			// Collapse consecutive "**".
			for len(ps) > 1 && ps[1] == "**" {
				ps = ps[1:]
			}
			for k := 0; k <= len(ss); k++ {
				if k > 0 && strings.HasPrefix(ss[k-1], ".") {
					// "**" does not enter dot directories.
					return false, nil
				}
				ok, err := matchSegs(ps[1:], ss[k:])
				if err != nil || ok {
					return ok, err
				}
			}
			return false, nil
		}
		if len(ss) == 0 {
			return false, nil
		}
		ok, err := matchSegment(ps[0], ss[0])
		if err != nil || !ok {
			return false, err
		}
		ps, ss = ps[1:], ss[1:]
	}
	return len(ss) == 0, nil
}

// matchSegment matches one segment with "*", "?" and classes.
func matchSegment(p, s string) (bool, error) {
	if strings.HasPrefix(s, ".") && !strings.HasPrefix(p, ".") {
		return false, nil
	}
	return matchChars(p, s)
}

func matchChars(p, s string) (bool, error) {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			for len(p) > 0 && p[0] == '*' {
				p = p[1:]
			}
			for k := 0; k <= len(s); k++ {
				ok, err := matchChars(p, s[k:])
				if err != nil || ok {
					return ok, err
				}
			}
			return false, nil
		case '?':
			if len(s) == 0 {
				return false, nil
			}
			_, n := firstRune(s)
			p, s = p[1:], s[n:]
		case '[':
			end, negate, set, err := parseClass(p)
			if err != nil {
				return false, err
			}
			if len(s) == 0 {
				return false, nil
			}
			r, n := firstRune(s)
			if set(r) == negate {
				return false, nil
			}
			p, s = p[end:], s[n:]
		default:
			if len(s) == 0 || p[0] != s[0] {
				return false, nil
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0, nil
}

func firstRune(s string) (rune, int) { return utf8.DecodeRuneInString(s) }

// parseClass parses "[…]" at the start of p. It returns the length of the
// class, whether it is negated, and its membership test.
func parseClass(p string) (int, bool, func(rune) bool, error) {
	i := 1
	negate := false
	if i < len(p) && (p[i] == '!' || p[i] == '^') {
		negate = true
		i++
	}
	type rng struct{ lo, hi rune }
	var ranges []rng
	first := true
	for {
		if i >= len(p) {
			return 0, false, nil, fmt.Errorf("%w: unclosed class in %q", ErrUnsupported, p)
		}
		if p[i] == ']' && !first {
			i++
			break
		}
		if p[i] == '[' && i+1 < len(p) && p[i+1] == ':' {
			return 0, false, nil, fmt.Errorf("%w: POSIX class in %q", ErrUnsupported, p)
		}
		first = false
		lo, n := firstRune(p[i:])
		i += n
		hi := lo
		if i+1 < len(p) && p[i] == '-' && p[i+1] != ']' {
			hi, n = firstRune(p[i+1:])
			i += 1 + n
		}
		ranges = append(ranges, rng{lo, hi})
	}
	return i, negate, func(r rune) bool {
		for _, x := range ranges {
			if r >= x.lo && r <= x.hi {
				return true
			}
		}
		return false
	}, nil
}

// NameMatch matches a package name pattern, where "*" matches any run of
// characters (including "/", as in "@scope/*").
func NameMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	name = name[len(parts[0]):]
	for i := 1; i < len(parts)-1; i++ {
		j := strings.Index(name, parts[i])
		if j < 0 {
			return false
		}
		name = name[j+len(parts[i]):]
	}
	return strings.HasSuffix(name, parts[len(parts)-1])
}

// Rule is one compiled-set rule of a converted pattern.
type Rule struct {
	Kind match.Kind
	// Value is the path (dir, file) or the pattern (glob).
	Value string
}

// ToRules converts a tool glob, relative to dir (repository-relative), into
// compiled-set rules that match at least what the tool matches (they may match
// more: classes and escapes become "?", a "**" inside a segment becomes "*", and
// a plain path becomes a dir rule, which also matches a file). A pattern that
// escapes the repository or uses unsupported syntax returns an error.
func ToRules(dir, pattern string) ([]Rule, error) {
	if strings.IndexByte(pattern, 0) >= 0 {
		return nil, fmt.Errorf("NUL byte in pattern")
	}
	for _, x := range []string{"@(", "!(", "+(", "?(", "*("} {
		if strings.Contains(pattern, x) {
			return nil, fmt.Errorf("%w: extglob in %q", ErrUnsupported, pattern)
		}
	}
	alts, err := Expand(pattern)
	if err != nil {
		return nil, err
	}
	var out []Rule
	for _, a := range alts {
		a = strings.TrimSuffix(a, "/")
		if strings.HasPrefix(a, "/") {
			return nil, fmt.Errorf("absolute pattern %q", a)
		}
		joined, ok := pathx.Join(dir, a)
		if !ok {
			return nil, fmt.Errorf("pattern %q escapes the repository", a)
		}
		if !strings.ContainsAny(joined, "*?[]\\") {
			out = append(out, Rule{Kind: match.Dir, Value: joined})
			continue
		}
		out = append(out, Rule{Kind: match.Glob, Value: widenGlob(joined)})
	}
	return out, nil
}

// widenGlob rewrites a cleaned glob into the compiled-set language.
func widenGlob(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if seg == "**" {
			continue
		}
		var b strings.Builder
		for j := 0; j < len(seg); j++ {
			c := seg[j]
			switch c {
			case '\\':
				b.WriteByte('?')
				if j+1 < len(seg) {
					j++
				}
			case '[':
				end := strings.IndexByte(seg[j+1:], ']')
				if end < 0 {
					b.WriteByte('?')
					continue
				}
				if end == 0 {
					// "[]…]": the first "]" is a member.
					if e2 := strings.IndexByte(seg[j+2:], ']'); e2 >= 0 {
						end = e2 + 1
					}
				}
				b.WriteByte('?')
				j += end + 1
			case ']':
				b.WriteByte('?')
			case '*':
				for j+1 < len(seg) && seg[j+1] == '*' {
					j++
				}
				b.WriteByte('*')
			default:
				b.WriteByte(c)
			}
		}
		segs[i] = b.String()
	}
	return strings.Join(segs, "/")
}
