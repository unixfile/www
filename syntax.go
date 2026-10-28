package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"html/template"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// A code page's syntax tree comes from a helper, <name>-syntax, built
// from syntax/ with tree-sitter. Tree-sitter is C, so the helper keeps cgo
// out of the main binary, which runs without it and says so. The helper
// lies beside the binary, as make builds it, or on PATH.

// synNode is one named node of a syntax tree, as the helper prints it.
// Rows and columns count from 0, columns in bytes.
type synNode struct {
	depth          int
	kind, field    string
	sr, sc, er, ec int
}

// errNoHelper says the helper is not installed; errNoGrammar that it has
// no grammar for the file.
var (
	errNoHelper  = errors.New("no helper")
	errNoGrammar = errors.New("no grammar")
)

// syntaxWait bounds a parse. The helper parses a large file in tens of
// milliseconds.
const syntaxWait = 5 * time.Second

func syntaxHelper() (string, error) {
	name := progName + "-syntax"
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", errNoHelper
	}
	return p, nil
}

// parseSyntax runs the helper on src, which name's extension picks the
// grammar for.
func parseSyntax(ctx context.Context, name string, src []byte) ([]synNode, error) {
	helper, err := syntaxHelper()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, syntaxWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, filepath.Base(name))
	cmd.Stdin = bytes.NewReader(src)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && syscall.Errno(ee.ExitCode()) == syscall.EOPNOTSUPP {
			return nil, errNoGrammar
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return readNodes(out.String())
}

// readNodes reads the helper's output: a node a line, with its depth,
// kind, field and range as tab-separated fields. A large file prints tens
// of thousands of lines, so it cuts fields in place, with no slice per
// line.
func readNodes(out string) ([]synNode, error) {
	nodes := make([]synNode, 0, strings.Count(out, "\n"))
	for line := range strings.Lines(out) {
		line = strings.TrimSuffix(line, "\n")
		if k := strings.Count(line, "\t"); k != 6 {
			return nil, errors.New("the helper printed a line of " + strconv.Itoa(k+1) + " fields")
		}
		var f [7]string
		for i := range 6 {
			f[i], line, _ = strings.Cut(line, "\t")
		}
		f[6] = line
		var v [5]int
		for i, s := range [5]string{f[0], f[3], f[4], f[5], f[6]} {
			var err error
			if v[i], err = strconv.Atoi(s); err != nil {
				return nil, errors.New("the helper printed a bad number: " + s)
			}
		}
		nodes = append(nodes, synNode{depth: v[0], kind: f[1], field: f[2], sr: v[1], sc: v[2], er: v[3], ec: v[4]})
	}
	return nodes, nil
}

// syntaxCache keeps rendered trees by file name and content hash, as
// codeCache keeps code pages, since a parse costs a process start.
var syntaxCache = newCache(8 << 20)

// syntaxPage renders the syntax tree of src as nested folds: a node with
// children folds them, and the root starts open, so the top-level
// declarations show. Each row names the node's kind, its field in its
// parent, its text, cut to one line, and links its lines on the code page
// at href.
func syntaxPage(ctx context.Context, name, href string, src []byte) (string, error) {
	sum := sha256.Sum256(src)
	key := filepath.Ext(name) + "\x00" + href + "\x00" + string(sum[:])
	if h, ok := syntaxCache.get(key); ok {
		return h.(string), nil
	}
	nodes, err := parseSyntax(ctx, name, src)
	if err != nil {
		return "", err
	}
	h := renderSyntax(nodes, href, src)
	syntaxCache.put(key, h, int64(len(h)))
	return h, nil
}

// renderSyntax writes the rows of nodes, parsed from src, with line links
// to href.
func renderSyntax(nodes []synNode, href string, src []byte) string {
	lines := bytes.Split(src, []byte("\n"))
	var b strings.Builder
	// About 120 bytes a row, so the builder grows about once.
	b.Grow(len(nodes) * 128)
	b.WriteString("<div class=\"syntax\">\n")
	var num, num2 [20]byte
	open := 0 // folds left open
	for i, n := range nodes {
		for ; open > n.depth; open-- {
			b.WriteString("</details>\n")
		}
		folds := i+1 < len(nodes) && nodes[i+1].depth > n.depth
		switch {
		case folds && n.depth == 0:
			b.WriteString("<details open><summary>")
		case folds:
			b.WriteString("<details><summary>")
		default:
			b.WriteString("<div>")
		}
		// The row: its field in its parent, its kind, its text, and its
		// lines as a link. Each row repeats these tags, so they are short.
		if n.field != "" {
			b.WriteString("<i>")
			escapeHTML(&b, n.field)
			b.WriteString(":</i> ")
		}
		b.WriteString("<b>")
		escapeHTML(&b, n.kind)
		b.WriteString("</b>")
		writeExcerpt(&b, lines, n)
		er := n.er
		// A node that ends at the start of a row ends on the row before.
		if n.ec == 0 && er > n.sr {
			er--
		}
		first := strconv.AppendInt(num[:0], int64(n.sr+1), 10)
		var last []byte
		if er > n.sr {
			last = strconv.AppendInt(num2[:0], int64(er+1), 10)
		}
		b.WriteString(` <a href="`)
		escapeHTML(&b, href)
		b.WriteString("#L")
		b.Write(first)
		if last != nil {
			b.WriteString("-L")
			b.Write(last)
		}
		b.WriteString(`">`)
		b.Write(first)
		if last != nil {
			b.WriteString("–")
			b.Write(last)
		}
		b.WriteString("</a>")
		if folds {
			b.WriteString("</summary>\n")
			open++
		} else {
			b.WriteString("</div>\n")
		}
	}
	for ; open > 0; open-- {
		b.WriteString("</details>\n")
	}
	b.WriteString("</div>\n")
	return b.String()
}

