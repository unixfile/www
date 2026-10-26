package main

import (
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
)

// pyTokens lexes Python as chroma's Python lexer does, in one pass. Its
// rules live in chroma's lexers/embedded/python.xml; this follows them,
// quirks included: a keyword stays one after a dot, 1j is a number and a
// name, and a docstring is an ordinary string.
func pyTokens(code string) []chroma.Token {
	p := &pyLex{newLex(code)}
	for i := 0; i < len(code); {
		i = p.step(i, false)
	}
	return p.tokens()
}

type pyLex struct{ lex }

var (
	pyKeywords = setOf("nonlocal", "continue", "finally", "except", "lambda", "assert", "global", "return", "raise",
		"yield", "while", "break", "await", "async", "pass", "else", "elif", "with", "try", "for", "del", "as", "if",
		"match", "case")
	pyExprKeywords = setOf("lambda", "yield", "await", "else", "for", "if")
	pyOpWords      = setOf("in", "is", "and", "or", "not")
	pyConstants    = setOf("False", "True", "None")
	pyBuiltins     = setOf("staticmethod", "classmethod", "memoryview", "__import__", "issubclass", "isinstance",
		"frozenset", "bytearray", "enumerate", "reversed", "property", "compile", "complex", "delattr", "hasattr",
		"setattr", "globals", "getattr", "divmod", "filter", "locals", "format", "object", "sorted", "slice", "print",
		"bytes", "range", "input", "tuple", "round", "super", "float", "eval", "list", "dict", "repr", "type", "vars",
		"hash", "next", "bool", "open", "iter", "oct", "pow", "min", "zip", "max", "map", "bin", "len", "set", "any",
		"dir", "all", "abs", "str", "sum", "chr", "int", "hex", "ord", "id", "self", "Ellipsis", "NotImplemented", "cls")
	pyMagicFuncs = setOf("__instancecheck__", "__subclasscheck__", "__getattribute__", "__length_hint__",
		"__rfloordiv__", "__ifloordiv__", "__itruediv__", "__contains__", "__floordiv__", "__rtruediv__",
		"__reversed__", "__setitem__", "__complex__", "__rdivmod__", "__delattr__", "__rmatmul__", "__ilshift__",
		"__prepare__", "__delitem__", "__rrshift__", "__imatmul__", "__rlshift__", "__setattr__", "__truediv__",
		"__getitem__", "__missing__", "__getattr__", "__irshift__", "__rshift__", "__format__", "__invert__",
		"__matmul__", "__divmod__", "__delete__", "__aenter__", "__lshift__", "__await__", "__bytes__", "__anext__",
		"__aiter__", "__aexit__", "__round__", "__float__", "__enter__", "__index__", "__iadd__", "__ipow__",
		"__rpow__", "__iter__", "__init__", "__ixor__", "__rmul__", "__rmod__", "__imul__", "__imod__", "__iand__",
		"__hash__", "__rsub__", "__exit__", "__rxor__", "__bool__", "__call__", "__rand__", "__next__", "__radd__",
		"__isub__", "__repr__", "__set__", "__add__", "__new__", "__neg__", "__xor__", "__and__", "__mul__",
		"__mod__", "__sub__", "__len__", "__str__", "__ror__", "__ior__", "__pos__", "__del__", "__get__", "__dir__",
		"__abs__", "__int__", "__pow__", "__eq__", "__gt__", "__le__", "__lt__", "__ne__", "__or__", "__ge__")
	pyMagicVars = setOf("__annotations__", "__kwdefaults__", "__qualname__", "__objclass__", "__defaults__",
		"__closure__", "__globals__", "__weakref__", "__module__", "__class__", "__bases__", "__slots__", "__file__",
		"__code__", "__name__", "__func__", "__dict__", "__self__", "__mro__", "__doc__")
	pyPrefixes = setOf("r", "u", "b", "f", "rb", "br", "rf", "fr")
)

func isPyStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isPyPart(r rune) bool  { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// step lexes one token at i and returns where the next begins. inF is set
// inside an f-string's braces, where chroma knows fewer rules.
func (p *pyLex) step(i int, inF bool) int {
	s := p.src
	c := s[i]
	switch {
	case c == '\n':
		p.emit(chroma.Text, i, i+1)
		return i + 1
	case c == '#' && !inF:
		e := lineEnd(s, i)
		p.emit(chroma.CommentSingle, i, e)
		return e
	case c == '\\' && !inF:
		e := i + 1
		if e < len(s) && s[e] == '\n' {
			e++
		}
		p.emit(chroma.Text, i, e)
		return e
	case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
		e := i
		for e < len(s) && (s[e] == ' ' || s[e] == '\t' || s[e] == '\r' || s[e] == '\f' || s[e] == '\v' || inF && s[e] == '\n') {
			e++
		}
		p.emit(chroma.Text, i, e)
		return e
	case c == '"' || c == '\'':
		return p.str(i, i)
	case isDigit(c) || c == '.' && i+1 < len(s) && isDigit(s[i+1]):
		e, t := pyNumber(s, i)
		p.emit(t, i, e)
		return e
	case c == '!' && i+1 < len(s) && s[i+1] == '=', c == '=' && i+1 < len(s) && s[i+1] == '=',
		c == '<' && i+1 < len(s) && s[i+1] == '<', c == '>' && i+1 < len(s) && s[i+1] == '>',
		c == ':' && i+1 < len(s) && s[i+1] == '=':
		p.emit(chroma.Operator, i, i+2)
		return i + 2
	case strings.IndexByte("-~+/*%=<>&^|.", c) >= 0:
		p.emit(chroma.Operator, i, i+1)
		return i + 1
	case strings.IndexByte("[]{}:(),;", c) >= 0:
		p.emit(chroma.Punctuation, i, i+1)
		return i + 1
	case c == '@':
		if e := p.decorator(i); e > i {
			p.emit(chroma.NameDecorator, i, e)
			return e
		}
		p.emit(chroma.Operator, i, i+1)
		return i + 1
	}
	r, n := runeAt(s, i)
	if !isPyStart(r) {
		p.emit(chroma.Error, i, i+n)
		return i + n
	}
	e := wordEnd(s, i, isPyPart)
	w := s[i:e]
	if e < len(s) && (s[e] == '"' || s[e] == '\'') && pyPrefixes[strings.ToLower(w)] {
		return p.str(i, e)
	}
	if !inF {
		if w == "yield" && hasWordAt(s, e, " from") {
			p.emit(chroma.Keyword, i, e+5)
			return e + 5
		}
		if pyKeywords[w] {
			p.emit(chroma.Keyword, i, e)
			return e
		}
		if pyConstants[w] {
			p.emit(chroma.KeywordConstant, i, e)
			return e
		}
		switch w {
		case "def", "class", "from", "import":
			if ws := pySpaceRun(s, e); ws > e {
				return p.statement(w, i, e, ws)
			}
		}
	}
	if pyOpWords[w] {
		p.emit(chroma.OperatorWord, i, e)
		return e
	}
	if inF {
		switch {
		case w == "yield" && hasWordAt(s, e, " from"), w == "async" && hasWordAt(s, e, " for"):
			p.emit(chroma.Keyword, i, wordEnd(s, e+1, isPyPart))
			return wordEnd(s, e+1, isPyPart)
		case pyExprKeywords[w]:
			p.emit(chroma.Keyword, i, e)
			return e
		case pyConstants[w]:
			p.emit(chroma.KeywordConstant, i, e)
			return e
		}
	}
	afterDot := i > 0 && s[i-1] == '.'
	switch {
	case pyBuiltins[w] && !afterDot:
		p.emit(chroma.NameBuiltin, i, e)
	case pyMagicFuncs[w]:
		p.emit(chroma.NameFunctionMagic, i, e)
	case pyMagicVars[w]:
		p.emit(chroma.NameVariableMagic, i, e)
	default:
		p.emit(chroma.Name, i, e)
	}
	return e
}

// hasWordAt reports whether s holds word at i, ending at a word boundary.
func hasWordAt(s string, i int, word string) bool {
	if !strings.HasPrefix(s[i:], word) {
		return false
	}
	r, _ := runeAt(s, i+len(word))
	return i+len(word) == len(s) || !isWord(r)
}

// pySpaceRun returns the end of (?:\s|\\\s)+ from i, or i when none.
func pySpaceRun(s string, i int) int {
	for i < len(s) {
		r, n := runeAt(s, i)
		switch {
		case isSpace(r):
			i += n
		case r == '\\' && i+1 < len(s):
			if r2, n2 := runeAt(s, i+1); isSpace(r2) {
				i += 1 + n2
				continue
			}
			return i
		default:
			return i
		}
	}
	return i
}

// statement lexes def, class, from and import with what follows them, as
// chroma's funcname, classname, fromimport and import states do.
func (p *pyLex) statement(w string, i, e, ws int) int {
	s := p.src
	if w == "from" || w == "import" {
		p.emit(chroma.KeywordNamespace, i, e)
	} else {
		p.emit(chroma.Keyword, i, e)
	}
	p.emit(chroma.Text, e, ws)
	j := ws
	ident := func(j int) int {
		if r, _ := runeAt(s, j); j < len(s) && isPyStart(r) {
			return wordEnd(s, j, isPyPart)
		}
		return j
	}
	switch w {
	case "def":
		if k := ident(j); k > j {
			p.emit(chroma.NameFunction, j, k)
			return k
		}
	case "class":
		if k := ident(j); k > j {
			p.emit(chroma.NameClass, j, k)
			return k
		}
	case "from":
		for j < len(s) {
			if s[j] == '.' {
				p.emit(chroma.NameNamespace, j, j+1)
				j++
				continue
			}
			if k := ident(j); k > j {
				if s[j:k] == "None" {
					p.emit(chroma.NameBuiltinPseudo, j, k)
					return k
				}
				p.emit(chroma.NameNamespace, j, k)
				j = k
				continue
			}
			k := wordEnd(s, j, isSpace)
			if k > j && hasWordAt(s, k, "import") {
				p.emit(chroma.Text, j, k)
				p.emit(chroma.KeywordNamespace, k, k+6)
				return k + 6
			}
			return j
		}
	case "import":
		for j < len(s) {
			k := wordEnd(s, j, isSpace)
			if k > j && hasWordAt(s, k, "as") {
				if m := wordEnd(s, k+2, isSpace); m > k+2 {
					p.emit(chroma.Text, j, k)
					p.emit(chroma.Keyword, k, k+2)
					p.emit(chroma.Text, k+2, m)
					j = m
					continue
				}
			}
			if s[j] == '.' {
				p.emit(chroma.NameNamespace, j, j+1)
				j++
				continue
			}
			if m := ident(j); m > j {
				p.emit(chroma.NameNamespace, j, m)
				j = m
				continue
			}
			if k < len(s) && s[k] == ',' {
				m := wordEnd(s, k+1, isSpace)
				p.emit(chroma.Text, j, k)
				p.emit(chroma.Operator, k, k+1)
				p.emit(chroma.Text, k+1, m)
				j = m
				continue
			}
			return j
		}
	}
	return j
}

// decorator returns the end of @name(\s*\.\s*name)* at i, or i.
func (p *pyLex) decorator(i int) int {
	s := p.src
	r, _ := runeAt(s, i+1)
	if i+1 >= len(s) || !isPyStart(r) {
		return i
	}
	e := wordEnd(s, i+1, isPyPart)
	for {
		j := wordEnd(s, e, isSpace)
		if j >= len(s) || s[j] != '.' {
			return e
		}
		j = wordEnd(s, j+1, isSpace)
		if r, _ := runeAt(s, j); j >= len(s) || !isPyStart(r) {
			return e
		}
		e = wordEnd(s, j, isPyPart)
	}
}

// pyDigits returns the end of \d(?:_?\d)* at i, or -1.
func pyDigits(s string, i int) int {
	if i >= len(s) || !isDigit(s[i]) {
		return -1
	}
	i++
	for i < len(s) {
		if isDigit(s[i]) {
			i++
		} else if s[i] == '_' && i+1 < len(s) && isDigit(s[i+1]) {
			i += 2
		} else {
			break
		}
	}
	return i
}

// pyExponent returns the end of [eE][+-]?digits at i, or -1.
func pyExponent(s string, i int) int {
	if i >= len(s) || s[i] != 'e' && s[i] != 'E' {
		return -1
	}
	i++
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	return pyDigits(s, i)
}

// pyNumber follows chroma's number rules in their order: floats, octal,
// binary and hex, then integers.
func pyNumber(s string, i int) (int, chroma.TokenType) {
	withExp := func(j int) int {
		if e := pyExponent(s, j); e > 0 {
			return e
		}
		return j
	}
	d := pyDigits(s, i)
	if d > 0 && d < len(s) && s[d] == '.' {
		j := d + 1
		if f := pyDigits(s, j); f > 0 {
			j = f
		}
		return withExp(j), chroma.LiteralNumberFloat
	}
	if s[i] == '.' {
		if f := pyDigits(s, i+1); f > 0 {
			return withExp(f), chroma.LiteralNumberFloat
		}
	}
	if d > 0 {
		if e := pyExponent(s, d); e > 0 {
			if e < len(s) && s[e] == 'j' {
				e++
			}
			return e, chroma.LiteralNumberFloat
		}
	}
	if s[i] == '0' && i+2 < len(s) {
		var ok func(byte) bool
		t := chroma.LiteralNumberHex
		switch s[i+1] {
		case 'o', 'O':
			ok, t = func(c byte) bool { return c >= '0' && c <= '7' }, chroma.LiteralNumberOct
		case 'b', 'B':
			ok, t = func(c byte) bool { return c == '0' || c == '1' }, chroma.LiteralNumberBin
		case 'x', 'X':
			ok = func(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
		}
		if ok != nil {
			j := i + 2
			for j < len(s) {
				if ok(s[j]) {
					j++
				} else if s[j] == '_' && j+1 < len(s) && ok(s[j+1]) {
					j += 2
				} else {
					break
				}
			}
			if j > i+2 {
				return j, t
			}
		}
	}
	return d, chroma.LiteralNumberInteger
}

// pyEscape returns the end of the escape at i in a string that is not raw,
// as chroma's stringescape matches it, or i.
func pyEscape(s string, i int) int {
	if i+1 >= len(s) || s[i] != '\\' {
		return i
	}
	c := s[i+1]
	hex := func(n int) int {
		for k := 0; k < n; k++ {
			if j := i + 2 + k; j >= len(s) || !(isDigit(s[j]) || s[j] >= 'a' && s[j] <= 'f' || s[j] >= 'A' && s[j] <= 'F') {
				return i
			}
		}
		return i + 2 + n
	}
	switch {
	case strings.IndexByte("\\abfnrtv\"'\n", c) >= 0:
		return i + 2
	case c == 'N' && i+2 < len(s) && s[i+2] == '{':
		if k := strings.IndexByte(s[i+3:], '}'); k >= 0 && !strings.Contains(s[i+3:i+3+k], "\n") {
			return i + 3 + k + 1
		}
	case c == 'u':
		return hex(4)
	case c == 'U':
		return hex(8)
	case c == 'x':
		return hex(2)
	case c >= '0' && c <= '7':
		j := i + 2
		for j < len(s) && j < i+4 && s[j] >= '0' && s[j] <= '7' {
			j++
		}
		return j
	}
	return i
}

// str lexes a string whose prefix runs from i to q, where its quote is.
// Every part is a string to the stylesheet except escapes, which an
// f-string's {{ and }} count among, and an f-string's expressions.
func (p *pyLex) str(i, q int) int {
	s := p.src
	prefix := strings.ToLower(s[i:q])
	raw, f := strings.Contains(prefix, "r"), strings.Contains(prefix, "f")
	p.emit(chroma.LiteralStringAffix, i, q)
	quote := s[q : q+1]
	triple := strings.HasPrefix(s[q:], quote+quote+quote)
	closer := quote
	if triple {
		closer = quote + quote + quote
	}
	p.emit(chroma.LiteralString, q, q+len(closer))
	j := q + len(closer)
	for j < len(s) {
		switch {
		case strings.HasPrefix(s[j:], closer):
			p.emit(chroma.LiteralString, j, j+len(closer))
			return j + len(closer)
		case f && (strings.HasPrefix(s[j:], "{{") || strings.HasPrefix(s[j:], "}}")):
			p.emit(chroma.LiteralStringEscape, j, j+2)
			j += 2
		case s[j] == '\\' && !raw && pyEscape(s, j) > j:
			e := pyEscape(s, j)
			p.emit(chroma.LiteralStringEscape, j, e)
			j = e
		case s[j] == '\\' && !triple && j+1 < len(s) && (s[j+1] == '\\' || s[j+1] == quote[0] || s[j+1] == '\n'):
			p.emit(chroma.LiteralStringEscape, j, j+2)
			j += 2
		case f && s[j] == '{':
			p.emit(chroma.LiteralStringInterpol, j, j+1)
			j = p.fexpr(j + 1)
		case s[j] == '\n' && !triple:
			return j
		default:
			p.emit(chroma.LiteralString, j, j+1)
			j++
		}
	}
	return j
}

// fexpr lexes the expression in an f-string's braces from i, up to and
// including its closing } or the : that starts a format spec.
func (p *pyLex) fexpr(i int) int {
	s := p.src
	depth := 0
	for i < len(s) {
		c := s[i]
		if depth == 0 {
			j := i
			if c == '=' {
				j = wordEnd(s, i+1, isSpace)
			}
			if j+1 < len(s) && s[j] == '!' && strings.IndexByte("sraf", s[j+1]) >= 0 {
				j += 2
			}
			if j < len(s) && (s[j] == '}' || s[j] == ':') {
				p.emit(chroma.LiteralStringInterpol, i, j+1)
				return j + 1
			}
		}
		switch {
		case c == '{' || c == '(' || c == '[':
			p.emit(chroma.Punctuation, i, i+1)
			depth++
			i++
		case depth > 0 && (c == '}' || c == ')' || c == ']'):
			p.emit(chroma.Punctuation, i, i+1)
			depth--
			i++
		default:
			i = p.step(i, true)
		}
	}
	return i
}
