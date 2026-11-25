package match

import (
	"regexp"
	"strings"
)

// This file ports Coolify's legacy watch paths (app/Models/Application.php:
// parseWatchPaths, isWatchPathsTriggered, matchPaths, globMatch, globToRegex)
// exactly, quirks included. testdata/php/watch_paths.php holds a verbatim copy
// of the PHP, and the tests check this port against it.
//
// How Coolify decides today, for a push with changed files F:
//
//	if blank($application->watch_paths) → deploy
//	else deploy if isWatchPathsTriggered(F):
//	    $lines = explode("\n", effectiveWatchPaths($stored) ?? '')  // a null becomes [""]
//	    matchPaths(F, $lines)->count() > 0
//
// Quirks this port keeps:
//   - parseWatchPaths runs again on the stored (already normalized) value, and
//     once more in the set mutator when that changes it. It is not idempotent:
//     a stored "/ / /x" is matched as "/x" (and saved so; the next push then
//     matches "x"). This port models one call on the given stored value.
//   - A value that normalizes to "0" (such as "/0") is not blank but parses to
//     null, so it never matches. A line "0" is skipped by empty() but still
//     counts as an inclusion pattern.
//   - The regex runs in PCRE byte mode: "?" and "[…]" match one byte, not one
//     UTF-8 character, and "$" also matches before a final "\n".
//   - A "#" in the regex ends the PHP pattern delimiter: preg_match fails and
//     the pattern never matches (it still counts as an inclusion pattern). A
//     regex that fails to compile (unterminated or reversed class) never matches
//     either. (Under Laravel the warning becomes an ErrorException, which the
//     webhook controller catches: the push fails for the remaining
//     applications. In the PHP reference and here it is "no match".)
//
// One known divergence: PCRE gives up after pcre.backtrack_limit (1,000,000)
// steps and preg_match then returns false, while Go's regexp always finds the
// answer. That takes a pattern with many "*"/"**" and a long path (such as
// "**a**a**a**a**a**c" and 400 "a"s then "cb"), and in every such case we
// found, the path does not match anyway, so both say "no match". Only a path
// that does match but still exhausts the limit would differ.

// phpTrimSet is the set of bytes PHP's trim() strips by default.
const phpTrimSet = " \t\n\r\x00\x0B"

// WatchPaths is a compiled legacy watch paths value.
type WatchPaths struct {
	blank        bool
	lines        []watchLine
	hasInclusion bool
	widened      bool
}

type watchLine struct {
	// skip is true for a line that matchPaths skips: empty() after trim().
	skip    bool
	exclude bool
	// re is nil when preg_match fails for the pattern: it never matches.
	re *regexp.Regexp
}

// CompileWatchPaths compiles the stored watch_paths value of an application.
func CompileWatchPaths(value string) *WatchPaths {
	w := compileWatchLines(strings.Split(effectiveWatchPaths(value), "\n"))
	w.blank = strings.Trim(value, phpTrimSet) == ""
	return w
}

// compileWatchLines compiles the patterns matchPaths receives.
func compileWatchLines(lines []string) *WatchPaths {
	w := &WatchPaths{}
	for _, line := range lines {
		l := compileWatchLine(line)
		w.widened = w.widened || l.uncertain
		w.lines = append(w.lines, l.watchLine)
		// $watch_paths->contains(fn ($p) => ! str_starts_with(trim($p), '!'))
		if !strings.HasPrefix(strings.Trim(line, phpTrimSet), "!") {
			w.hasInclusion = true
		}
	}
	return w
}

// Blank reports whether the value is blank (Laravel's blank(): empty after
// trim()). Coolify deploys on every push then, without matching: the caller
// decides that, Match does not.
func (w *WatchPaths) Blank() bool { return w.blank }

// Widened reports whether a pattern uses a regex construct that this port
// does not model exactly (a POSIX bracket expression such as "[[:digit:]]").
// Match then includes every path, so that a push deploys rather than skips.
func (w *WatchPaths) Widened() bool { return w.widened }

