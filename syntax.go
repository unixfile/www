package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"html/template"
	"io"
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
// grammar for, and hands row each node as the helper prints it. The
// helper prints while it walks the tree, so rows render in the meantime.
func parseSyntax(ctx context.Context, name string, src []byte, row func(synNode)) error {
	helper, err := syntaxHelper()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, syntaxWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, filepath.Base(name))
	cmd.Stdin = bytes.NewReader(src)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := readNodes(out, row); err != nil {
		// Cancelling kills the helper, so Wait returns.
		cancel()
		cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && syscall.Errno(ee.ExitCode()) == syscall.EOPNOTSUPP {
			return errNoGrammar
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// syntaxNodes is every node parseSyntax hands on, in order.
func syntaxNodes(ctx context.Context, name string, src []byte) ([]synNode, error) {
	var nodes []synNode
	err := parseSyntax(ctx, name, src, func(n synNode) { nodes = append(nodes, n) })
	return nodes, err
}

// readNodes reads the helper's output from r: a node a line, with its
// depth, kind, field and range as tab-separated fields. A large file
// prints tens of thousands of lines, so it cuts fields in place and keeps
// one copy of each kind and field name, which the grammar holds to a few
// hundred.
func readNodes(r io.Reader, row func(synNode)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	names := map[string]string{}
	intern := func(b []byte) string {
		if s, ok := names[string(b)]; ok {
			return s
		}
		s := string(b)
		names[s] = s
		return s
	}
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return errors.New("the helper printed a line over 64 KiB")
		}
		if err != nil && err != io.EOF {
			return err
		}
		if len(line) == 0 {
			return nil
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		if k := bytes.Count(line, []byte("\t")); k != 6 {
			return errors.New("the helper printed a line of " + strconv.Itoa(k+1) + " fields")
		}
		var f [7][]byte
		for i := range 6 {
			f[i], line, _ = bytes.Cut(line, []byte("\t"))
		}
		f[6] = line
		var v [5]int
		for i, b := range [5][]byte{f[0], f[3], f[4], f[5], f[6]} {
			var ok bool
			if v[i], ok = atoi(b); !ok {
				return errors.New("the helper printed a bad number: " + string(b))
			}
		}
		row(synNode{depth: v[0], kind: intern(f[1]), field: intern(f[2]), sr: v[1], sc: v[2], er: v[3], ec: v[4]})
	}
}

// atoi reads b as a number of up to 18 digits, the most an int64 always
// holds, with no sign. strconv.Atoi would need a string, which costs an
// allocation a field.
func atoi(b []byte) (int, bool) {
	if len(b) == 0 || len(b) > 18 {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
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
	rows := newSynRows(href, src)
	if err := parseSyntax(ctx, name, src, rows.add); err != nil {
		return "", err
	}
	h := rows.done()
	// The rows keep the builder's whole buffer, which the guess in
	// newSynRows may make larger than h.
	syntaxCache.put(key, h, int64(rows.b.Cap()))
	return h, nil
}

// synRows writes a syntax tree's rows as its nodes come. It holds each
// node back until the next one shows whether it folds.
type synRows struct {
	b     strings.Builder
	lines [][]byte
	href  string
	open  int // folds left open
	held  synNode
	any   bool // whether a node is held
}

// newSynRows starts the rows of src's tree, with line links to href.
func newSynRows(href string, src []byte) *synRows {
	w := &synRows{lines: bytes.Split(src, []byte("\n")), href: href}
	// A row is about 120 bytes, and a line of a large file gives about
	// four, so the builder seldom grows where it would cost the most.
	w.b.Grow(len(w.lines) * 512)
	w.b.WriteString("<div class=\"syntax\">\n")
	return w
}

func (w *synRows) add(n synNode) {
	if w.any {
		w.row(w.held, n.depth > w.held.depth)
	}
	w.held, w.any = n, true
}

// done writes the last row, closes the folds and returns the rows.
func (w *synRows) done() string {
	if w.any {
		w.row(w.held, false)
	}
	for ; w.open > 0; w.open-- {
		w.b.WriteString("</details>\n")
	}
	w.b.WriteString("</div>\n")
	return w.b.String()
}

// row writes n, as a fold's summary when folds says the next node is its
// child.
func (w *synRows) row(n synNode, folds bool) {
	b := &w.b
	for ; w.open > n.depth; w.open-- {
		b.WriteString("</details>\n")
	}
	switch {
	case folds && n.depth == 0:
		b.WriteString("<details open><summary>")
	case folds:
		b.WriteString("<details><summary>")
	default:
		b.WriteString("<div>")
	}
	// The row: its field in its parent, its kind, its text, and its lines
	// as a link. Each row repeats these tags, so they are short.
	if n.field != "" {
		b.WriteString("<i>")
		escapeHTML(b, n.field)
		b.WriteString(":</i> ")
	}
	b.WriteString("<b>")
	escapeHTML(b, n.kind)
	b.WriteString("</b>")
	writeExcerpt(b, w.lines, n)
	er := n.er
	// A node that ends at the start of a row ends on the row before.
	if n.ec == 0 && er > n.sr {
		er--
	}
	var num, num2 [20]byte
	first := strconv.AppendInt(num[:0], int64(n.sr+1), 10)
	var last []byte
	if er > n.sr {
		last = strconv.AppendInt(num2[:0], int64(er+1), 10)
	}
	b.WriteString(` <a href="`)
	escapeHTML(b, w.href)
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
		w.open++
	} else {
		b.WriteString("</div>\n")
	}
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
