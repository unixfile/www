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
// The parse and the walk run in C, in walk.c, in one call; the Go binding
// supplies tree-sitter's library and the grammars.
//
// Usage: <name>-syntax FILE < source
package main

/*
#include <stddef.h>
#include <stdint.h>

void ts_set_allocator(void *(*)(size_t), void *(*)(size_t, size_t), void *(*)(void *, size_t), void (*)(void *));
int syntax_tree(const void *lang, const char *src, uint32_t len);
*/
import "C"

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	// The binding builds tree-sitter's C library, which walk.c calls.
	_ "github.com/tree-sitter/go-tree-sitter"
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
	if len(src) > math.MaxUint32 {
		os.Exit(fail(syscall.EFBIG, "the source is over 4 GiB"))
	}
	// The NUL gives C a valid pointer for an empty source.
	buf := append(src, 0)
	switch code := syscall.Errno(C.syntax_tree(lang(), (*C.char)(unsafe.Pointer(&buf[0])), C.uint32_t(len(src)))); code {
	case 0:
	case syscall.EINVAL:
		os.Exit(fail(code, "cannot load the grammar: its ABI version does not match tree-sitter's"))
	case syscall.EIO:
		os.Exit(fail(code, "the parser gave no tree"))
	case syscall.ENOMEM:
		os.Exit(fail(code, "out of memory"))
	default:
		os.Exit(fail(code, "cannot write the tree: "+code.Error()))
	}
}

// fail prints an error as E{code}: message and returns the code.
func fail(code syscall.Errno, msg string) int {
	fmt.Fprintf(os.Stderr, "E%d: %s\n", int(code), msg)
	return int(code)
}
