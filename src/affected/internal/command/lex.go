package command

import (
	"strings"
)

// tokKind is the kind of a lexer token.
type tokKind int

const (
	// tWord is a word, with its quotes removed.
	tWord tokKind = iota
	// tOp is a control operator: "&&", "||", ";", "|", "&", "\n", "(" or ")".
	tOp
	// tRedir is a redirection operator. The word after it is its target, not an
	// argument. Its subs hold the body of a here-document.
	tRedir
)

// token is one lexer token.
type token struct {
	kind tokKind
	text string
	// subs are nested scripts that run as commands of their own: command
	// substitutions, process substitutions and here-document bodies.
	subs [][]token
}

// maxDepth caps the nesting of substitutions. Deeper nesting is replaced by an
// Opaque command.
const maxDepth = 32

// errUnterminated is returned when the input ends inside a quote or a
// substitution that starts at pos.
type errUnterminated struct{ pos int }

func (e *errUnterminated) Error() string { return "unterminated quote" }

// lexer tokenizes a shell script.
type lexer struct {
	s string
	i int
	// literal marks quote characters (by position) that turned out to be
	// unterminated: they are read as plain characters on the next attempt, so
	// that one stray quote does not swallow the rest of the script.
	literal map[int]bool
	depth   int
	// heredocs wait for the end of the current line to read their bodies.
	heredocs []pendingHeredoc
}

type pendingHeredoc struct {
	toks      *[]token
	index     int
	delim     string
	stripTabs bool
}

// maxRetries caps how many unterminated quotes lexScript retries as literal
// characters before it gives up and returns an opaque command.
const maxRetries = 64

// lexScript tokenizes a whole script. An unterminated quote is retried as a
// literal character. A script it can't tokenize becomes an Opaque command.
func lexScript(s string, depth int) []token {
	literal := map[int]bool{}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		l := &lexer{s: s, literal: literal, depth: depth}
		toks, err := l.lexList(false, -1)
		if err == nil {
			return toks
		}
		ue, ok := err.(*errUnterminated)
		if !ok || literal[ue.pos] {
			break
		}
		literal[ue.pos] = true
	}
	return []token{{kind: tWord, text: Opaque}}
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }

func isMeta(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ';', '&', '|', '(', ')', '<', '>':
		return true
	}
	return false
}

// lexList reads tokens until the end of the input or, when stopAtParen is set,
// until the ")" that closes a "$(" or "<(" opened at start.
func (l *lexer) lexList(stopAtParen bool, start int) ([]token, error) {
	var toks []token
	parens := 0
	for {
		// Blanks and line continuations between words.
		for l.i < len(l.s) {
			c := l.s[l.i]
			if isBlank(c) {
				l.i++
				continue
			}
			if c == '\\' && l.i+1 < len(l.s) && l.s[l.i+1] == '\n' {
				l.i += 2
				continue
			}
			break
		}
		if l.i >= len(l.s) {
			if stopAtParen {
				return nil, &errUnterminated{pos: start}
			}
			return toks, nil
		}
		c := l.s[l.i]
		switch {
		case c == '\n':
			l.i++
			toks = append(toks, token{kind: tOp, text: "\n"})
			l.readHeredocs()
		case c == '#':
			for l.i < len(l.s) && l.s[l.i] != '\n' {
				l.i++
			}
		case c == ';':
			l.i++
			for l.i < len(l.s) && (l.s[l.i] == ';' || l.s[l.i] == '&') {
				l.i++
			}
			toks = append(toks, token{kind: tOp, text: ";"})
		case c == '&':
			switch {
			case strings.HasPrefix(l.s[l.i:], "&&"):
				l.i += 2
				toks = append(toks, token{kind: tOp, text: "&&"})
			case strings.HasPrefix(l.s[l.i:], "&>>"):
				l.i += 3
				toks = append(toks, token{kind: tRedir, text: "&>>"})
			case strings.HasPrefix(l.s[l.i:], "&>"):
				l.i += 2
				toks = append(toks, token{kind: tRedir, text: "&>"})
			default:
				l.i++
				toks = append(toks, token{kind: tOp, text: "&"})
			}
		case c == '|':
			switch {
			case strings.HasPrefix(l.s[l.i:], "||"):
				l.i += 2
				toks = append(toks, token{kind: tOp, text: "||"})
			case strings.HasPrefix(l.s[l.i:], "|&"):
				l.i += 2
				toks = append(toks, token{kind: tOp, text: "|"})
			default:
				l.i++
				toks = append(toks, token{kind: tOp, text: "|"})
			}
		case c == '(':
			l.i++
			parens++
			toks = append(toks, token{kind: tOp, text: "("})
		case c == ')':
			l.i++
			if stopAtParen && parens == 0 {
				return toks, nil
			}
			if parens > 0 {
				parens--
			}
			toks = append(toks, token{kind: tOp, text: ")"})
		case c == '<' || c == '>':
			tok, err := l.lexRedirOrProcSubst(&toks)
			if err != nil {
				return nil, err
			}
			if tok != nil {
				toks = append(toks, *tok)
			}
		default:
			tok, err := l.lexWord()
			if err != nil {
				return nil, err
			}
			if tok == nil {
				// The word was a file descriptor number of a redirection.
				continue
			}
			toks = append(toks, *tok)
		}
	}
}

