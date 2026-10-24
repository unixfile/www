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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	var nodes []synNode
	for line := range strings.Lines(out.String()) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		if len(f) != 7 {
			return nil, errors.New("the helper printed a line of " + strconv.Itoa(len(f)) + " fields")
		}
		var v [5]int
		for i, s := range []string{f[0], f[3], f[4], f[5], f[6]} {
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
	lines := bytes.Split(src, []byte("\n"))
	esc := template.HTMLEscapeString
	var b strings.Builder
	b.WriteString("<div class=\"syntax\">\n")
	open := 0 // folds left open
	for i, n := range nodes {
		for ; open > n.depth; open-- {
			b.WriteString("</details>\n")
		}
		er := n.er
		// A node that ends at the start of a row ends on the row before.
		if n.ec == 0 && er > n.sr {
			er--
		}
		span := "L" + strconv.Itoa(n.sr+1)
		if er > n.sr {
			span += "-L" + strconv.Itoa(er+1)
		}
		var row strings.Builder
		if n.field != "" {
			row.WriteString("<span class=\"field\">" + esc(n.field) + ":</span> ")
		}
		row.WriteString("<span class=\"kind\">" + esc(n.kind) + "</span>")
		if text := excerpt(lines, n); text != "" {
			row.WriteString(" <code>" + esc(text) + "</code>")
		}
		row.WriteString(" <a href=\"" + esc(href+"#"+span) + "\">" + esc(strings.ReplaceAll(span[1:], "-L", "–")) + "</a>")
		if i+1 < len(nodes) && nodes[i+1].depth > n.depth {
			if n.depth == 0 {
				b.WriteString("<details open>")
			} else {
				b.WriteString("<details>")
			}
			b.WriteString("<summary>" + row.String() + "</summary>\n")
			open++
		} else {
			b.WriteString("<div>" + row.String() + "</div>\n")
		}
	}
	for ; open > 0; open-- {
		b.WriteString("</details>\n")
	}
	b.WriteString("</div>\n")
	h := b.String()
	syntaxCache.put(key, h, int64(len(h)))
	return h, nil
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

// codeViews are the links over a code page's top right corner: the views
// other than the one shown, then the raw file.
func codeViews(shown string) string {
	var b strings.Builder
	b.WriteString(`<div class="view">`)
	for _, v := range []struct{ name, href string }{{"flat", "?"}, {"scopes", "?scopes"}, {"tree", "?tree"}} {
		if v.name != shown {
			b.WriteString(`<a href="` + v.href + `">` + v.name + `</a>`)
		}
	}
	b.WriteString(`<a href="?raw">raw</a></div>`)
	return b.String()
}

// treeNote says why a tree page shows the flat code instead.
func treeNote(name string, err error) string {
	esc := template.HTMLEscapeString
	switch {
	case errors.Is(err, errNoHelper):
		return "<p class=\"own warn\">The syntax tree needs " + esc(progName) + "-syntax, which is not installed. <code>make syntax</code> builds it beside " + esc(progName) + ".</p>\n"
	case errors.Is(err, errNoGrammar):
		return "<p class=\"own\">No grammar for " + esc(filepath.Base(name)) + ", so no syntax tree.</p>\n"
	}
	return "<p class=\"own warn\">No syntax tree: " + esc(err.Error()) + "</p>\n"
}

// pageHref is the code page of urlPath, with no query, for a tree's line
// links.
func pageHref(urlPath string) string {
	return (&url.URL{Path: urlPath}).String()
}
