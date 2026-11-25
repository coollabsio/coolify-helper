package match

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ValidateGlob reports whether pattern is a valid glob: not empty, no leading
// or trailing "/", no empty, "." or ".." segment and no NUL byte.
func ValidateGlob(pattern string) error {
	if pattern == "" {
		return errors.New("empty glob")
	}
	if strings.IndexByte(pattern, 0) >= 0 {
		return fmt.Errorf("glob %q has a NUL byte", pattern)
	}
	if strings.HasPrefix(pattern, "/") || strings.HasSuffix(pattern, "/") {
		return fmt.Errorf("glob %q has a leading or trailing slash", pattern)
	}
	for _, seg := range strings.Split(pattern, "/") {
		switch seg {
		case "":
			return fmt.Errorf("glob %q has an empty segment", pattern)
		case ".", "..":
			return fmt.Errorf("glob %q has a %q segment", pattern, seg)
		}
	}
	return nil
}

// GlobMatch reports whether path matches pattern, the pattern of a rule of
// kind "glob". An invalid pattern matches nothing. It compiles the pattern on
// every call: use Compile to match many paths. The syntax:
//
//   - "*" matches any run of bytes except "/", including the empty run.
//   - "**" as a whole segment matches zero or more whole segments, so "a/**"
//     matches "a", "a/b" and "a/b/c", and "**/x" matches "x" and "d/x". A "**"
//     that is not a whole segment is just two "*".
//   - "?" matches one character except "/": one UTF-8 encoded character, or a
//     single byte where the path is not valid UTF-8 (the width of
//     utf8.DecodeRuneInString).
//   - Every other byte matches itself. There are no character classes, braces
//     or escapes: "[", "]", "{", "}" and "\" are literal.
//
// Matching is byte-exact and case-sensitive. Paths are repository-relative,
// without a leading "/".
func GlobMatch(pattern, path string) bool {
	g, err := compileGlob(pattern)
	if err != nil {
		return false
	}
	return g.match(strings.Split(path, "/"))
}

// glob is a compiled glob: one matcher per segment.
type glob struct {
	segs []globSeg
	// fixed is true when there is no "**": a path matches only with as many segments.
	fixed bool
}

// segKind is how a glob segment matches one path segment.
type segKind uint8

const (
	segLiteral    segKind = iota // no "*" or "?": the segment must be equal
	segStars                     // "*" but no "?": greedy wildcard matching
	segQuestion                  // "?" (and maybe "*"): dynamic programming
	segDoubleStar                // "**": zero or more segments
)

type globSeg struct {
	kind segKind
	text string
	// prefix and suffix are the literal text before the first wildcard and
	// after the last one: cheap first tests.
	prefix, suffix string
}

func compileGlob(pattern string) (*glob, error) {
	if err := ValidateGlob(pattern); err != nil {
		return nil, err
	}
	parts := strings.Split(pattern, "/")
	g := &glob{segs: make([]globSeg, 0, len(parts))}
	for _, p := range parts {
		s := globSeg{text: p}
		switch {
		case p == "**":
			s.kind = segDoubleStar
			// "**/**" is "**": collapsing keeps the matcher linear in practice.
			if n := len(g.segs); n > 0 && g.segs[n-1].kind == segDoubleStar {
				continue
			}
		case strings.IndexByte(p, '?') >= 0:
			s.kind = segQuestion
		case strings.IndexByte(p, '*') >= 0:
			s.kind = segStars
		default:
			s.kind = segLiteral
		}
		if i := strings.IndexAny(p, "*?"); i >= 0 {
			s.prefix = p[:i]
			s.suffix = p[strings.LastIndexAny(p, "*?")+1:]
		}
		g.segs = append(g.segs, s)
	}
	g.fixed = true
	for _, s := range g.segs {
		if s.kind == segDoubleStar {
			g.fixed = false
		}
	}
	return g, nil
}

// match reports whether the path segments match. A "**" matches any number of
// whole segments and every other glob segment matches exactly one, so the
// classic wildcard algorithm applies: on a mismatch, retry from the last "**"
// with one more segment consumed. It is exact and O(len(g.segs)*len(segs)).
func (g *glob) match(segs []string) bool {
	if g.fixed && len(segs) != len(g.segs) {
		return false
	}
	// Cheap rejection first: a last glob segment that is not "**" must match
	// the last path segment.
	if last := &g.segs[len(g.segs)-1]; last.kind != segDoubleStar && !last.matchSegment(segs[len(segs)-1]) {
		return false
	}
	pi, si := 0, 0
	starPi, starSi := -1, 0
	for si < len(segs) {
		if pi < len(g.segs) {
			s := &g.segs[pi]
			if s.kind == segDoubleStar {
				starPi, starSi = pi, si
				pi++
				continue
			}
			if s.matchSegment(segs[si]) {
				pi++
				si++
				continue
			}
		}
		if starPi < 0 {
			return false
		}
		starSi++
		pi, si = starPi+1, starSi
	}
	for pi < len(g.segs) && g.segs[pi].kind == segDoubleStar {
		pi++
	}
	return pi == len(g.segs)
}

func (s *globSeg) matchSegment(name string) bool {
	switch s.kind {
	case segLiteral:
		return name == s.text
	case segStars:
		return len(name) >= len(s.prefix)+len(s.suffix) &&
			strings.HasPrefix(name, s.prefix) && strings.HasSuffix(name, s.suffix) &&
			matchStars(s.text[len(s.prefix):len(s.text)-len(s.suffix)], name[len(s.prefix):len(name)-len(s.suffix)])
	case segQuestion:
		return strings.HasPrefix(name, s.prefix) && matchQuestion(s.text[len(s.prefix):], name[len(s.prefix):])
	}
	return false
}

// matchStars matches a segment pattern made of literal bytes and "*". Every
// token but "*" is one byte wide, so retrying from the last "*" is exact.
func matchStars(p, s string) bool {
	pi, si := 0, 0
	starPi, starSi := -1, 0
	for si < len(s) {
		if pi < len(p) {
			if p[pi] == '*' {
				starPi, starSi = pi, si
				pi++
				continue
			}
			if p[pi] == s[si] {
				pi++
				si++
				continue
			}
		}
		if starPi < 0 {
			return false
		}
		starSi++
		pi, si = starPi+1, starSi
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// matchQuestion matches a segment pattern with "?", whose width depends on the
// bytes at its position. That breaks the greedy algorithm, so this is a
// dynamic program: next[j] is whether p[i:] matches s[j:], computed for i from
// the end of the pattern to its start.
func matchQuestion(p, s string) bool {
	n := len(s)
	next := make([]bool, 2*(n+1))
	cur, nxt := next[:n+1], next[n+1:]
	nxt[n] = true // the empty pattern matches the empty rest
	for i := len(p) - 1; i >= 0; i-- {
		switch p[i] {
		case '*':
			cur[n] = nxt[n]
			for j := n - 1; j >= 0; j-- {
				cur[j] = nxt[j] || cur[j+1]
			}
		case '?':
			cur[n] = false
			for j := n - 1; j >= 0; j-- {
				_, w := utf8.DecodeRuneInString(s[j:])
				cur[j] = nxt[j+w]
			}
		default:
			cur[n] = false
			for j := n - 1; j >= 0; j-- {
				cur[j] = s[j] == p[i] && nxt[j+1]
			}
		}
		cur, nxt = nxt, cur
	}
	return nxt[0]
}
