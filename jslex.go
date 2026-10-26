package main

import (
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
)

// jsTokens lexes JavaScript as chroma's JavaScript lexer does, in one
// pass. Its rules live in chroma's lexers/embedded/javascript.xml; this
// follows them, quirks included: a slash after an operator, a keyword or
// an opening bracket starts a regular expression, and one that does not
// close marks the rest of its line as an error.
func jsTokens(code string) []chroma.Token {
	j := &jsLex{lex: newLex(code)}
	i := 0
	if strings.HasPrefix(code, "#!") {
		if k := strings.IndexByte(code, '\n'); k >= 0 && (strings.HasPrefix(code, "#!/") || strings.HasPrefix(code, "#! /")) {
			j.emit(chroma.CommentHashbang, 0, k+1)
			i = k + 1
		}
	}
	for i < len(code) {
		i = j.step(i, false)
	}
	return j.tokens()
}

type jsLex struct {
	lex
	// regex is chroma's slashstartsregex state: the next slash starts a
	// regular expression.
	regex bool
}

var (
	jsKeywords = setOf("for", "in", "while", "do", "break", "return", "continue", "switch", "case", "default", "if",
		"else", "throw", "try", "catch", "finally", "new", "delete", "typeof", "instanceof", "void", "yield", "this", "of")
	jsDeclarations = setOf("var", "let", "with", "function")
	jsReserved     = setOf("abstract", "async", "await", "boolean", "byte", "char", "class", "const", "debugger",
		"double", "enum", "export", "extends", "final", "float", "goto", "implements", "import", "int", "interface",
		"long", "native", "package", "private", "protected", "public", "short", "static", "super", "synchronized",
		"throws", "transient", "using", "volatile")
	jsConstants = setOf("true", "false", "null", "NaN", "Infinity", "undefined")
	jsBuiltins  = setOf("Array", "Boolean", "Date", "Error", "Function", "Math", "netscape", "Number", "Object",
		"Packages", "RegExp", "String", "Promise", "Proxy", "sun", "decodeURI", "decodeURIComponent", "encodeURI",
		"encodeURIComponent", "eval", "isFinite", "isNaN", "isSafeInteger", "parseFloat", "parseInt", "document",
		"this", "window")
)

// step lexes one token at i and returns where the next begins. In a
// template's ${...}, a } ends the expression; step returns -1-i for it.
func (j *jsLex) step(i int, interp bool) int {
	s := j.src
	c := s[i]
	if i == 0 || s[i-1] == '\n' {
		if r, _ := runeAt(s, i); isSpace(r) || c == '/' || strings.HasPrefix(s[i:], "<!--") {
			j.regex = true
		}
	}
	if r, _ := runeAt(s, i); isSpace(r) {
		e := wordEnd(s, i, isSpace)
		j.emit(chroma.Text, i, e)
		return e
	}
	switch {
	case strings.HasPrefix(s[i:], "<!--"):
		j.emit(chroma.Comment, i, i+4)
		return i + 4
	case strings.HasPrefix(s[i:], "//"):
		e := lineEnd(s, i)
		if e < len(s) {
			e++
		}
		j.emit(chroma.CommentSingle, i, e)
		return e
	case strings.HasPrefix(s[i:], "/*"):
		if k := strings.Index(s[i+2:], "*/"); k >= 0 {
			j.emit(chroma.CommentMultiline, i, i+2+k+2)
			return i + 2 + k + 2
		}
	}
	if j.regex {
		j.regex = false
		if c == '/' {
			if e := jsRegex(s, i); e > i {
				j.emit(chroma.LiteralStringRegex, i, e)
				return e
			}
			// badregex: every rune to the line's end is an error.
			e := lineEnd(s, i)
			for k := i; k < e; {
				_, n := runeAt(s, k)
				j.emit(chroma.Error, k, k+n)
				k += n
			}
			return e
		}
	}
	if interp && c == '}' {
		return -1 - i
	}
	switch {
	case isDigit(c):
		e, t := jsNumber(s, i)
		j.emit(t, i, e)
		return e
	case strings.HasPrefix(s[i:], "...") || strings.HasPrefix(s[i:], "=>"):
		e := i + 3
		if c == '=' {
			e = i + 2
		}
		j.emit(chroma.Punctuation, i, e)
		return e
	}
	if e := jsOperator(s, i); e > i {
		j.emit(chroma.Operator, i, e)
		j.regex = true
		return e
	}
	switch {
	case strings.IndexByte("{([;,", c) >= 0:
		j.emit(chroma.Punctuation, i, i+1)
		j.regex = true
		return i + 1
	case strings.IndexByte("})].", c) >= 0:
		j.emit(chroma.Punctuation, i, i+1)
		return i + 1
	}
	if r, _ := runeAt(s, i); unicode.IsLetter(r) {
		w := s[i:wordEnd(s, i, isWord)]
		switch {
		case jsKeywords[w]:
			j.emit(chroma.Keyword, i, i+len(w))
			j.regex = true
			return i + len(w)
		case jsDeclarations[w]:
			j.emit(chroma.KeywordDeclaration, i, i+len(w))
			j.regex = true
			return i + len(w)
		case jsReserved[w]:
			j.emit(chroma.KeywordReserved, i, i+len(w))
			return i + len(w)
		case jsConstants[w]:
			j.emit(chroma.KeywordConstant, i, i+len(w))
			return i + len(w)
		case jsBuiltins[w]:
			j.emit(chroma.NameBuiltin, i, i+len(w))
			return i + len(w)
		}
	}
	if e := jsName(s, i); e > i {
		j.emit(chroma.NameOther, i, e)
		return e
	}
	switch c {
	case '"', '\'':
		if e := jsString(s, i); e > i {
			j.emit(chroma.LiteralString, i, e)
			return e
		}
	case '`':
		return j.template(i)
	}
	_, n := runeAt(s, i)
	j.emit(chroma.Error, i, i+n)
	return i + n
}

