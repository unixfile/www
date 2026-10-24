package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A changed line marks only the tokens that changed, and spacing alone
// marks nothing.
func TestTokenMarks(t *testing.T) {
	lines := []string{"@@ -1 +1 @@", "-\tx := f(a, b)", "-\ty := 1", "+\tx := f(a, c)", "+\ty  :=  1"}
	marks := diffMarks("a.go", lines)
	got := map[int]string{}
	for i, rs := range marks {
		for _, r := range rs {
			got[i] += lines[i][1:][r[0]:r[1]]
		}
	}
	if len(got) != 2 || got[1] != "b" || got[3] != "c" {
		t.Errorf("marks: %q", got)
	}
	if h := markLine(lines[3], marks[3]); h != "+\tx := f(a, <mark>c</mark>)" {
		t.Errorf("line: %s", h)
	}
	// Runs that share too little are left unmarked.
	if m := diffMarks("a.go", []string{"-a b c d", "+w x y z"}); len(m) != 0 {
		t.Errorf("unrelated runs marked: %v", m)
	}
}

// realSyntax puts the helper make syntax built on PATH, or skips.
func realSyntax(t *testing.T) {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("build", progName+"-syntax"))
	if err != nil {
		t.Skip(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skip("no helper; make syntax builds it")
	}
	t.Setenv("PATH", filepath.Dir(abs)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A commit's semantic diff names what it did to each declaration:
// changed, reformatted, renamed, moved within a file and to another,
// added and removed.
func TestSemanticDiff(t *testing.T) {
	needGit(t)
	realSyntax(t)
	s, tr, dir := serveTree(t)
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.lock\n"), 0o644)
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("x.go", "package a\n\n// Kept says nothing.\nfunc Kept() int { return 1 }\n\nfunc Edit() int { return 1 }\n\nfunc Tidy() int { return 2 }\n\nfunc Old() int { return 3 }\n\nfunc Away() int { return 4 }\n\nfunc Gone() int { return 5 }\n\ntype T struct{}\n\nfunc (t *T) M() int { return 6 }\n")
	write("y.go", "package a\n")
	commitAll(t, dir, "one")
	write("x.go", "package a\n\nfunc (t *T) M() int { return 6 }\n\n// Kept says nothing.\nfunc Kept() int { return 1 }\n\nfunc Edit() int { return 10 }\n\nfunc Tidy() int {\n\treturn 2\n}\n\nfunc New() int { return 3 }\n\nfunc Fresh() int { return 7 }\n\ntype T struct{}\n")
	write("y.go", "package a\n\nfunc Away() int { return 4 }\n")
	commitAll(t, dir, "two")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(out))
	// The page asks for the semantic diff after the plain one, and renders
	// it inline once it is kept.
	w := get(s.handler(), "/?commit="+hash)
	if !strings.Contains(w.Body.String(), `<div class="semantic" data-src="`) {
		t.Errorf("no placeholder")
	}
	_, diffs, _, err := tr.gitShow(context.Background(), ".", hash)
	if err != nil {
		t.Fatal(err)
	}
	sem := tr.semanticDiff(context.Background(), ".", hash, diffs)
	got := map[string]string{}
	for _, c := range sem.changes {
		got[c.name] = strings.Join(c.what, ", ")
		if c.from != "" {
			got[c.name] += " " + c.from
		}
	}
	want := map[string]string{
		"Edit":  "changed",
		"Tidy":  "reformatted",
		"New":   "renamed Old",
		"T.M":   "moved",
		"Away":  "moved x.go",
		"Fresh": "added",
		"Gone":  "removed",
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("%s: %q, want %q", n, got[n], w)
		}
	}
	for n := range got {
		if _, ok := want[n]; !ok {
			t.Errorf("unexpected %s: %s", n, got[n])
		}
	}
	if sem.note != "" {
		t.Errorf("note: %s", sem.note)
	}
	w = get(s.handler(), "/?commit="+hash+"&semantic")
	body := w.Body.String()
	for _, want := range []string{`<span class="what">renamed</span>`, `<mark>`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in the semantic page", want)
		}
	}
	w = get(s.handler(), "/?commit="+hash)
	if strings.Contains(w.Body.String(), "data-src") || !strings.Contains(w.Body.String(), `class="decls"`) {
		t.Errorf("kept diff not inline")
	}
	if !slices.ContainsFunc(sem.changes, func(c declChange) bool { return c.name == "Gone" && c.gone }) {
		t.Errorf("Gone not marked gone")
	}
}
