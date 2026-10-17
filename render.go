package main

import (
	"bytes"
	"html/template"
	"io"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// markdown renders docs as CommonMark with GitHub's extensions: lines join
// into paragraphs, bare URLs become links and a list may follow a line
// with no blank line between. Raw HTML passes through; the
// Content-Security-Policy keeps its scripts from running.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Footnote, extension.DefinitionList, extension.Typographer),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(
		html.WithUnsafe(),
		// The lowest priority registers last and wins, so this replaces
		// goldmark's own fenced code renderer at 1000.
		renderer.WithNodeRenderers(util.Prioritized(fenceRenderer{}, 100)),
	),
)

func renderMarkdown(src []byte) (template.HTML, error) {
	var out bytes.Buffer
	if err := markdown.Convert(src, &out); err != nil {
		return "", err
	}
	return template.HTML(out.String()), nil
}

// fenceRenderer highlights a fenced block whose language chroma knows. A
// block in any other language is plain <pre><code>.
type fenceRenderer struct{}

func (fenceRenderer) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(ast.KindFencedCodeBlock, renderFence)
}

func renderFence(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.FencedCodeBlock)
	var code strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(src))
	}
	var lexer chroma.Lexer
	if lang := n.Language(src); lang != nil {
		lexer = lexers.Get(string(lang))
	}
	if lexer == nil {
		w.WriteString("<pre><code>")
		w.WriteString(template.HTMLEscapeString(code.String()))
		w.WriteString("</code></pre>\n")
		return ast.WalkSkipChildren, nil
	}
	highlight(w, lexer, code.String(), false)
	w.WriteString("\n")
	return ast.WalkSkipChildren, nil
}

// codeLexer picks the lexer for a code page by file name, which covers
// Makefile and Dockerfile as well as extensions, and falls back to plain
// text.
func codeLexer(name string) chroma.Lexer {
	if l := lexers.Match(name); l != nil {
		return l
	}
	return lexers.Fallback
}

// highlight writes code in pandoc's markup with pandoc's short classes, so
// the stylesheet's colours apply unchanged. Chroma only lexes. Numbered
// lines carry the ids GitHub uses, so main.go#L42 lands on line 42.
// Escaping keeps file content from forging an id.
func highlight(w io.Writer, lexer chroma.Lexer, code string, numbered bool) {
	lang := langName(lexer)
	pre := "sourceCode " + lang
	if numbered {
		pre = "sourceCode numberSource " + lang + " numberLines"
	}
	io.WriteString(w, `<div class="sourceCode"><pre class="`+strings.Join(strings.Fields(pre), " ")+`"><code class="`+strings.TrimSpace("sourceCode "+lang)+`">`)
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	var tokens []chroma.Token
	if err == nil {
		tokens = it.Tokens()
	} else {
		tokens = []chroma.Token{{Type: chroma.Text, Value: code}}
	}
	lines := chroma.SplitTokensIntoLines(tokens)
	// A final newline ends the last line; it does not start another.
	if len(lines) > 0 && lineText(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for i, line := range lines {
		if i > 0 {
			io.WriteString(w, "\n")
		}
		if numbered {
			id := "L" + strconv.Itoa(i+1)
			io.WriteString(w, `<span id="`+id+`"><a href="#`+id+`"></a>`)
		} else {
			io.WriteString(w, "<span>")
		}
		writeLine(w, line)
		io.WriteString(w, "</span>")
	}
	io.WriteString(w, "</code></pre></div>")
}

// writeLine writes one line's tokens, merging neighbours of one class.
func writeLine(w io.Writer, line []chroma.Token) {
	var run strings.Builder
	class := ""
	flush := func() {
		if run.Len() == 0 {
			return
		}
		text := template.HTMLEscapeString(run.String())
		if class == "" {
			io.WriteString(w, text)
		} else {
			io.WriteString(w, `<span class="`+class+`">`+text+`</span>`)
		}
		run.Reset()
	}
	for _, tok := range line {
		v := strings.TrimSuffix(tok.Value, "\n")
		if v == "" {
			continue
		}
		if c := pandocClass(tok.Type); c != class {
			flush()
			class = c
		}
		run.WriteString(v)
	}
	flush()
}

func lineText(line []chroma.Token) string {
	var s strings.Builder
	for _, tok := range line {
		s.WriteString(tok.Value)
	}
	return s.String()
}

// langName is the lexer's class on the code element: its first alias, eg
// go or makefile. Plain text has none.
func langName(l chroma.Lexer) string {
	c := l.Config()
	if l == lexers.Fallback || c == nil {
		return ""
	}
	name := c.Name
	if len(c.Aliases) > 0 {
		name = c.Aliases[0]
	}
	name = strings.ToLower(name)
	if strings.ContainsFunc(name, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '_' || r == '-')
	}) {
		return ""
	}
	return name
}

// pandocClass maps a chroma token type to the class pandoc gives the same
// kind of token. Types without a class render as plain text.
func pandocClass(t chroma.TokenType) string {
	switch t {
	case chroma.KeywordType, chroma.NameClass:
		return "dt"
	case chroma.KeywordConstant, chroma.NameConstant:
		return "cn"
	case chroma.NameTag:
		return "kw"
	case chroma.NameAttribute:
		return "at"
	case chroma.LiteralStringChar:
		return "ch"
	case chroma.LiteralStringEscape:
		return "sc"
	case chroma.LiteralStringDoc:
		return "do"
	case chroma.LiteralNumberFloat:
		return "fl"
	case chroma.LiteralNumberHex, chroma.LiteralNumberOct, chroma.LiteralNumberBin:
		return "bn"
	case chroma.Error:
		return "er"
	}
	switch t.SubCategory() {
	case chroma.NameBuiltin:
		return "bu"
	case chroma.NameFunction:
		return "fu"
	case chroma.NameVariable:
		return "va"
	case chroma.LiteralString:
		return "st"
	case chroma.LiteralNumber:
		return "dv"
	case chroma.CommentPreproc:
		return "pp"
	}
	switch t.Category() {
	case chroma.Keyword:
		return "kw"
	case chroma.Operator, chroma.Punctuation:
		return "op"
	case chroma.Comment:
		return "co"
	}
	return ""
}