// jsName returns the end of a name at i: a letter, digit, $ or _, or a
// \uXXXX escape, then letters, digits and $ but no _, as chroma reads it.
func jsName(s string, i int) int {
	part := func(i int, start bool) int {
		if strings.HasPrefix(s[i:], `\u`) && i+6 <= len(s) {
			for _, c := range []byte(s[i+2 : i+6]) {
				if !(isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
					return i
				}
			}
			return i + 6
		}
		r, n := runeAt(s, i)
		if i < len(s) && (r == '$' || start && r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)) {
			return i + n
		}
		return i
	}
	e := part(i, true)
	if e == i {
		return i
	}
	for {
		k := part(e, false)
		if k == e {
			return e
		}
		e = k
	}
}

// jsNumber follows chroma's number rules in their order.
func jsNumber(s string, i int) (int, chroma.TokenType) {
	d := i
	for d < len(s) && isDigit(s[d]) {
		d++
	}
	if d < len(s) && s[d] == '.' {
		e := d + 1
		for e < len(s) && isDigit(s[e]) {
			e++
		}
		return e, chroma.LiteralNumberFloat
	}
	if d < len(s) && (s[d] == 'e' || s[d] == 'E') {
		e := d + 1
		if e < len(s) && (s[e] == '+' || s[e] == '-') {
			e++
		}
		if f := e; f < len(s) && isDigit(s[f]) {
			for f < len(s) && isDigit(s[f]) {
				f++
			}
			return f, chroma.LiteralNumberFloat
		}
	}
	if s[i] == '0' && i+2 < len(s) {
		var ok func(byte) bool
		t := chroma.LiteralNumberHex
		switch s[i+1] {
		case 'b', 'B':
			ok, t = func(c byte) bool { return c == '0' || c == '1' }, chroma.LiteralNumberBin
		case 'o', 'O':
			ok, t = func(c byte) bool { return c >= '0' && c <= '7' }, chroma.LiteralNumberOct
		case 'x', 'X':
			ok = func(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
		}
		if ok != nil && ok(s[i+2]) {
			e := i + 2
			for e < len(s) && ok(s[e]) {
				e++
			}
			return e, t
		}
	}
	e := i + 1
	for e < len(s) && (isDigit(s[e]) || s[e] == '_') {
		e++
	}
	return e, chroma.LiteralNumberInteger
}

// jsOperator returns the end of an operator at i, as chroma's operator
// rule matches it, or i.
func jsOperator(s string, i int) int {
	for _, op := range []string{"++", "--", "~", "&&", "?", ":", "||"} {
		if strings.HasPrefix(s[i:], op) {
			return i + len(op)
		}
	}
	if s[i] == '\\' && i+1 < len(s) && s[i+1] == '\n' {
		return i + 1
	}
	e := i
	switch {
	case strings.HasPrefix(s[i:], ">>>"):
		e = i + 3
	case strings.HasPrefix(s[i:], "<<"), strings.HasPrefix(s[i:], ">>"), strings.HasPrefix(s[i:], "=="),
		strings.HasPrefix(s[i:], "!="):
		e = i + 2
	case strings.IndexByte("=!-<>+*%&|^/", s[i]) >= 0:
		e = i + 1
	default:
		return i
	}
	if e < len(s) && s[e] == '=' {
		e++
	}
	return e
}

// jsRegex returns the end of a regular expression literal at i, flags
// included, or i when chroma's pattern does not match there.
func jsRegex(s string, i int) int {
	k := i + 1
	body := false
	for k < len(s) {
		c := s[k]
		switch {
		case c == '\\' && k+1 < len(s):
			_, n := runeAt(s, k+1)
			k += 1 + n
		case c == '[':
			m := k + 1
			for m < len(s) && s[m] != ']' && s[m] != '\n' {
				if s[m] == '\\' {
					if m+1 >= len(s) {
						return i
					}
					_, n := runeAt(s, m+1)
					m += n
				}
				m++
			}
			if m >= len(s) || s[m] != ']' {
				return i
			}
			k = m + 1
		case c == '/' || c == '\n':
			if c == '\n' || !body {
				return i
			}
			e := k + 1
			f := e
			for f < len(s) && strings.IndexByte("gimuy", s[f]) >= 0 {
				f++
			}
			next, _ := runeAt(s, f)
			if f > e && (f == len(s) || !isWord(next)) {
				return f
			}
			if r, _ := runeAt(s, e); e == len(s) || !isWord(r) {
				return e
			}
			return i
		default:
			_, n := runeAt(s, k)
			k += n
		}
		body = true
	}
	return i
}

// jsString returns the end of a quoted string at i. A string may span
// lines. One that never closes ends at the last escaped quote, where
// chroma's pattern backtracks to, or does not match at all.
func jsString(s string, i int) int {
	q := s[i]
	last := -1
	for k := i + 1; k < len(s); k++ {
		switch s[k] {
		case '\\':
			if k+1 < len(s) && (s[k+1] == '\\' || s[k+1] == q) {
				if s[k+1] == q {
					last = k + 1
				}
				k++
			}
		case q:
			return k + 1
		}
	}
	if last >= 0 {
		return last + 1
	}
	return i
}

// template lexes a template literal at i, its ${...} expressions as
// code. The first } in an expression ends it, as in chroma.
func (j *jsLex) template(i int) int {
	s := j.src
	j.emit(chroma.LiteralStringBacktick, i, i+1)
	k := i + 1
	for k < len(s) {
		switch {
		case s[k] == '`':
			j.emit(chroma.LiteralStringBacktick, k, k+1)
			return k + 1
		case s[k] == '\\' && k+1 < len(s):
			_, n := runeAt(s, k+1)
			j.emit(chroma.LiteralStringBacktick, k, k+1+n)
			k += 1 + n
		case strings.HasPrefix(s[k:], "${"):
			j.emit(chroma.LiteralStringInterpol, k, k+2)
			saved := j.regex
			j.regex = false
			k += 2
			for k < len(s) {
				n := j.step(k, true)
				if n < 0 {
					k = -1 - n
					j.emit(chroma.LiteralStringInterpol, k, k+1)
					k++
					break
				}
				k = n
			}
			j.regex = saved
		default:
			j.emit(chroma.LiteralStringBacktick, k, k+1)
			k++
		}
	}
	return k
}
