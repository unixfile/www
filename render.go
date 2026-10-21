package main

import (
	"bytes"
	"crypto/sha256"
	"html/template"
	"io"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
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
		// goldmark's fenced code renderer at 1000 and its task list
		// renderer at 500.
		renderer.WithNodeRenderers(util.Prioritized(nodeRenderer{}, 100)),
	),
)

// renderMarkdown renders a doc. missing, when given, reports whether a
// relative link's path, unescaped and without query or fragment, names
// nothing; such a link gets the class broken and a title that says so.
func renderMarkdown(src []byte, missing func(dest string) bool) (template.HTML, error) {
	doc := markdown.Parser().Parse(text.NewReader(src))
	if missing != nil {
		markBroken(doc, missing)
	}
	var out bytes.Buffer
	if err := markdown.Renderer().Render(&out, src, doc); err != nil {
		return "", err
	}
	return template.HTML(out.String()), nil
}

// markBroken marks each link whose relative target is missing.
func markBroken(doc ast.Node, missing func(string) bool) {
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		l, ok := n.(*ast.Link)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		dest := relativePath(string(l.Destination))
		if dest == "" || !missing(dest) {
			return ast.WalkContinue, nil
		}
		l.SetAttributeString("class", []byte("broken"))
		if len(l.Title) == 0 {
			l.SetAttributeString("title", []byte("missing: "+dest))
		}
		return ast.WalkContinue, nil
	})
}

// relativePath is the path a relative link names, unescaped, or "" for a
// link with a scheme or host, an absolute path or only a fragment or
// query.
func relativePath(dest string) string {
	u, err := url.Parse(dest)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || strings.HasPrefix(u.Path, "/") {
		return ""
	}
	return u.Path
}

// nodeRenderer replaces goldmark's rendering of fenced code and task list
// checkboxes.
type nodeRenderer struct{}

func (nodeRenderer) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(ast.KindFencedCodeBlock, renderFence)
	r.Register(east.KindTaskCheckBox, renderCheckBox)
}

// renderCheckBox leaves out goldmark's disabled attribute, which greys the
// box out, so a task list looks as it did under pandoc.
func renderCheckBox(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	if node.(*east.TaskCheckBox).IsChecked {
		w.WriteString(`<input type="checkbox" checked=""> `)
	} else {
		w.WriteString(`<input type="checkbox"> `)
	}
	return ast.WalkContinue, nil
}

// renderFence draws a Graphviz block through dot and highlights a fenced
// block whose language chroma knows. A graph dot cannot draw shows as code
// under a line that says why; a block in any other language is plain
// <pre><code>.
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
		if isGraph(string(lang)) {
			svg, err := drawGraph(code.String())
			if err == nil {
				w.WriteString(string(svg) + "\n")
				return ast.WalkSkipChildren, nil
			}
			w.WriteString(`<p class="own warn">` + template.HTMLEscapeString(err.Error()) + "</p>\n")
		}
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
// text. lexers.Match tries every glob of every lexer, about 3 ms, so the
// answer is kept per base name.
func codeLexer(name string) chroma.Lexer {
	base := path.Base(name)
	if l, ok := lexerByName.Load(base); ok {
		return l.(chroma.Lexer)
	}
	l := lexers.Match(base)
	if l == nil {
		l = lexers.Fallback
	}
	lexerByName.Store(base, l)
	return l
}

var lexerByName sync.Map

// tokenise lexes Go with the standard library's scanner, Python,
// JavaScript and shell with the program's own lexers, and everything else
// with chroma.
func tokenise(lexer chroma.Lexer, code string) []chroma.Token {
	if lexer == lexers.Go {
		return goTokens(code)
	}
	if own := ownLexers[lexer]; own != nil {
		return own(ensureLF(code))
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return []chroma.Token{{Type: chroma.Text, Value: code}}
	}
	return it.Tokens()
}

// warmLexers compiles the lexers for common files in the background, so
// the first code page a server shows does not wait for chroma to build its
// regular expressions.
func warmLexers() {
	for _, name := range []string{"x.sh", "Makefile", "x.py", "x.js", "x.ts", "x.json", "x.yaml", "x.toml",
		"x.html", "x.css", "x.c", "x.rs", "Dockerfile", "x.md"} {
		tokenise(codeLexer(name), "x\n")
	}
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
	lines := chroma.SplitTokensIntoLines(tokenise(lexer, code))
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

// codeCache keeps highlighted code pages by lexer and content hash. Chroma
// lexes most languages at about 70 µs a line, so a large Python or
// JavaScript file takes far over the page budget; from the cache a page
// takes as long as a Go one.
var codeCache = newCache(16 << 20)

// highlightPage is highlight for a code page, through codeCache.
func highlightPage(lexer chroma.Lexer, b []byte) string {
	sum := sha256.Sum256(b)
	key := langName(lexer) + "\x00" + string(sum[:])
	if h, ok := codeCache.get(key); ok {
		return h.(string)
	}
	var out strings.Builder
	highlight(&out, lexer, string(b), true)
	h := out.String()
	codeCache.put(key, h, int64(len(h)))
	return h
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
