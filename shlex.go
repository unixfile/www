package main

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
)

// shTokens lexes shell as chroma's Bash lexer does, in one pass. Its rules
// live in chroma's lexers/embedded/bash.xml; this follows them, quirks
// included: a keyword needs a word boundary on both sides, a builtin only
// a space, ) or ` after it, and a number only a space or the line's end.
func shTokens(code string) []chroma.Token {
	l := &shLex{lex: lex{src: code}}
	i := 0
	if strings.HasPrefix(code, "#!") {
		if k := strings.IndexByte(code, '\n'); k > 2 {
			l.emit(chroma.CommentPreproc, 0, k+1)
			i = k + 1
		}
	}
	for i < len(code) {
		i = l.step(i, 0)
	}
	return l.tokens()
}

type shLex struct{ lex }

var (
	shKeywords = setOf("if", "fi", "else", "while", "do", "done", "for", "then", "return", "function", "case",
		"select", "continue", "until", "esac", "elif")
	shBuiltins = setOf("alias", "bg", "bind", "break", "builtin", "caller", "cd", "command", "compgen", "complete",
		"declare", "dirs", "disown", "echo", "enable", "eval", "exec", "exit", "export", "false", "fc", "fg",
		"getopts", "hash", "help", "history", "jobs", "kill", "let", "local", "logout", "popd", "printf", "pushd",
		"pwd", "read", "readonly", "set", "shift", "shopt", "source", "suspend", "test", "time", "times", "trap",
		"true", "type", "typeset", "ulimit", "umask", "unalias", "unset", "wait")
)

// The states that end on a closer: $( ... ), $(( ... )), ` ... ` and
// ${ ... }. In them step reports the closer by returning -1-i.
const (
	shRoot = iota
	shParen
	shMath
	shBacktick
	shCurly
)

