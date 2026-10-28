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
// with no blank line between. Wiki links, [[name]], become links too. Raw HTML passes through; the
// Content-Security-Policy keeps its scripts from running.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Footnote, extension.DefinitionList, extension.Typographer),
	goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithInlineParsers(util.Prioritized(wikiLinkParser{}, 199))),
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
// web, when given, is where the web address serves, eg
// http://127.31.1.2:8080 or "" on the web address itself; a link to a
// repo on a forge, or a file in one, is followed by a mark that reads it
// there.
func renderMarkdown(src []byte, missing func(dest string) bool, web *string) (template.HTML, error) {
	doc := markdown.Parser().Parse(text.NewReader(src))
	markAlerts(doc, src)
	if missing != nil {
		markBroken(doc, missing)
	}
	if web != nil {
		markWeb(doc, src, *web)
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

// alertKinds are the words a quote may open with as [!WORD] to become an
// alert.
var alertKinds = map[string]bool{"note": true, "tip": true, "important": true, "warning": true, "caution": true}

// alertLabel names an alert's kind at the top of its quote.
type alertLabel struct {
	ast.BaseBlock
	kind string
}

var kindAlertLabel = ast.NewNodeKind("AlertLabel")

func (n *alertLabel) Kind() ast.NodeKind         { return kindAlertLabel }
func (n *alertLabel) IsRaw() bool                { return true }
func (n *alertLabel) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// markAlerts turns each quote whose first line is only [!NOTE], [!TIP],
// [!IMPORTANT], [!WARNING] or [!CAUTION], in any case, into an alert: the
// marker gives way to a label that names the kind.
func markAlerts(doc ast.Node, src []byte) {
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		q, ok := n.(*ast.Blockquote)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		p, ok := q.FirstChild().(*ast.Paragraph)
		if !ok || p.Lines().Len() == 0 {
			return ast.WalkContinue, nil
		}
		first := p.Lines().At(0)
		marker := strings.TrimSpace(string(first.Value(src)))
		word := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(marker, "[!"), "]"))
		if len(marker) != len(word)+3 || !alertKinds[word] {
			return ast.WalkContinue, nil
		}
		// The marker is text on the first line, and nothing else is.
		for c := p.FirstChild(); c != nil; {
			t, ok := c.(*ast.Text)
			if !ok || t.Segment.Start >= first.Stop {
				break
			}
			next := c.NextSibling()
			p.RemoveChild(p, c)
			c = next
		}
		if p.ChildCount() == 0 {
			q.RemoveChild(q, p)
		}
		q.SetAttributeString("class", []byte("alert "+word))
		q.InsertBefore(q, q.FirstChild(), &alertLabel{kind: word})
		return ast.WalkSkipChildren, nil
	})
}

// renderAlertLabel writes the label as text of www's own.
func renderAlertLabel(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		w.WriteString(`<p class="own alert">` + node.(*alertLabel).kind + "</p>\n")
	}
	return ast.WalkContinue, nil
}

// webMark follows a link to a forge with a link that reads it on the web
// address.
type webMark struct {
	ast.BaseInline
	href, key string
}

var kindWebMark = ast.NewNodeKind("WebMark")

func (n *webMark) Kind() ast.NodeKind         { return kindWebMark }
func (n *webMark) Dump(src []byte, level int) { ast.DumpHelper(n, src, level, nil, nil) }

// markWeb puts a webMark after each link, bare URLs included, that names a
// repo on a forge or a file in one.
func markWeb(doc ast.Node, src []byte, base string) {
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		var dest string
		switch l := n.(type) {
		case *ast.Link:
			dest = string(l.Destination)
		case *ast.AutoLink:
			dest = string(l.URL(src))
		default:
			return ast.WalkContinue, nil
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		u, err := url.Parse(dest)
		if err != nil {
			return ast.WalkSkipChildren, nil
		}
		if key, p := webPath(u); p != "" {
			href := base + (&url.URL{Path: p, Fragment: u.Fragment}).String()
			// The walk visits the mark next and passes it by.
			n.Parent().InsertAfter(n.Parent(), n, &webMark{href: href, key: key})
		}
		return ast.WalkSkipChildren, nil
	})
}

// renderWebMark writes the mark, the program's name, after the link.
func renderWebMark(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		m := node.(*webMark)
		esc := template.HTMLEscapeString
		w.WriteString(`<a class="web" href="` + esc(m.href) + `" title="read ` + esc(m.key) + ` in ` + progName + `">` + progName + `</a>`)
	}
	return ast.WalkContinue, nil
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
// checkboxes, and renders the nodes www adds.
type nodeRenderer struct{}