// lexRedirOrProcSubst reads a redirection operator, or a process substitution
// "<(…)"/">(…)", which becomes a word with nested commands.
func (l *lexer) lexRedirOrProcSubst(toks *[]token) (*token, error) {
	start := l.i
	c := l.s[l.i]
	if l.i+1 < len(l.s) && l.s[l.i+1] == '(' {
		l.i += 2
		sub, err := l.nested(start)
		if err != nil {
			return nil, err
		}
		return &token{kind: tWord, text: string(c) + "(…)", subs: [][]token{sub}}, nil
	}
	if c == '<' && strings.HasPrefix(l.s[l.i:], "<<<") {
		l.i += 3
		return &token{kind: tRedir, text: "<<<"}, nil
	}
	if c == '<' && strings.HasPrefix(l.s[l.i:], "<<") {
		l.i += 2
		strip := false
		if l.i < len(l.s) && l.s[l.i] == '-' {
			strip = true
			l.i++
		}
		for l.i < len(l.s) && isBlank(l.s[l.i]) {
			l.i++
		}
		// The delimiter word.
		w, err := l.lexWord()
		if err != nil {
			return nil, err
		}
		*toks = append(*toks, token{kind: tRedir, text: "<<"})
		if w != nil && w.text != "" {
			l.heredocs = append(l.heredocs, pendingHeredoc{toks: toks, index: len(*toks) - 1, delim: w.text, stripTabs: strip})
		}
		return nil, nil
	}
	op := string(c)
	l.i++
	for l.i < len(l.s) && (l.s[l.i] == '>' || l.s[l.i] == '&' || l.s[l.i] == '|' || (c == '<' && l.s[l.i] == '>')) {
		op += string(l.s[l.i])
		l.i++
		if len(op) >= 3 {
			break
		}
	}
	return &token{kind: tRedir, text: op}, nil
}