func isASCIIWord(c byte) bool {
	return c == '_' || isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// wordBoundary reports whether \b holds at i.
func wordBoundary(s string, i int) bool {
	before, after := false, false
	if i > 0 {
		r, _ := lastRune(s[:i])
		before = isWord(r)
	}
	if i < len(s) {
		r, _ := runeAt(s, i)
		after = isWord(r)
	}
	return before != after
}

func lastRune(s string) (rune, int) {
	for k := len(s) - 1; k >= 0 && k >= len(s)-4; k-- {
		if s[k] < 0x80 || s[k] >= 0xC0 {
			return runeAt(s, k)
		}
	}
	return runeAt(s, len(s)-1)
}

// step lexes one token at i in state st and returns where the next begins.
func (l *shLex) step(i int, st int) int {
	s := l.src
	switch st {
	case shParen:
		if s[i] == ')' {
			return -1 - i
		}
	case shMath:
		if strings.HasPrefix(s[i:], "))") {
			return -1 - i
		}
		switch {
		case strings.HasPrefix(s[i:], "**"), strings.HasPrefix(s[i:], "||"):
			l.emit(chroma.Operator, i, i+1)
			return i + 1
		case strings.IndexByte("-+*/%^|&", s[i]) >= 0:
			l.emit(chroma.Operator, i, i+1)
			return i + 1
		case isDigit(s[i]):
			e := i
			for e < len(s) && isDigit(s[e]) {
				e++
			}
			if e < len(s) && s[e] == '#' {
				f := e + 1
				for f < len(s) && isDigit(s[f]) {
					f++
				}
				if f > e+1 {
					e = f
				} else if e+1 >= len(s) || s[e+1] != ' ' {
					e++
				}
			}
			l.emit(chroma.LiteralNumber, i, e)
			return e
		}
	case shBacktick:
		if s[i] == '`' {
			return -1 - i
		}
	case shCurly:
		switch {
		case s[i] == '}':
			return -1 - i
		case strings.HasPrefix(s[i:], ":-"):
			l.emit(chroma.Keyword, i, i+2)
			return i + 2
		}
		if r, _ := runeAt(s, i); isWord(r) {
			e := wordEnd(s, i, isWord)
			l.emit(chroma.NameVariable, i, e)
			return e
		}
		if e := strings.IndexAny(s[i:], "}:\"'`$\\"); e != 0 {
			if e < 0 {
				e = len(s) - i
			}
			l.emit(chroma.Punctuation, i, i+e)
			return i + e
		}
		if s[i] == ':' {
			l.emit(chroma.Punctuation, i, i+1)
			return i + 1
		}
	}
	return l.root(i)
}

// root lexes one token by chroma's root state: basic, then backticks,
// data and interp.
func (l *shLex) root(i int) int {
	s := l.src
	c := s[i]
	if r, _ := runeAt(s, i); isWord(r) && wordBoundary(s, i) {
		w := s[i:wordEnd(s, i, isWord)]
		e := i + len(w)
		if shKeywords[w] {
			// (\s*)\b: the spaces go only as far as a boundary follows.
			ws := wordEnd(s, e, isSpace)
			for ws > e && !wordBoundary(s, ws) {
				ws--
			}
			l.emit(chroma.Keyword, i, e)
			l.emit(chroma.Text, e, ws)
			return ws
		}
		if shBuiltins[w] && e < len(s) {
			if r, _ := runeAt(s, e); isSpace(r) || r == ')' || r == '`' {
				l.emit(chroma.NameBuiltin, i, e)
				return e
			}
		}
	}
	switch {
	case c == '#':
		e := lineEnd(s, i)
		l.emit(chroma.CommentSingle, i, e)
		return e
	case c == '\\' && i+1 < len(s):
		_, n := runeAt(s, i+1)
		l.emit(chroma.LiteralStringEscape, i, i+1+n)
		return i + 1 + n
	}
	if e := l.assignment(i); e > i {
		return e
	}
	switch {
	case strings.IndexByte("[]{}()=", c) >= 0:
		l.emit(chroma.Operator, i, i+1)
		return i + 1
	case strings.HasPrefix(s[i:], "<<<"):
		l.emit(chroma.Operator, i, i+3)
		return i + 3
	case strings.HasPrefix(s[i:], "<<"):
		if e := shHeredoc(s, i); e > i {
			l.emit(chroma.LiteralString, i, e)
			return e
		}
	case strings.HasPrefix(s[i:], "&&"), strings.HasPrefix(s[i:], "||"):
		l.emit(chroma.Operator, i, i+2)
		return i + 2
	case c == '`':
		l.emit(chroma.LiteralStringBacktick, i, i+1)
		return l.nested(i+1, shBacktick, chroma.LiteralStringBacktick, 1)
	}
	return l.data(i)
}

// assignment lexes (\b\w+)(\s*)(\+?=) at i, a variable being set.
func (l *shLex) assignment(i int) int {
	s := l.src
	if r, _ := runeAt(s, i); !isWord(r) || !wordBoundary(s, i) {
		return i
	}
	e := wordEnd(s, i, isWord)
	k := e
	for k < len(s) && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r' || s[k] == '\f' || s[k] == '\v') {
		k++
	}
	op := k
	if op < len(s) && s[op] == '+' {
		op++
	}
	if op >= len(s) || s[op] != '=' {
		return i
	}
	l.emit(chroma.NameVariable, i, e)
	l.emit(chroma.Text, e, k)
	l.emit(chroma.Operator, k, op+1)
	return op + 1
}

// shHeredoc matches <<-?\s*('?)\\?(\w+)[\w\W]+?\2 at i: the heredoc runs
// to the next place its word appears, as chroma's pattern does.
func shHeredoc(s string, i int) int {
	k := i + 2
	if k < len(s) && s[k] == '-' {
		k++
	}
	k = wordEnd(s, k, isSpace)
	if k < len(s) && s[k] == '\'' {
		k++
	}
	if k < len(s) && s[k] == '\\' {
		k++
	}
	e := wordEnd(s, k, isWord)
	if e == k {
		return i
	}
	// \w+ backtracks: the shortest word whose next appearance comes
	// soonest wins, so try each length from the longest down, as the
	// regex engine does, and keep the first that matches.
	for w := e; w > k; w-- {
		word := s[k:w]
		if m := strings.Index(s[w+1:], word); m >= 0 && w+1 <= len(s) {
			return w + 1 + m + len(word)
		}
	}
	return i
}

