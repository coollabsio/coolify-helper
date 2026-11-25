package match

import (
	"fmt"
	"regexp"
	"strings"
)

// compilePCRE translates a PHP preg pattern produced by globToRegex into a Go
// regexp with the same matches, as preg_match($pattern, $subject) === 1 gives
// them with PHP 8.4 and PCRE2 (no modifiers: byte mode, "." excludes "\n",
// "$" also matches before a final "\n").
//
// The translation does not hand the PCRE text to RE2. It parses the small
// subset of PCRE that globToRegex can produce and emits every literal byte as
// \x{HH}, so no character means something different in the two dialects. The
// regexp matches latin1(subject): one rune per byte, which gives byte semantics.
//
// It returns ok=false when PHP fails to compile the pattern (preg_match then
// returns false: the pattern never matches), and uncertain=true when the
// pattern uses a construct this subset does not model (the caller widens).
//
// PCRE constructs and how they map:
//
//	"#" delimiters    The pattern ends at the first unescaped "#". globToRegex
//	                  never escapes "#", so a "#" in the glob ends it early and
//	                  the rest ("…$#") is read as modifiers: "$" is not a
//	                  modifier, so PHP fails ("Unknown modifier") → ok=false.
//	^                 start of subject → ^ (Go: start of text, no (?m)).
//	$ (last)          end, or before a final "\n" (no D modifier) → \n?\z.
//	.                 any byte but "\n" (no s modifier) → [^\n].
//	*                 greedy repeat of the previous atom → *.
//	\X                X not alphanumeric: the literal X.
//	]                 outside a class: a literal "]".
//	[…] / [^…]        a byte class; "]" right after "[" or "[^" is literal;
//	                  "a-b" is a byte range (b < a fails to compile); "-" first
//	                  or before "]" is literal; "\X" is X; a "[" is literal.
//	                  A negated class matches "\n" in both dialects. An
//	                  unterminated class fails to compile.
//	[: [. [=          POSIX bracket syntax (PCRE2 check_posix_syntax): not
//	                  modeled → uncertain.
//	anything else     ( ) | + ? { and alphanumeric escapes cannot come out of
//	                  globToRegex; they are reported as uncertain to be safe.
func compilePCRE(pcre string) (re *regexp.Regexp, ok, uncertain bool) {
	body, ok := pcreBody(pcre)
	if !ok {
		return nil, false, false
	}
	expr, ok, uncertain := translatePCRE(body)
	if !ok || uncertain {
		return nil, ok, uncertain
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		// translatePCRE only emits valid syntax; never narrow on a bug.
		return nil, false, true
	}
	return re, true, false
}

// pcreBody returns the pattern between the delimiters, as PHP's
// pcre_get_compiled_regex_cache finds them, and false when PHP rejects it.
func pcreBody(pcre string) (string, bool) {
	if len(pcre) < 2 || pcre[0] != '#' {
		return "", false
	}
	for i := 1; i < len(pcre); i++ {
		switch pcre[i] {
		case '\\':
			if i+1 < len(pcre) {
				i++
			}
		case '#':
			// Whatever follows the end delimiter is modifiers. globToRegex
			// always ends with "$#", and "$" is not a modifier, so anything
			// after this "#" makes PHP fail.
			if i != len(pcre)-1 {
				return "", false
			}
			return pcre[1:i], true
		}
	}
	return "", false // no ending delimiter
}

// translatePCRE translates a pattern body. See compilePCRE.
func translatePCRE(body string) (expr string, ok, uncertain bool) {
	var b strings.Builder
	atom := false // whether the previous item can take a quantifier
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch c {
		case '^':
			b.WriteByte('^')
			atom = false
		case '$':
			if i != len(body)-1 {
				return "", true, true // a "$" before the end cannot come out of globToRegex
			}
			b.WriteString(`\x{0A}?\z`)
			atom = false
		case '.':
			b.WriteString(`[^\x{0A}]`)
			atom = true
		case '*':
			if !atom {
				return "", true, true
			}
			b.WriteByte('*')
			atom = false
		case '\\':
			if i+1 >= len(body) || isAlnum(body[i+1]) {
				return "", true, true
			}
			i++
			writeByte(&b, body[i])
			atom = true
		case '[':
			if posixAhead(body, i) {
				return "", true, true
			}
			end, class, ok, unc := translateClass(body, i)
			if unc {
				return "", true, true
			}
			if !ok {
				return "", false, false
			}
			b.WriteString(class)
			i = end
			atom = true
		case '(', ')', '|', '+', '?', '{':
			return "", true, true
		default:
			writeByte(&b, c)
			atom = true
		}
	}
	return b.String(), true, false
}

// translateClass translates the class that starts at body[start] == '['. It
// returns the index of its closing "]".
func translateClass(body string, start int) (end int, class string, ok, uncertain bool) {
	var b strings.Builder
	b.WriteByte('[')
	i := start + 1
	if i < len(body) && body[i] == '^' {
		b.WriteByte('^')
		i++
	}
	first := true
	for i < len(body) {
		if body[i] == ']' && !first {
			b.WriteByte(']')
			return i, b.String(), true, false
		}
		first = false
		lo, next, unc := classChar(body, i)
		if unc {
			return 0, "", false, true
		}
		i = next
		// A range: "-" followed by anything but "]" (or the end).
		if i+1 < len(body) && body[i] == '-' && body[i+1] != ']' {
			hi, after, unc := classChar(body, i+1)
			if unc {
				return 0, "", false, true
			}
			if hi < lo {
				return 0, "", false, false // range out of order: compile error
			}
			fmt.Fprintf(&b, `\x{%02X}-\x{%02X}`, lo, hi)
			i = after
			continue
		}
		writeByte(&b, lo)
	}
	return 0, "", false, false // missing terminating "]"
}

// classChar reads one literal class character at body[i]: a byte, or an
// escaped non-alphanumeric byte.
func classChar(body string, i int) (c byte, next int, uncertain bool) {
	switch body[i] {
	case '\\':
		if i+1 >= len(body) || isAlnum(body[i+1]) {
			return 0, 0, true
		}
		return body[i+1], i + 2, false
	case '[':
		if posixAhead(body, i) {
			return 0, 0, true
		}
	}
	return body[i], i + 1, false
}

// posixAhead reports whether body[i] == '[' starts something PCRE2 may read as
// POSIX bracket syntax ("[:", "[." or "[="). That is not modeled.
func posixAhead(body string, i int) bool {
	if i+1 >= len(body) {
		return false
	}
	switch body[i+1] {
	case ':', '.', '=':
		return true
	}
	return false
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func writeByte(b *strings.Builder, c byte) {
	fmt.Fprintf(b, `\x{%02X}`, c)
}
