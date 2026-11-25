// Package dockerignore compiles a .dockerignore file into ordered rules of the
// compiled set (dir, file and glob rules, the last match wins) that reproduce
// BuildKit's filtering of the build context exactly.
//
// BuildKit reads the file with ignorefile.ReadAll and matches every path of the
// context with patternmatcher: a path is excluded when the last pattern that
// matches the path, or one of its parent directories, is not a "!" exception
// (PatternMatcher.MatchesOrParentMatches). Patterns are relative to the context
// root, "*" does not cross "/", and a pattern without a slash only matches at
// the root (unlike .gitignore).
//
// A pattern that cannot be compiled exactly makes Compile fail as a whole: the
// caller then drops every rule of the file, because dropping only some of them
// could narrow the set ("!" exceptions re-include what earlier patterns exclude).
package dockerignore

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"

	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
)

// Caps on the ignore file. Past them Compile fails, which widens.
const (
	MaxBytes    = 1 << 20
	MaxPatterns = 10000
)

// Compile compiles the content of a .dockerignore file for the build context
// contextDir (repository-relative, "" for the root) into ordered rules:
// exclude rules for patterns and include rules for "!" exceptions. The rules
// have no Origin; the caller sets it. It reports ok=false, with a reason, when
// a pattern cannot be compiled exactly; the caller must then drop every rule.
func Compile(contextDir string, content []byte) (rules []match.Rule, ok bool, reason string) {
	if len(content) > MaxBytes {
		return nil, false, fmt.Sprintf("the file is larger than %d bytes", MaxBytes)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, false, "the file has a NUL byte"
	}
	lines, err := ignorefile.ReadAll(bytes.NewReader(content))
	if err != nil {
		return nil, false, "cannot read the file: " + err.Error()
	}
	if len(lines) > MaxPatterns {
		return nil, false, fmt.Sprintf("the file has more than %d patterns", MaxPatterns)
	}
	// patternmatcher.New is what BuildKit compiles the patterns with: a pattern
	// it rejects fails the build, and its Patterns are the exact cleaned
	// patterns that are matched.
	pm, err := patternmatcher.New(lines)
	if err != nil {
		return nil, false, "invalid pattern: " + err.Error()
	}
	for _, p := range pm.Patterns() {
		action := match.Exclude
		if p.Exclusion() {
			action = match.Include
		}
		r, use, why := compilePattern(contextDir, p.String(), action)
		if why != "" {
			return nil, false, why
		}
		if use {
			rules = append(rules, r)
		}
	}
	return rules, true, ""
}

// compilePattern compiles one cleaned pattern (without its "!"). use is false
// for a pattern that can never match a path of the context; why is set when
// the pattern cannot be compiled exactly.
func compilePattern(contextDir, p string, action match.Action) (r match.Rule, use bool, why string) {
	quoted := fmt.Sprintf("pattern %q", p)
	if !utf8.ValidString(p) || strings.ContainsRune(p, utf8.RuneError) || strings.ContainsRune(p, '\uFEFF') {
		// patternmatcher scans patterns rune by rune with text/scanner, which
		// rewrites invalid UTF-8 and byte order marks.
		return r, false, quoted + " is not valid UTF-8"
	}
	// patternmatcher matches the cleaned pattern against relative paths, which
	// never are ".", start with "/" or have a ".." segment: such patterns match
	// nothing. In particular "." and "/" do not exclude the context.
	if p == "." || p == ".." || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") {
		return r, false, ""
	}
	if strings.ContainsAny(p, `[]\`) {
		return r, false, quoted + ": character classes and escapes are not supported"
	}
	wild := strings.ContainsAny(p, "*?")
	if !wild {
		// An exact pattern: the path or one of its parents equals it.
		full, okJoin := pathx.Join(contextDir, p)
		if !okJoin || full == "" {
			return r, false, quoted + " cannot be placed in the context"
		}
		return match.Rule{Action: action, Kind: match.Dir, Path: full}, true, ""
	}
	if strings.ContainsAny(p, "^{|}") {
		// patternmatcher leaves these unescaped in its regular expression: its
		// escape bitmap overflows for "{", "|" and "}", and "^" is not in it.
		return r, false, quoted + `: "^", "{", "|" and "}" are not supported in a wildcard pattern`
	}
	if strings.ContainsAny(contextDir, "*?") {
		return r, false, fmt.Sprintf("the context directory %q has a wildcard character", contextDir)
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return r, false, quoted + " is not clean"
		}
		if s != "**" && strings.Contains(s, "**") {
			// patternmatcher's "**" inside a segment also crosses "/".
			return r, false, quoted + ": \"**\" must be a whole path segment"
		}
	}
	// The pattern matches the path or one of its parents: "P/**" (our "**"
	// matches zero segments). A trailing "**" in patternmatcher requires at
	// least one more character after the "/" before it, so "X/**" matches the
	// paths strictly under X: "X/*/**". A lone "**" matches every path.
	var glob string
	if segs[len(segs)-1] == "**" {
		glob = strings.Join(append(segs[:len(segs)-1:len(segs)-1], "*", "**"), "/")
	} else {
		glob = p + "/**"
	}
	if contextDir != "" {
		glob = contextDir + "/" + glob
	}
	if err := match.ValidateGlob(glob); err != nil {
		return r, false, quoted + ": " + err.Error()
	}
	return match.Rule{Action: action, Kind: match.Glob, Pattern: glob}, true, ""
}