// data lexes chroma's data and interp rules.
func (l *shLex) data(i int) int {
	s := l.src
	c := s[i]
	switch {
	case c == '"' || c == '$' && i+1 < len(s) && s[i+1] == '"':
		start := i
		if c == '$' {
			i++
		}
		if e := shPlainDouble(s, i); e > 0 {
			l.emit(chroma.LiteralStringDouble, start, e)
			return e
		}
		if c == '"' {
			l.emit(chroma.LiteralStringDouble, i, i+1)
			return l.double(i + 1)
		}
	case c == '$' && i+1 < len(s) && s[i+1] == '\'':
		if e := shDollarSingle(s, i+1); e > 0 {
			l.emit(chroma.LiteralStringSingle, i, e)
			return e
		}
	case c == '\'':
		if k := strings.IndexByte(s[i+1:], '\''); k >= 0 {
			l.emit(chroma.LiteralStringSingle, i, i+1+k+1)
			return i + 1 + k + 1
		}
	case c == ';' || c == '&' || c == '|':
		l.emit(chroma.Punctuation, i, i+1)
		return i + 1
	}
	if r, _ := runeAt(s, i); isSpace(r) {
		e := wordEnd(s, i, isSpace)
		l.emit(chroma.Text, i, e)
		return e
	}
	if isDigit(c) {
		e := i
		for e < len(s) && isDigit(s[e]) {
			e++
		}
		// \d+(?= |$): the digits may end early where a space follows.
		for k := e; k > i; k-- {
			if k == len(s) || s[k] == ' ' || s[k] == '\n' {
				l.emit(chroma.LiteralNumber, i, k)
				return k
			}
		}
	}
	if e := shTextEnd(s, i); e > i {
		l.emit(chroma.Text, i, e)
		return e
	}
	if c == '<' {
		l.emit(chroma.Text, i, i+1)
		return i + 1
	}
	if c == '$' {
		return l.interp(i)
	}
	_, n := runeAt(s, i)
	l.emit(chroma.Error, i, i+n)
	return i + n
}

// shTextEnd returns the end of [^=\s\[\]{}()$"'`\\<&|;]+ at i.
func shTextEnd(s string, i int) int {
	return wordEnd(s, i, func(r rune) bool {
		return !isSpace(r) && !strings.ContainsRune("=[]{}()$\"'`\\<&|;", r)
	})
}

// shPlainDouble matches \$?"(\\\\|\\[0-7]+|\\.|[^"\\$])*" at the quote i:
// a double-quoted string with no $ in it. It returns the end, or 0.
func shPlainDouble(s string, i int) int {
	for k := i + 1; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '$':
			return 0
		case '"':
			return k + 1
		}
	}
	return 0
}

// shDollarSingle matches \$'(\\\\|\\[0-7]+|\\.|[^'\\])*' from the quote i.
func shDollarSingle(s string, i int) int {
	for k := i + 1; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++
		case '\'':
			return k + 1
		}
	}
	return 0
}

// double lexes the rest of a double-quoted string that holds a $.
func (l *shLex) double(i int) int {
	s := l.src
	for i < len(s) {
		switch {
		case s[i] == '"':
			l.emit(chroma.LiteralStringDouble, i, i+1)
			return i + 1
		case s[i] == '$':
			i = l.interp(i)
		default:
			e := i
			for e < len(s) && s[e] != '"' && s[e] != '$' {
				if s[e] == '\\' && e+1 < len(s) {
					e++
				}
				e++
			}
			l.emit(chroma.LiteralStringDouble, i, e)
			i = e
		}
	}
	return i
}

// interp lexes a $ form at i.
func (l *shLex) interp(i int) int {
	s := l.src
	switch {
	case strings.HasPrefix(s[i:], "$(("):
		l.emit(chroma.Keyword, i, i+3)
		return l.nested(i+3, shMath, chroma.Keyword, 2)
	case strings.HasPrefix(s[i:], "$("):
		l.emit(chroma.Keyword, i, i+2)
		return l.nested(i+2, shParen, chroma.Keyword, 1)
	case strings.HasPrefix(s[i:], "${"):
		e := i + 2
		if e < len(s) && s[e] == '#' {
			e++
		}
		l.emit(chroma.LiteralStringInterpol, i, e)
		return l.nested(e, shCurly, chroma.LiteralStringInterpol, 1)
	}
	if i+1 < len(s) {
		c := s[i+1]
		switch {
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			e := wordEnd(s, i+1, isWord)
			l.emit(chroma.NameVariable, i, e)
			return e
		case isDigit(c):
			e := i + 1
			for e < len(s) && isDigit(s[e]) {
				e++
			}
			l.emit(chroma.NameVariable, i, e)
			return e
		case strings.IndexByte("#$?!_*@-", c) >= 0:
			l.emit(chroma.NameVariable, i, i+2)
			return i + 2
		}
	}
	l.emit(chroma.Text, i, i+1)
	return i + 1
}

// nested lexes a state from i to its closer, which it emits as t with
// width n.
func (l *shLex) nested(i, st int, t chroma.TokenType, n int) int {
	s := l.src
	for i < len(s) {
		k := l.step(i, st)
		if k < 0 {
			k = -1 - k
			l.emit(t, k, k+n)
			return k + n
		}
		i = k
	}
	return i
}