// readHeredocs reads the bodies of the here-documents of the line that just
// ended. A body is lexed as a script of its own: "bash <<EOF" runs it, and
// treating other bodies as commands only means more commands to look at.
func (l *lexer) readHeredocs() {
	pending := l.heredocs
	l.heredocs = nil
	for _, h := range pending {
		var body strings.Builder
		for l.i < len(l.s) {
			end := strings.IndexByte(l.s[l.i:], '\n')
			var line string
			if end < 0 {
				line = l.s[l.i:]
				l.i = len(l.s)
			} else {
				line = l.s[l.i : l.i+end]
				l.i += end + 1
			}
			cmp := strings.TrimRight(line, "\r")
			if h.stripTabs {
				cmp = strings.TrimLeft(cmp, "\t")
			}
			if cmp == h.delim {
				break
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		if h.index < len(*h.toks) {
			(*h.toks)[h.index].subs = append((*h.toks)[h.index].subs, l.sublex(body.String()))
		}
	}
}

// sublex lexes a separate script, one level deeper.
func (l *lexer) sublex(s string) []token {
	if l.depth+1 > maxDepth {
		return []token{{kind: tWord, text: Opaque}}
	}
	return lexScript(s, l.depth+1)
}

// nested lexes a "$(…)" or "<(…)" body from l.i, up to its closing ")".
func (l *lexer) nested(start int) ([]token, error) {
	if l.depth+1 > maxDepth {
		// Skip the body by counting parentheses; it becomes an opaque command.
		depth := 1
		for l.i < len(l.s) && depth > 0 {
			switch l.s[l.i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			l.i++
		}
		if depth > 0 {
			return nil, &errUnterminated{pos: start}
		}
		return []token{{kind: tWord, text: Opaque}}, nil
	}
	l.depth++
	saved := l.heredocs
	l.heredocs = nil
	toks, err := l.lexList(true, start)
	l.heredocs = append(saved, l.heredocs...)
	l.depth--
	return toks, err
}

// lexWord reads one word. It returns nil, nil when the word is the file
// descriptor number of a redirection ("2>&1").
func (l *lexer) lexWord() (*token, error) {
	var b strings.Builder
	tok := &token{kind: tWord}
	quoted := false
	for l.i < len(l.s) {
		c := l.s[l.i]
		if isMeta(c) {
			if (c == '<' || c == '>') && !quoted && b.Len() > 0 && allDigits(b.String()) {
				return nil, nil
			}
			break
		}
		switch c {
		case '\\':
			if l.i+1 >= len(l.s) {
				b.WriteByte('\\')
				l.i++
				continue
			}
			if l.s[l.i+1] == '\n' {
				l.i += 2
				continue
			}
			b.WriteByte(l.s[l.i+1])
			l.i += 2
			quoted = true
		case '\'':
			if l.literal[l.i] {
				b.WriteByte(c)
				l.i++
				continue
			}
			end := strings.IndexByte(l.s[l.i+1:], '\'')
			if end < 0 {
				return nil, &errUnterminated{pos: l.i}
			}
			b.WriteString(l.s[l.i+1 : l.i+1+end])
			l.i += end + 2
			quoted = true
		case '"':
			if l.literal[l.i] {
				b.WriteByte(c)
				l.i++
				continue
			}
			if err := l.lexDouble(&b, tok); err != nil {
				return nil, err
			}
			quoted = true
		case '`':
			if l.literal[l.i] {
				b.WriteByte(c)
				l.i++
				continue
			}
			if err := l.lexBacktick(&b, tok); err != nil {
				return nil, err
			}
		case '$':
			if err := l.lexDollar(&b, tok); err != nil {
				return nil, err
			}
		default:
			b.WriteByte(c)
			l.i++
		}
	}
	tok.text = b.String()
	if tok.text == "" && !quoted && len(tok.subs) == 0 {
		// Nothing was read (a lone "$" is written, so this does not happen in
		// practice); avoid an endless loop.
		if l.i < len(l.s) && !isMeta(l.s[l.i]) {
			l.i++
		}
	}
	return tok, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// lexDouble reads a double-quoted string starting at l.i.
func (l *lexer) lexDouble(b *strings.Builder, tok *token) error {
	start := l.i
	l.i++
	for l.i < len(l.s) {
		c := l.s[l.i]
		switch c {
		case '"':
			l.i++
			return nil
		case '\\':
			if l.i+1 < len(l.s) {
				n := l.s[l.i+1]
				switch n {
				case '\n':
					l.i += 2
					continue
				case '$', '`', '"', '\\':
					b.WriteByte(n)
					l.i += 2
					continue
				}
			}
			b.WriteByte(c)
			l.i++
		case '`':
			if l.literal[l.i] {
				b.WriteByte(c)
				l.i++
				continue
			}
			if err := l.lexBacktick(b, tok); err != nil {
				return err
			}
		case '$':
			if err := l.lexDollar(b, tok); err != nil {
				return err
			}
		default:
			b.WriteByte(c)
			l.i++
		}
	}
	return &errUnterminated{pos: start}
}

// lexBacktick reads a `…` command substitution starting at l.i.
func (l *lexer) lexBacktick(b *strings.Builder, tok *token) error {
	start := l.i
	l.i++
	var body strings.Builder
	for l.i < len(l.s) {
		c := l.s[l.i]
		if c == '\\' && l.i+1 < len(l.s) && (l.s[l.i+1] == '`' || l.s[l.i+1] == '\\' || l.s[l.i+1] == '$') {
			body.WriteByte(l.s[l.i+1])
			l.i += 2
			continue
		}
		if c == '`' {
			l.i++
			tok.subs = append(tok.subs, l.sublex(body.String()))
			b.WriteString("$(…)")
			return nil
		}
		body.WriteByte(c)
		l.i++
	}
	return &errUnterminated{pos: start}
}

// lexDollar reads a "$" expansion starting at l.i. The word keeps a "$" so that
// resolvers see that it is not a literal.
func (l *lexer) lexDollar(b *strings.Builder, tok *token) error {
	start := l.i
	if l.literal[l.i] {
		b.WriteByte('$')
		l.i++
		return nil
	}
	rest := l.s[l.i:]
	switch {
	case strings.HasPrefix(rest, "$(("):
		// Arithmetic expansion: skip to the matching "))".
		depth := 0
		j := l.i + 1
		for j < len(l.s) {
			switch l.s[j] {
			case '(':
				depth++
			case ')':
				depth--
			}
			j++
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			return &errUnterminated{pos: start}
		}
		b.WriteString(l.s[l.i:j])
		l.i = j
	case strings.HasPrefix(rest, "$("):
		l.i += 2
		sub, err := l.nested(start)
		if err != nil {
			return err
		}
		tok.subs = append(tok.subs, sub)
		b.WriteString("$(…)")
	case strings.HasPrefix(rest, "${"):
		depth := 0
		j := l.i + 1
		for j < len(l.s) {
			switch l.s[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
			j++
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			return &errUnterminated{pos: start}
		}
		b.WriteString(l.s[l.i:j])
		l.i = j
	case strings.HasPrefix(rest, "$'"):
		// ANSI-C quoting: no expansion, so no "$" in the result.
		j := l.i + 2
		var q strings.Builder
		for j < len(l.s) && l.s[j] != '\'' {
			if l.s[j] == '\\' && j+1 < len(l.s) {
				switch l.s[j+1] {
				case 'n':
					q.WriteByte('\n')
				case 't':
					q.WriteByte('\t')
				default:
					q.WriteByte(l.s[j+1])
				}
				j += 2
				continue
			}
			q.WriteByte(l.s[j])
			j++
		}
		if j >= len(l.s) {
			return &errUnterminated{pos: start}
		}
		b.WriteString(q.String())
		l.i = j + 1
	case strings.HasPrefix(rest, "$\""):
		l.i++
		return l.lexDouble(b, tok)
	default:
		// $VAR, $1, $@, or a lone "$": keep the text.
		b.WriteByte('$')
		l.i++
		for l.i < len(l.s) {
			c := l.s[l.i]
			if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
				b.WriteByte(c)
				l.i++
				continue
			}
			break
		}
	}
	return nil
}
