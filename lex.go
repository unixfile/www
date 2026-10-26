package main

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// ownLexers lexes the languages whose chroma lexers are too slow for the
// page budget, with hand-written lexers that type tokens as chroma's do,
// so the colours stay the same. Chroma lexes them at about 70 µs a line.
var ownLexers = map[chroma.Lexer]func(string) []chroma.Token{
	lexers.Get("python"):     pyTokens,
	lexers.Get("javascript"): jsTokens,
	lexers.Get("bash"):       shTokens,
}

// ensureLF turns \r\n and a lone \r into \n, as chroma does to every
// source before it lexes.
func ensureLF(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// lex collects the tokens of one source as spans of it, merging
// neighbours of one type; tokens slices the source once at the end.
type lex struct {
	src   string
	spans []span
}

type span struct {
	t        chroma.TokenType
	from, to int
}

// newLex makes room for the spans up front: JavaScript runs about one
// span to four bytes, Python and shell one to six or seven.
func newLex(code string) lex {
	return lex{src: code, spans: make([]span, 0, len(code)/4+16)}
}

func (l *lex) emit(t chroma.TokenType, from, to int) {
	if to <= from {
		return
	}
	if n := len(l.spans); n > 0 && l.spans[n-1].t == t && l.spans[n-1].to == from {
		l.spans[n-1].to = to
		return
	}
	l.spans = append(l.spans, span{t, from, to})
}

func (l *lex) tokens() []chroma.Token {
	out := make([]chroma.Token, len(l.spans))
	for i, sp := range l.spans {
		out[i] = chroma.Token{Type: sp.t, Value: l.src[sp.from:sp.to]}
	}
	return out
}

// runeAt decodes the rune at i and its size.
func runeAt(s string, i int) (rune, int) {
	if i >= len(s) {
		return utf8.RuneError, 0
	}
	if c := s[i]; c < utf8.RuneSelf {
		return rune(c), 1
	}
	return utf8.DecodeRuneInString(s[i:])
}

// wordEnd returns the end of the run of runes from i that ok accepts.
func wordEnd(s string, i int, ok func(rune) bool) int {
	for i < len(s) {
		r, n := runeAt(s, i)
		if !ok(r) {
			break
		}
		i += n
	}
	return i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isSpace is \s: space, tab, newline, return, form feed and vertical tab,
// and Unicode spaces.
func isSpace(r rune) bool { return unicode.IsSpace(r) }

// isWord is \w as .NET reads it: letters, digits, marks and connectors.
func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Pc, r)
}

// lineEnd returns the index of the next newline at or after i, or the end.
func lineEnd(s string, i int) int {
	for i < len(s) && s[i] != '\n' {
		i++
	}
	return i
}