// Match reports whether a changed file is included: the last matching pattern
// decides, and when none matches, the file is included only if there are no
// inclusion patterns at all.
func (w *WatchPaths) Match(file string) bool {
	if w.widened {
		return true
	}
	subject := latin1(file)
	matched, include := false, false
	for _, l := range w.lines {
		if l.skip || l.re == nil {
			continue
		}
		if l.re.MatchString(subject) {
			matched, include = true, !l.exclude
		}
	}
	if !matched {
		return !w.hasInclusion
	}
	return include
}

type compiledWatchLine struct {
	watchLine
	uncertain bool
}

func compileWatchLine(line string) compiledWatchLine {
	pattern := strings.Trim(line, phpTrimSet)
	if phpEmpty(pattern) {
		return compiledWatchLine{watchLine: watchLine{skip: true}}
	}
	l := compiledWatchLine{}
	if strings.HasPrefix(pattern, "!") {
		l.exclude = true
		pattern = pattern[1:]
	}
	re, ok, uncertain := compilePCRE(globToRegex(pattern))
	if ok {
		l.re = re
	}
	l.uncertain = uncertain
	return l
}

// phpEmpty is PHP's empty() for a string.
func phpEmpty(s string) bool { return s == "" || s == "0" }

// effectiveWatchPaths returns the value isWatchPathsTriggered splits into
// patterns. normalizeWatchPaths() parses the stored value, and when the result
// differs it assigns it through the watchPaths() set mutator, which parses it
// once more (or stores null for a falsy value). A null is split as "".
func effectiveWatchPaths(stored string) string {
	normalized, ok := parseWatchPaths(stored)
	if ok && normalized == stored {
		return stored
	}
	if !ok || phpEmpty(normalized) {
		return ""
	}
	again, _ := parseWatchPaths(normalized)
	return again
}

// parseWatchPaths ports Application::parseWatchPaths. ok is false where the
// PHP returns null (a falsy value: "" or "0").
func parseWatchPaths(value string) (normalized string, ok bool) {
	if phpEmpty(value) {
		return "", false
	}
	var kept []string
	for _, path := range strings.Split(value, "\n") {
		path = strings.Trim(path, phpTrimSet)
		if strings.HasPrefix(path, "!") {
			path = "!" + strings.TrimLeft(strings.Trim(path[1:], phpTrimSet), "/")
		} else {
			path = strings.TrimLeft(path, "/")
		}
		if path != "" {
			kept = append(kept, path)
		}
	}
	return strings.Trim(strings.Join(kept, "\n"), phpTrimSet), true
}

// matchPatterns ports Application::matchPaths for one file and a list of raw
// patterns (not normalized), as Coolify's unit tests call it.
func matchPatterns(patterns []string, file string) bool {
	if len(patterns) == 0 {
		return false
	}
	return compileWatchLines(patterns).Match(file)
}

// globToRegex ports Application::globToRegex verbatim: it returns the PCRE
// pattern, delimiters included.
func globToRegex(pattern string) string {
	var b strings.Builder
	inGroup := false
	n := len(pattern)
	for i := 0; i < n; i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < n && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
				if i+1 < n && pattern[i+1] == '/' {
					i++
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			inGroup = true
			b.WriteByte('[')
			if i+1 < n && (pattern[i+1] == '!' || pattern[i+1] == '^') {
				b.WriteByte('^')
				i++
			}
		case ']':
			if inGroup {
				inGroup = false
				b.WriteByte(']')
			} else {
				b.WriteString(`\]`) // preg_quote(']', '#')
			}
		case '.', '(', ')', '+', '{', '}', '$', '^', '|', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return "#^" + b.String() + "$#"
}

// latin1 maps every byte of s to the rune with the same value, so that a Go
// regexp built by compilePCRE matches bytes as PCRE does without the "u" flag.
func latin1(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	r := make([]rune, len(s))
	for i := 0; i < len(s); i++ {
		r[i] = rune(s[i])
	}
	return string(r)
}
