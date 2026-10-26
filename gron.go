package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2/lexers"
)

// isJSON says whether a code page offers the gron view.
func isJSON(name string) bool {
	return strings.EqualFold(path.Ext(name), ".json")
}

// gronLexer colours the gron view: its lines are JavaScript.
var gronLexer = lexers.Get("javascript")

// gron writes each node of a JSON document on its own line as a JS
// assignment, json.a.b[0] = "x";, so the page reads and searches by path.
// It scans the input once and builds no tree. Keys keep their input order
// and values print as written. Malformed input returns an error that names
// its line and column.
func gron(b []byte) (out []byte, err error) {
	g := &gronner{buf: b, path: []byte("json")}
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(gronError)
			if !ok {
				panic(r)
			}
			out, err = nil, e
		}
	}()
	g.ws()
	if g.pos == len(b) {
		g.fail("no value")
	}
	g.value()
	g.ws()
	if g.pos != len(b) {
		g.fail("data after the value")
	}
	return g.out.Bytes(), nil
}

type gronError struct {
	line, col int
	msg       string
}

func (e gronError) Error() string {
	return fmt.Sprintf("line %d, column %d: %s", e.line, e.col, e.msg)
}

// gronMaxDepth bounds nesting, so a hostile file cannot exhaust the stack.
const gronMaxDepth = 10000

type gronner struct {
	buf   []byte
	pos   int
	depth int
	path  []byte
	out   bytes.Buffer
}

func (g *gronner) fail(msg string) {
	line, col := 1, 1
	for _, c := range g.buf[:min(g.pos, len(g.buf))] {
		if c == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	panic(gronError{line, col, msg})
}

func (g *gronner) ws() {
	for g.pos < len(g.buf) {
		switch g.buf[g.pos] {
		case ' ', '\t', '\n', '\r':
			g.pos++
		default:
			return
		}
	}
}

func (g *gronner) peek() byte {
	if g.pos < len(g.buf) {
		return g.buf[g.pos]
	}
	return 0
}

func (g *gronner) emit(v []byte) {
	g.out.Write(g.path)
	g.out.WriteString(" = ")
	g.out.Write(v)
	g.out.WriteString(";\n")
}

func (g *gronner) value() {
	g.ws()
	start := g.pos
	switch c := g.peek(); {
	case c == '{':
		g.object()
	case c == '[':
		g.array()
	case c == '"':
		g.str()
		g.emit(g.buf[start:g.pos])
	case c == '-' || isDigit(c):
		g.number()
		g.emit(g.buf[start:g.pos])
	case c == 't':
		g.literal("true")
	case c == 'f':
		g.literal("false")
	case c == 'n':
		g.literal("null")
	case g.pos >= len(g.buf):
		g.fail("unexpected end")
	default:
		g.fail(fmt.Sprintf("unexpected %q", c))
	}
}

func (g *gronner) enter() {
	g.depth++
	if g.depth > gronMaxDepth {
		g.fail(fmt.Sprintf("nested deeper than %d", gronMaxDepth))
	}
	g.pos++
	g.ws()
}

func (g *gronner) object() {
	g.emit([]byte("{}"))
	g.enter()
	defer func() { g.depth-- }()
	if g.peek() == '}' {
		g.pos++
		return
	}
	for {
		g.ws()
		if g.peek() != '"' {
			g.fail("expected a string key")
		}
		start := g.pos
		esc := g.str()
		n := len(g.path)
		g.path = appendKey(g.path, g.buf[start:g.pos], esc)
		g.ws()
		if g.peek() != ':' {
			g.fail("expected ':'")
		}
		g.pos++
		g.value()
		g.path = g.path[:n]
		g.ws()
		switch g.peek() {
		case ',':
			g.pos++
		case '}':
			g.pos++
			return
		default:
			g.fail("expected ',' or '}'")
		}
	}
}

func (g *gronner) array() {
	g.emit([]byte("[]"))
	g.enter()
	defer func() { g.depth-- }()
	if g.peek() == ']' {
		g.pos++
		return
	}
	for i := 0; ; i++ {
		n := len(g.path)
		g.path = append(g.path, '[')
		g.path = strconv.AppendInt(g.path, int64(i), 10)
		g.path = append(g.path, ']')
		g.value()
		g.path = g.path[:n]
		g.ws()
		switch g.peek() {
		case ',':
			g.pos++
		case ']':
			g.pos++
			return
		default:
			g.fail("expected ',' or ']'")
		}
	}
}

// str scans a string and reports whether it holds escapes.
func (g *gronner) str() (esc bool) {
	g.pos++
	for g.pos < len(g.buf) {
		c := g.buf[g.pos]
		switch {
		case c == '"':
			g.pos++
			return esc
		case c == '\\':
			esc = true
			g.pos++
			switch g.peek() {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				for range 4 {
					g.pos++
					if !isHex(g.peek()) {
						g.fail(`bad \u escape`)
					}
				}
			default:
				g.fail("bad escape")
			}
		case c < 0x20:
			g.fail("control character in a string")
		}
		g.pos++
	}
	g.fail("unterminated string")
	return
}

func (g *gronner) digits() {
	if !isDigit(g.peek()) {
		g.fail("expected a digit")
	}
	for isDigit(g.peek()) {
		g.pos++
	}
}

func (g *gronner) number() {
	if g.peek() == '-' {
		g.pos++
	}
	if g.peek() == '0' {
		g.pos++
	} else {
		g.digits()
	}
	if g.peek() == '.' {
		g.pos++
		g.digits()
	}
	if c := g.peek(); c == 'e' || c == 'E' {
		g.pos++
		if c := g.peek(); c == '+' || c == '-' {
			g.pos++
		}
		g.digits()
	}
}

func (g *gronner) literal(s string) {
	if !bytes.HasPrefix(g.buf[g.pos:], []byte(s)) {
		g.fail("bad literal")
	}
	g.pos += len(s)
	g.emit([]byte(s))
}

func isHex(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// appendKey adds a key to the path: .name when the key is a JS identifier,
// otherwise ["key"] with the key as written.
func appendKey(path, quoted []byte, esc bool) []byte {
	name := quoted[1 : len(quoted)-1]
	if esc {
		var s string
		if json.Unmarshal(quoted, &s) == nil {
			name = []byte(s)
		}
	}
	if utf8.Valid(name) && isIdent(name) {
		path = append(path, '.')
		return append(path, name...)
	}
	path = append(path, '[')
	path = append(path, quoted...)
	return append(path, ']')
}

// isIdent follows gron's rule: a JS identifier that is not a reserved word.
func isIdent(b []byte) bool {
	if len(b) == 0 || gronReserved[string(b)] {
		return false
	}
	for i, r := range string(b) {
		if r == '$' || r == '_' || unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lm, unicode.Lo, unicode.Nl) {
			continue
		}
		if i > 0 && unicode.In(r, unicode.Mn, unicode.Mc, unicode.Nd, unicode.Pc) {
			continue
		}
		return false
	}
	return true
}

var gronReserved = map[string]bool{
	"break": true, "case": true, "catch": true, "class": true, "const": true,
	"continue": true, "debugger": true, "default": true, "delete": true,
	"do": true, "else": true, "export": true, "extends": true, "false": true,
	"finally": true, "for": true, "function": true, "if": true, "import": true,
	"in": true, "instanceof": true, "new": true, "null": true, "return": true,
	"super": true, "switch": true, "this": true, "throw": true, "true": true,
	"try": true, "typeof": true, "var": true, "void": true, "while": true,
	"with": true, "yield": true,
}
