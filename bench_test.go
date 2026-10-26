package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// Benchmarks cover only the paths a reader can feel: search as you type,
// and the first view of a code page, before codeCache holds it. Warm pages
// sit far under pageBudget, which TestPageBudget guards. Run them with make
// bench.

// BenchmarkSearch scans a generated tree of 4,000 text files and 24 MB,
// about the size of ~/scratch. A rare word reads every file; a common one
// stops at maxLineHits.
func BenchmarkSearch(b *testing.B) {
	dir := b.TempDir()
	words := strings.Fields("the a of to and in is it that for on with as by this be not are from or at an " +
		"page tree line file code search server render lexer cache commit history listing branch share query")
	rng := rand.New(rand.NewPCG(1, 2))
	var size int64
	for i := range 4000 {
		var text strings.Builder
		for text.Len() < 6000 {
			for n := 6 + rng.IntN(7); n > 0; n-- {
				text.WriteString(words[rng.IntN(len(words))])
				text.WriteByte(' ')
			}
			text.WriteByte('\n')
		}
		p := filepath.Join(dir, fmt.Sprintf("d%02d", i/50), fmt.Sprintf("f%04d.md", i))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text.String()), 0o644); err != nil {
			b.Fatal(err)
		}
		size += int64(text.Len())
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()
	t := newTree(root, dir, "/", "bench")
	for _, q := range []struct{ name, query string }{{"rare", "zyzzyva"}, {"common", "server"}} {
		b.Run(q.name, func(b *testing.B) {
			if q.name == "rare" {
				b.SetBytes(size)
			}
			b.ReportAllocs()
			for b.Loop() {
				t.search(context.Background(), ".", q.query)
			}
		})
	}
}

// BenchmarkCodePage times a code page's first view per language: lex
// alone, then the whole page, whose difference is the page writer, and
// the page with scopes, whose difference is the scope boxes. Go and
// CSS come from this repo, CSS through chroma; Python, JavaScript and
// shell from Debian's packages, the files doc/speed.md names, skipped where
// they are not installed.
func BenchmarkCodePage(b *testing.B) {
	pythons, _ := filepath.Glob("/usr/lib/python3*/argparse.py")
	files := append([]string{"server.go", "assets/style.css",
		"/usr/share/javascript/jquery/jquery.js", "/usr/share/bash-completion/bash_completion"}, pythons...)
	for _, name := range files {
		b.Run(path.Base(name), func(b *testing.B) {
			src, err := os.ReadFile(name)
			if err != nil {
				b.Skip(err)
			}
			code := string(src)
			lines := float64(strings.Count(code, "\n"))
			lexer := codeLexer(name)
			tokenise(lexer, "x\n")
			perLine := func(b *testing.B) {
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/lines, "ns/line")
			}
			b.Run("lex", func(b *testing.B) {
				for b.Loop() {
					tokenise(lexer, code)
				}
				perLine(b)
			})
			for _, view := range []string{"page", "scopes"} {
				b.Run(view, func(b *testing.B) {
					b.ReportAllocs()
					for b.Loop() {
						var out strings.Builder
						highlight(&out, lexer, code, true, view == "scopes")
					}
					perLine(b)
				})
			}
		})
	}
}