func (nodeRenderer) RegisterFuncs(r renderer.NodeRendererFuncRegisterer) {
	r.Register(ast.KindFencedCodeBlock, renderFence)
	r.Register(east.KindTaskCheckBox, renderCheckBox)
	r.Register(kindWebMark, renderWebMark)
	r.Register(kindAlertLabel, renderAlertLabel)
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
	highlight(w, lexer, code.String(), false, false)
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
// Escaping keeps file content from forging an id. Scoped lines, which
// must be numbered, draw each block of code as a box behind it.
func highlight(w io.Writer, lexer chroma.Lexer, code string, numbered, scoped bool) {
	// Every write goes to one builder: a builder of its own, or w when w is
	// one, as for a code page, so the page is never copied whole.
	b, ok := w.(*strings.Builder)
	if !ok {
		b = new(strings.Builder)
		defer func() { io.WriteString(w, b.String()) }()
	}
	lang := langName(lexer)
	lines := splitLines(tokenise(lexer, code))
	// A final newline ends the last line; it does not start another.
	if len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	var scopes []scope
	if numbered && scoped {
		scopes = findScopes(lines, lang == "python")
	}
	pre := "sourceCode " + lang
	if numbered {
		pre = "sourceCode numberSource " + lang + " numberLines"
	}
	if len(scopes) > 0 {
		pre += " scopes"
	}
	b.Grow(pageSize(code, lines, len(scopes), numbered))
	b.WriteString(`<div class="sourceCode"><pre class="` + strings.Join(strings.Fields(pre), " ") + `"><code class="` + strings.TrimSpace("sourceCode "+lang) + `">`)
	var num [20]byte
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if numbered {
			id := strconv.AppendInt(num[:0], int64(i+1), 10)
			b.WriteString(`<span id="L`)
			b.Write(id)
			b.WriteString(`"><a href="#L`)
			b.Write(id)
			b.WriteString(`"></a>`)
			n := 0
			for n < len(scopes) && scopes[n].first == i {
				n++
			}
			writeScopes(b, scopes[:n])
			scopes = scopes[n:]
		} else {
			b.WriteString("<span>")
		}
		writeLine(b, line)
		b.WriteString("</span>")
	}
	b.WriteString("</code></pre></div>")
}

// pageSize guesses the length of highlight's output, so the builder grows
// once. Each token is weighed at 16 bytes, as if about half carried a
// span of their own; on this repo's code and Debian's jquery.js,
// bash_completion, argparse.py and stdio.h the guess is 7 to 41% over.
// Slack stays in a cached page unseen, and a builder that doubles leaves
// up to 100%.
func pageSize(code string, lines [][]chroma.Token, scopes int, numbered bool) int {
	tokens := 0
	for _, line := range lines {
		tokens += len(line)
	}
	perLine := len("\n<span></span>")
	if numbered {
		perLine = len(`\n<span id="L1234"><a href="#L1234"></a></span>`)
	}
	box := len(`<i class="scope leaf shade" style="--c:12;--w:34;--d:3"></i>`)
	return len(code) + len(code)/8 + tokens*16 + len(lines)*perLine + scopes*box
}

// splitLines splits tokens into lines as chroma.SplitTokensIntoLines
// does: a token that spans lines becomes one per line, each ending in its
// newline. Chroma allocates each line on its own, which took a third of
// the page writer; here one array backs every line.
func splitLines(tokens []chroma.Token) [][]chroma.Token {
	n, rows := len(tokens), 1
	for _, tok := range tokens {
		c := strings.Count(tok.Value, "\n")
		n += c
		rows += c
	}
	all := make([]chroma.Token, 0, n)
	lines := make([][]chroma.Token, 0, rows)
	start := 0
tokens:
	for _, tok := range tokens {
		for {
			i := strings.IndexByte(tok.Value, '\n')
			if i < 0 {
				break
			}
			head := tok
			head.Value = tok.Value[:i+1]
			all = append(all, head)
			lines = append(lines, all[start:len(all):len(all)])
			start = len(all)
			// An empty tail starts nothing.
			if tok.Value = tok.Value[i+1:]; tok.Value == "" {
				continue tokens
			}
		}
		all = append(all, tok)
	}
	if start < len(all) {
		lines = append(lines, all[start:len(all):len(all)])
	}
	// Chroma drops a last line that holds only an empty token.
	if len(lines) > 0 {
		if last := lines[len(lines)-1]; len(last) == 1 && last[0].Value == "" {
			lines = lines[:len(lines)-1]
		}
	}
	return lines
}

// codeCache keeps highlighted code pages by lexer and content hash. Chroma
// lexes most languages at about 70 µs a line, so a large Python or
// JavaScript file takes far over the page budget; from the cache a page
// takes as long as a Go one.
var codeCache = newCache(16 << 20)

// highlightPage is highlight for a code page, through codeCache.
func highlightPage(lexer chroma.Lexer, b []byte, scoped bool) string {
	sum := sha256.Sum256(b)
	key := langName(lexer) + "\x00" + strconv.FormatBool(scoped) + "\x00" + string(sum[:])
	if h, ok := codeCache.get(key); ok {
		return h.(string)
	}
	var out strings.Builder
	highlight(&out, lexer, string(b), true, scoped)
	h := out.String()
	codeCache.put(key, h, int64(len(h)))
	return h
}

// writeLine writes one line's tokens, merging neighbours of one class.
func writeLine(b *strings.Builder, line []chroma.Token) {
	class := ""
	for _, tok := range line {
		v := strings.TrimSuffix(tok.Value, "\n")
		if v == "" {
			continue
		}
		if c := pandocClass(tok.Type); c != class {
			if class != "" {
				b.WriteString("</span>")
			}
			if c != "" {
				b.WriteString(`<span class="` + c + `">`)
			}
			class = c
		}
		escapeHTML(b, v)
	}
	if class != "" {
		b.WriteString("</span>")
	}
}

// escapeHTML writes s escaped as template.HTMLEscapeString escapes it,
// without making a string of its own.
func escapeHTML(b *strings.Builder, s string) {
	last := 0
	for i := 0; i < len(s); i++ {
		var esc string
		switch s[i] {
		case '"':
			esc = "&#34;"
		case '\'':
			esc = "&#39;"
		case '&':
			esc = "&amp;"
		case '<':
			esc = "&lt;"
		case '>':
			esc = "&gt;"
		case 0:
			esc = "\uFFFD"
		default:
			continue
		}
		b.WriteString(s[last:i])
		b.WriteString(esc)
		last = i + 1
	}
	b.WriteString(s[last:])
}

// blank reports whether a line holds no text.
func blank(line []chroma.Token) bool {
	for _, tok := range line {
		if tok.Value != "" {
			return false
		}
	}
	return true
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
