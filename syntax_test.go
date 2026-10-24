package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSyntax puts a helper on PATH that prints a fixed tree for .go files
// and refuses the rest, as the real one does without a grammar.
func fakeSyntax(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in *.go) ;; *) echo \"E95: no grammar for $1\" >&2; exit 95;; esac\ncat >/dev/null\n" +
		"printf '0\\tsource_file\\t\\t0\\t0\\t3\\t0\\n1\\tfunction_declaration\\t\\t0\\t0\\t2\\t1\\n2\\tidentifier\\tname\\t0\\t5\\t0\\t6\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, progName+"-syntax"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// A tree page folds each node with children, opens the root, and gives
// each row its field, kind, text and lines.
func TestSyntaxPage(t *testing.T) {
	fakeSyntax(t)
	src := []byte("func f() {\n\treturn\n}\n")
	h, err := syntaxPage(context.Background(), "a.go", "/t/a.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<details open><summary><span class="kind">source_file</span> <code>func f() { …</code> <a href="/t/a.go#L1-L3">1–3</a></summary>`,
		`<details><summary><span class="kind">function_declaration</span> <code>func f() { …</code> <a href="/t/a.go#L1-L3">1–3</a></summary>`,
		`<div><span class="field">name:</span> <span class="kind">identifier</span> <code>f</code> <a href="/t/a.go#L1">1</a></div>`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %s in\n%s", want, h)
		}
	}
	if strings.Count(h, "<details") != strings.Count(h, "</details>") {
		t.Errorf("folds not closed:\n%s", h)
	}
}

// Without the helper, or without a grammar, a tree page says so.
func TestSyntaxMissing(t *testing.T) {
	fakeSyntax(t)
	if _, err := parseSyntax(context.Background(), "x.txt", nil); !errors.Is(err, errNoGrammar) {
		t.Errorf("no grammar: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := parseSyntax(context.Background(), "b.go", nil); !errors.Is(err, errNoHelper) {
		t.Errorf("no helper: %v", err)
	}
}

// An excerpt keeps a node's first row, its spaces folded, and marks text
// cut or continued.
func TestExcerpt(t *testing.T) {
	lines := [][]byte{[]byte("x := f(a,\t  b)"), []byte("y"), []byte(strings.Repeat("z", 80))}
	for _, tc := range []struct {
		n    synNode
		want string
	}{
		{synNode{sr: 0, sc: 5, er: 0, ec: 14}, "f(a, b)"},
		{synNode{sr: 0, sc: 0, er: 1, ec: 1}, "x := f(a, b) …"},
		{synNode{sr: 0, sc: 0, er: 1, ec: 0}, "x := f(a, b)"},
		{synNode{sr: 2, sc: 0, er: 2, ec: 80}, strings.Repeat("z", excerptLen) + " …"},
		{synNode{sr: 5}, ""},
	} {
		if got := excerpt(lines, tc.n); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.n, got, tc.want)
		}
	}
}
