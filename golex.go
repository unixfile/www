package main

import (
	"go/scanner"
	"go/token"
	"strings"

	"github.com/alecthomas/chroma/v2"
)

// goTokens lexes Go with the standard library's scanner, about 150 times
// faster than chroma's regular expressions. It types tokens as chroma's Go
// lexer does, so the colours stay the same.
func goTokens(code string) []chroma.Token {
	src := []byte(code)
	var s scanner.Scanner
	file := token.NewFileSet().AddFile("", -1, len(src))
	s.Init(file, src, nil, scanner.ScanComments)

	type lexeme struct {
		start, end int
		tok        token.Token
		lit        string
	}
	// scan returns the next lexeme. A name's type depends on the lexeme
	// after it, so the loop below looks one ahead.
	scan := func() (lexeme, bool) {
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				return lexeme{}, false
			}
			// The scanner inserts a semicolon at a line's end; it has no text.
			if tok == token.SEMICOLON && lit != ";" {
				continue
			}
			start := file.Offset(pos)
			text := lit
			if text == "" {
				text = tok.String()
			}
			// Comments and raw strings come without carriage returns, so
			// their end is found in the source, not from the literal's
			// length.
			end := min(start+len(text), len(src))
			if tok == token.COMMENT || tok == token.STRING && strings.HasPrefix(lit, "`") {
				end = rawEnd(code, start, tok)
			}
			return lexeme{start, end, tok, code[start:end]}, true
		}
	}

	// Go source runs about one token to three bytes, spaces included.
	out := make([]chroma.Token, 0, len(code)/3+16)
	last := 0
	for l, ok := scan(); ok; {
		next, more := scan()
		if l.start >= last {
			if l.start > last {
				out = append(out, chroma.Token{Type: chroma.Text, Value: code[last:l.start]})
			}
			paren := more && next.tok == token.LPAREN
			tight := paren && next.start == l.end
			out = append(out, chroma.Token{Type: goType(l.tok, l.lit, paren, tight), Value: l.lit})
			last = l.end
		}
		l, ok = next, more
	}
	if last < len(code) {
		out = append(out, chroma.Token{Type: chroma.Text, Value: code[last:]})
	}
	return out
}

// rawEnd finds the end of a comment or raw string that starts at start.
// It slices code rather than converting the source's bytes, which would
// copy the rest of the file for each comment.
func rawEnd(code string, start int, tok token.Token) int {
	rest := code[start:]
	var close string
	var from int
	switch {
	case tok == token.STRING:
		close, from = "`", 1
	case strings.HasPrefix(rest, "/*"):
		close, from = "*/", 2
	default:
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			return start + i
		}
		return len(code)
	}
	if i := strings.Index(rest[from:], close); i >= 0 {
		return start + from + i + len(close)
	}
	return len(code)
}

var goBuiltinFuncs = setOf("uint", "uint8", "uint16", "uint32", "uint64", "int", "int8", "int16", "int32", "int64",
	"float", "float32", "float64", "complex64", "complex128", "byte", "rune", "string", "bool", "error", "uintptr",
	"print", "println", "panic", "recover", "close", "complex", "real", "imag", "len", "cap", "append", "copy",
	"delete", "new", "make", "clear", "min", "max")

var goBuiltinTypes = setOf("uint", "uint8", "uint16", "uint32", "uint64", "int", "int8", "int16", "int32", "int64",
	"float", "float32", "float64", "complex64", "complex128", "byte", "rune", "string", "bool", "error", "uintptr", "any")

func setOf(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// goType follows chroma's Go rules: a name before ( is a function, or a
// builtin when the ( follows at once.
func goType(tok token.Token, lit string, paren, tight bool) chroma.TokenType {
	switch {
	case tok == token.COMMENT:
		if strings.HasPrefix(lit, "//") && len(lit) > 2 && !strings.ContainsRune(" \t\r\n", rune(lit[2])) {
			return chroma.CommentPreproc
		}
		return chroma.Comment
	case tok.IsKeyword():
		return chroma.Keyword
	case tok == token.IDENT:
		switch {
		case lit == "true" || lit == "false" || lit == "iota" || lit == "nil":
			return chroma.KeywordConstant
		case tight && goBuiltinFuncs[lit]:
			return chroma.NameBuiltin
		case goBuiltinTypes[lit]:
			return chroma.KeywordType
		case paren:
			return chroma.NameFunction
		}
		return chroma.NameOther
	case tok == token.CHAR:
		return chroma.LiteralStringChar
	case tok == token.STRING:
		return chroma.LiteralString
	case tok == token.FLOAT:
		return chroma.LiteralNumberFloat
	case tok == token.IMAG:
		return chroma.LiteralNumber
	case tok == token.INT:
		lower := strings.ToLower(lit)
		switch {
		case strings.HasPrefix(lower, "0x"):
			return chroma.LiteralNumberHex
		case strings.HasPrefix(lower, "0b"):
			return chroma.LiteralNumberBin
		case strings.HasPrefix(lower, "0o"), len(lit) > 1 && lit[0] == '0':
			return chroma.LiteralNumberOct
		}
		return chroma.LiteralNumberInteger
	case tok.IsOperator():
		return chroma.Operator
	}
	return chroma.Error
}
