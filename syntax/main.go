// Command syntax is the program's tree-sitter helper. It parses the source
// on stdin by the grammar its file name picks and prints the named nodes
// of the syntax tree in preorder, one per line, tab-separated: depth,
// kind, field name, start row, start column, end row, end column. Rows
// and columns count from 0, columns in bytes. A missing node, which the
// parser inserted to recover from an error, prints its kind after
// "MISSING ".
//
// It is a program of its own because tree-sitter is C, and cgo would make
// the main binary need a C compiler to build and a C library to run. The
// main binary runs it for each tree page and does without it when it is
// absent. The binary takes its name from the Makefile, eg www-syntax.
//
// Usage: <name>-syntax FILE < source
package main

/*
#include <stddef.h>

void ts_set_allocator(void *(*)(size_t), void *(*)(size_t, size_t), void *(*)(void *, size_t), void (*)(void *));
*/
import "C"

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	bash "github.com/tree-sitter/tree-sitter-bash/bindings/go"
	csharp "github.com/tree-sitter/tree-sitter-c-sharp/bindings/go"
	c "github.com/tree-sitter/tree-sitter-c/bindings/go"
	cpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	css "github.com/tree-sitter/tree-sitter-css/bindings/go"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	html "github.com/tree-sitter/tree-sitter-html/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	json "github.com/tree-sitter/tree-sitter-json/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// grammars maps a file extension to its grammar. A .h file is read as C,
// since C++ headers mostly parse as C and C headers often fail as C++.
var grammars = map[string]func() unsafe.Pointer{
	".go":   golang.Language,
	".py":   python.Language,
	".pyi":  python.Language,
	".js":   javascript.Language,
	".mjs":  javascript.Language,
	".cjs":  javascript.Language,
	".jsx":  javascript.Language,
	".ts":   typescript.LanguageTypescript,
	".mts":  typescript.LanguageTypescript,
	".cts":  typescript.LanguageTypescript,
	".tsx":  typescript.LanguageTSX,
	".rs":   rust.Language,
	".c":    c.Language,
	".h":    c.Language,
	".cc":   cpp.Language,
	".cpp":  cpp.Language,
	".cxx":  cpp.Language,
	".hh":   cpp.Language,
	".hpp":  cpp.Language,
	".hxx":  cpp.Language,
	".sh":   bash.Language,
	".bash": bash.Language,
	".java": java.Language,
	".cs":   csharp.Language,
	".json": json.Language,
	".html": html.Language,
	".htm":  html.Language,
	".css":  css.Language,
	".rb":   ruby.Language,
}

// The Go binding hands tree-sitter allocators that call back into Go,
// only to call C's malloc, so each allocation crosses cgo twice; that
// took a quarter of a parse. Null puts back tree-sitter's own, which call
// malloc directly. The binding's init has run by now, and memory from
// either is freed by the same free.
func init() {
	C.ts_set_allocator(nil, nil, nil, nil)
}

func main() {
	if len(os.Args) != 2 {
		os.Exit(fail(syscall.EINVAL, "usage: "+filepath.Base(os.Args[0])+" FILE < source"))
	}
	lang, ok := grammars[strings.ToLower(filepath.Ext(os.Args[1]))]
	if !ok {
		os.Exit(fail(syscall.EOPNOTSUPP, "no grammar for "+filepath.Base(os.Args[1])))
	}
	src, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(fail(syscall.EIO, "cannot read the source: "+err.Error()))
	}
	p := sitter.NewParser()
	defer p.Close()
	if err := p.SetLanguage(sitter.NewLanguage(lang())); err != nil {
		os.Exit(fail(syscall.EINVAL, "cannot load the grammar: "+err.Error()))
	}
	tree := p.Parse(src, nil)
	if tree == nil {
		os.Exit(fail(syscall.EIO, "the parser gave no tree"))
	}
	defer tree.Close()
	w := bufio.NewWriter(os.Stdout)
	write(w, tree)
	if err := w.Flush(); err != nil {
		os.Exit(fail(syscall.EIO, "cannot write the tree: "+err.Error()))
	}
}

// write prints the named nodes in preorder. Anonymous nodes, eg
// punctuation and keywords, are left out, but their named descendants
// keep their place under the nearest named ancestor. Each call into
// tree-sitter crosses cgo, so a node costs as few as the line needs, and
// kinds and field names are looked up once per id.
func write(w *bufio.Writer, tree *sitter.Tree) {
	lang := tree.Language()
	kinds := map[uint16]string{}
	fields := map[uint16]string{}
	cur := tree.Walk()
	defer cur.Close()
	depth := 0
	named := []int{} // the depths of the named nodes above the cursor
	for {
		n := cur.Node()
		missing := n.IsMissing()
		shown := missing || n.IsNamed()
		if shown {
			id := n.KindId()
			kind, ok := kinds[id]
			if !ok {
				kind = lang.NodeKindForId(id)
				kinds[id] = kind
			}
			if missing {
				kind = "MISSING " + kind
			}
			fid := cur.FieldId()
			field, ok := fields[fid]
			if !ok {
				field = lang.FieldNameForId(fid)
				fields[fid] = field
			}
			s, e := n.StartPosition(), n.EndPosition()
			w.WriteString(strconv.Itoa(len(named)))
			for _, f := range []string{kind, field} {
				w.WriteByte('\t')
				w.WriteString(f)
			}
			for _, v := range []uint{s.Row, s.Column, e.Row, e.Column} {
				w.WriteByte('\t')
				w.WriteString(strconv.FormatUint(uint64(v), 10))
			}
			w.WriteByte('\n')
		}
		if cur.GotoFirstChild() {
			if shown {
				named = append(named, depth)
			}
			depth++
			continue
		}
		for !cur.GotoNextSibling() {
			if !cur.GotoParent() {
				return
			}
			depth--
			if len(named) > 0 && named[len(named)-1] == depth {
				named = named[:len(named)-1]
			}
		}
	}
}

// fail prints an error as E{code}: message and returns the code.
func fail(code syscall.Errno, msg string) int {
	fmt.Fprintf(os.Stderr, "E%d: %s\n", int(code), msg)
	return int(code)
}