// excerptLen is how many characters of a node's text a row shows.
const excerptLen = 60

// excerpt is n's text on its first row, with runs of spaces and tabs as
// one space, cut to excerptLen characters; a node that goes on past its
// first row ends in an ellipsis.
func excerpt(lines [][]byte, n synNode) string {
	if n.sr >= len(lines) {
		return ""
	}
	line := lines[n.sr]
	start, end := min(n.sc, len(line)), len(line)
	more := n.er > n.sr && !(n.er == n.sr+1 && n.ec == 0)
	if n.er == n.sr {
		end = min(n.ec, len(line))
	}
	text := strings.Join(strings.Fields(string(line[start:end])), " ")
	if utf8.RuneCountInString(text) > excerptLen {
		text = string([]rune(text)[:excerptLen])
		more = true
	}
	if more && text != "" {
		text += " …"
	}
	return text
}

// writeExcerpt writes n's excerpt, escaped, in a code element after a
// space, and nothing when it is empty. It does excerpt's work in one pass
// with no allocation, since a large file has tens of thousands of rows. A
// row that is not valid UTF-8 goes through excerpt, whose cut turns
// invalid bytes into U+FFFD.
func writeExcerpt(b *strings.Builder, lines [][]byte, n synNode) {
	if n.sr >= len(lines) {
		return
	}
	line := lines[n.sr]
	start, end := min(n.sc, len(line)), len(line)
	if n.er == n.sr {
		end = min(n.ec, len(line))
	}
	text := line[start:end]
	if !utf8.Valid(text) {
		if s := excerpt(lines, n); s != "" {
			b.WriteString(" <code>")
			escapeHTML(b, s)
			b.WriteString("</code>")
		}
		return
	}
	more := n.er > n.sr && !(n.er == n.sr+1 && n.ec == 0)
	runes, space := 0, false
	for i := 0; i < len(text); {
		r, size := rune(text[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRune(text[i:])
		}
		if unicode.IsSpace(r) {
			space = runes > 0
			i += size
			continue
		}
		// The space that joins two words counts toward the cut.
		if space {
			if runes == excerptLen {
				more = true
				break
			}
			b.WriteByte(' ')
			runes++
			space = false
		}
		if runes == excerptLen {
			more = true
			break
		}
		if runes == 0 {
			b.WriteString(" <code>")
		}
		switch r {
		case '"':
			b.WriteString("&#34;")
		case '\'':
			b.WriteString("&#39;")
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case 0:
			b.WriteString("\uFFFD")
		default:
			b.Write(text[i : i+size])
		}
		runes++
		i += size
	}
	if runes == 0 {
		return
	}
	if more {
		b.WriteString(" …")
	}
	b.WriteString("</code>")
}

// codeViews are the links over a code page's top right corner: every
// view, the one shown as plain text, then the raw file. A JSON file also
// has the gron view, at the row's left end.
func codeViews(name, shown string) string {
	var b strings.Builder
	b.WriteString(`<div class="view">`)
	// Every view keeps its place, so a click never leaves another link
	// under the pointer; the one shown is plain text.
	view := func(name, href, class string) {
		if name == shown {
			b.WriteString(`<span` + class + `>` + name + `</span>`)
		} else {
			b.WriteString(`<a` + class + ` href="` + href + `">` + name + `</a>`)
		}
	}
	if isJSON(name) {
		view("gron", "?gron", ` class="left"`)
	}
	for _, v := range []struct{ name, href string }{{"flat", "?"}, {"scopes", "?scopes"}, {"tree", "?tree"}} {
		view(v.name, v.href, "")
	}
	b.WriteString(`<a href="?raw">raw</a></div>`)
	return b.String()
}

// treeNote says why a tree page shows the flat code instead.
func treeNote(name string, err error) string {
	esc := template.HTMLEscapeString
	switch {
	case errors.Is(err, errNoHelper):
		helper := esc(progName) + "-syntax"
		return "<p class=\"own warn\">The syntax tree needs " + helper + ", which is not installed. The package " + helper + " installs it, and <code>make syntax</code> builds it beside a build of " + esc(progName) + ".</p>\n"
	case errors.Is(err, errNoGrammar):
		return "<p class=\"own\">No grammar for " + esc(filepath.Base(name)) + ", so no syntax tree.</p>\n"
	}
	return "<p class=\"own warn\">No syntax tree: " + esc(err.Error()) + "</p>\n"
}

// pageHref is the code page of urlPath for a tree's line links: its base
// name, which from the tree page, the same path with ?tree, leads to the
// flat page. Every row holds the link, so a full path would add its
// length to each.
func pageHref(urlPath string) string {
	return (&url.URL{Path: path.Base(urlPath)}).String()
}
