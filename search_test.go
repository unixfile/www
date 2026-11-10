package www

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// A search lists matching names, then matching lines, ignoring case, and
// leaves out locks, .git and binary files.
func TestSearch(t *testing.T) {
	s, _, dir := serveTree(t)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "readme.md"), []byte("readme\n"), 0o644)
	w := get(s.handler(), "/?search=README")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var res found
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Names) != 1 || res.Names[0].Path != "a/README.md" || res.Names[0].Href != "/a/README.md" {
		t.Errorf("names: %+v", res.Names)
	}
	if len(res.Lines) != 1 || res.Lines[0].Text != "Readme of a" || res.Lines[0].Href != "/a/README.md" {
		t.Errorf("lines: %+v", res.Lines)
	}
	w = get(s.handler(), "/?search=func")
	json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Lines) != 1 || res.Lines[0].Href != "/a/b.go#L3" || res.Lines[0].Line != 3 {
		t.Errorf("code line: %+v", res.Lines)
	}
	w = get(s.handler(), "/?search=8080")
	json.Unmarshal(w.Body.Bytes(), &res)
	if len(res.Names)+len(res.Lines) != 0 {
		t.Errorf("lock found: %+v %+v", res.Names, res.Lines)
	}
}

// The search box searches the directory a page is in, and a page with
// ?q= is the page itself, for the script to fill the box from.
func TestSearchBoxScope(t *testing.T) {
	s, _, _ := serveTree(t)
	for _, c := range []struct{ url, want string }{
		{"/", "/"},
		{"/a/", "/a/"},
		{"/a/b.go", "/a/"},
		{"/a/b.go?q=func", "/a/"},
		{"/a/missing", "/"},
	} {
		w := get(s.handler(), c.url)
		if !strings.Contains(w.Body.String(), `data-search="`+c.want+`"`) {
			t.Errorf("%s: want data-search=%q, status %d", c.url, c.want, w.Code)
		}
	}
}

// Recent pages stay within their tree: each page names its tree for the
// script, a version names the tree it is a version of, and a tree at /
// names itself, not the trees whose paths lie below it.
func TestRecentsStayInTheirTree(t *testing.T) {
	s, dir := repoTree(t)
	tr := s.list()[0]
	tr.prefix, tr.label = "/notes/", "~/notes"
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "x.md"), []byte("# x\n"), 0o644)
	addTree(t, s, other, "/", "~")
	h := s.handler()
	star := stardatesIn(context.Background(), dir).byHash[head(t, dir, "HEAD~1")]
	for _, c := range []struct{ url, want string }{
		{"/notes/", "/notes/"},
		{"/notes/a/b.go", "/notes/"},
		{"/notes@" + star + "/a/b.go", "/notes/"},
		{"/x.md", "/"},
	} {
		w := get(h, c.url)
		if !strings.Contains(w.Body.String(), `data-recents="`+c.want+`"`) {
			t.Errorf("%s: want data-recents=%q, status %d", c.url, c.want, w.Code)
		}
	}
}

// Hits come nearest first and carry their level below the searched
// directory, with paths relative to it.
func TestSearchNearestFirst(t *testing.T) {
	s, _, dir := serveTree(t)
	os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0o755)
	os.WriteFile(filepath.Join(dir, "a", "b", "c", "deep.txt"), []byte("needle\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "m"), 0o755)
	os.WriteFile(filepath.Join(dir, "m", "mid.txt"), []byte("needle\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "z.txt"), []byte("needle\n"), 0o644)
	var res found
	json.Unmarshal(get(s.handler(), "/?search=needle").Body.Bytes(), &res)
	var got []string
	for _, h := range res.Lines {
		got = append(got, fmt.Sprintf("%s@%d", h.Path, h.Depth))
	}
	if want := "z.txt@0 m/mid.txt@1 a/b/c/deep.txt@3"; strings.Join(got, " ") != want {
		t.Errorf("lines: %v, want %s", got, want)
	}
	res = found{}
	json.Unmarshal(get(s.handler(), "/a/?search=needle").Body.Bytes(), &res)
	if len(res.Lines) != 1 || res.Lines[0].Path != "b/c/deep.txt" || res.Lines[0].Depth != 2 || res.Lines[0].Href != "/a/b/c/deep.txt#L1" {
		t.Errorf("lines below a: %+v", res.Lines)
	}
	res = found{}
	json.Unmarshal(get(s.handler(), "/a/?search=deep").Body.Bytes(), &res)
	if len(res.Names) != 1 || res.Names[0].Path != "b/c/deep.txt" {
		t.Errorf("names below a: %+v", res.Names)
	}
}

// One level cannot use up the answer: a deep directory full of hits leaves
// room for each level, and the answer says it was cut.
func TestSearchLevelLimits(t *testing.T) {
	s, _, dir := serveTree(t)
	busy := filepath.Join(dir, "a", "busy")
	os.MkdirAll(busy, 0o755)
	for i := range maxNameHits + 5 {
		os.WriteFile(filepath.Join(busy, fmt.Sprintf("needle%02d.txt", i)), nil, 0o644)
	}
	os.MkdirAll(filepath.Join(dir, "z", "y", "x"), 0o755)
	os.WriteFile(filepath.Join(dir, "z", "y", "x", "needle.txt"), nil, 0o644)
	var res found
	json.Unmarshal(get(s.handler(), "/?search=needle").Body.Bytes(), &res)
	if len(res.Names) != maxNameHits+1 || !res.Cut || res.Names[len(res.Names)-1].Path != "z/y/x/needle.txt" {
		t.Errorf("%d names, cut %v, last %+v", len(res.Names), res.Cut, res.Names[len(res.Names)-1])
	}
}

// A file shows its first lines as rows and counts the rest on its last row.
func TestSearchFileLines(t *testing.T) {
	s, _, dir := serveTree(t)
	os.WriteFile(filepath.Join(dir, "many.txt"), []byte(strings.Repeat("needle\n", maxFileLines+4)), 0o644)
	var res found
	json.Unmarshal(get(s.handler(), "/?search=needle").Body.Bytes(), &res)
	if len(res.Lines) != maxFileLines || res.Lines[maxFileLines-1].More != 4 || res.Lines[0].More != 0 || res.Cut {
		t.Errorf("cut %v, lines %+v", res.Cut, res.Lines)
	}
}

// A line shows from its start while the match falls early, and otherwise
// from shortly before the match, whatever its case.
func TestAround(t *testing.T) {
	pad := strings.Repeat("x", 80)
	for _, c := range []struct {
		line, q string
		pathLen int
		want    string
	}{
		{"short needle line", "needle", 5, "short needle line"},
		{pad + " NEEDLE tail", "needle", 5, "…" + pad[len(pad)-(leadBefore-1):] + " NEEDLE tail"},
		{strings.Repeat("x", 20) + " needle", "needle", 45, "…" + strings.Repeat("x", leadBefore-1) + " needle"},
		{"é" + pad + " needle", "needle", 5, "…" + pad[len(pad)-(leadBefore-1):] + " needle"},
		{"İ" + pad + " needle", "needle", 5, "İ" + pad + " needle"},
		{"no match here", "needle", 5, "no match here"},
	} {
		if got := around(c.line, c.q, c.pathLen); got != c.want {
			t.Errorf("around(%q, %q): %q, want %q", c.line, c.q, got, c.want)
		}
	}
	if got := around(strings.Repeat("é", 300), "zz", 5); len(got) > 200+len("…") || !utf8.ValidString(got) {
		t.Errorf("long line: %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

// What git ignores comes after every other hit and marked, nested repos
// included. Searching inside an ignored directory is asking for it, so
// nothing there is marked.
func TestSearchIgnoredLast(t *testing.T) {
	s, _, dir := serveTree(t)
	gitInit := func(d string) {
		t.Helper()
		if out, err := exec.Command("git", "-C", d, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %s", out)
		}
	}
	gitInit(dir)
	nested := filepath.Join(dir, "z", "repo")
	os.MkdirAll(filepath.Join(nested, "out"), 0o755)
	gitInit(nested)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/build/\n"), 0o644)
	os.WriteFile(filepath.Join(nested, ".gitignore"), []byte("/out/\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "build"), 0o755)
	os.WriteFile(filepath.Join(dir, "build", "needle.html"), []byte("needle\n"), 0o644)
	os.WriteFile(filepath.Join(nested, "out", "needle.o"), []byte("needle\n"), 0o644)
	os.WriteFile(filepath.Join(nested, "kept.txt"), []byte("needle\n"), 0o644)
	var res found
	json.Unmarshal(get(s.handler(), "/?search=needle").Body.Bytes(), &res)
	var got []string
	for _, h := range res.Lines {
		got = append(got, fmt.Sprintf("%s %v", h.Path, h.Ignored))
	}
	if want := "z/repo/kept.txt false,build/needle.html true,z/repo/out/needle.o true"; strings.Join(got, ",") != want {
		t.Errorf("lines: %v, want %s", got, want)
	}
	res = found{}
	json.Unmarshal(get(s.handler(), "/build/?search=needle").Body.Bytes(), &res)
	if len(res.Lines) != 1 || res.Lines[0].Ignored || res.Lines[0].Path != "needle.html" || res.Lines[0].Depth != 0 {
		t.Errorf("inside build: %+v", res.Lines)
	}
}

// Each keystroke searches again, so a tree keeps git's answer for
// ignoredTTL instead of starting a process per repo every time.
func TestSearchKeepsIgnored(t *testing.T) {
	s, tr, dir := serveTree(t)
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s", out)
	}
	ignore := filepath.Join(dir, ".gitignore")
	os.WriteFile(ignore, []byte("/build/\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "build"), 0o755)
	os.WriteFile(filepath.Join(dir, "build", "needle.html"), []byte("needle\n"), 0o644)
	ignored := func() bool {
		t.Helper()
		var res found
		json.Unmarshal(get(s.handler(), "/?search=needle").Body.Bytes(), &res)
		if len(res.Lines) != 1 {
			t.Fatalf("lines: %+v", res.Lines)
		}
		return res.Lines[0].Ignored
	}
	if !ignored() {
		t.Fatal("build/needle.html not ignored")
	}
	os.WriteFile(ignore, nil, 0o644)
	if !ignored() {
		t.Error("asked git again within ignoredTTL")
	}
	tr.ignores.mu.Lock()
	for k, d := range tr.ignores.dirs {
		d.at = d.at.Add(-ignoredTTL)
		tr.ignores.dirs[k] = d
	}
	tr.ignores.mu.Unlock()
	if ignored() {
		t.Error("kept git's answer past ignoredTTL")
	}
}

// Hidden files and directories are searched, but not the stores in
// unsearched.
func TestSearchHidden(t *testing.T) {
	s, _, dir := serveTree(t)
	os.MkdirAll(filepath.Join(dir, ".github"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".venv"), 0o755)
	for _, p := range []string{".gitignore", ".github/ci.yml", ".venv/x.py"} {
		os.WriteFile(filepath.Join(dir, p), []byte("needle\n"), 0o644)
	}
	var res found
	json.Unmarshal(get(s.handler(), "/?search=needle").Body.Bytes(), &res)
	var paths []string
	for _, h := range res.Lines {
		paths = append(paths, h.Path)
	}
	if strings.Join(paths, " ") != ".gitignore .github/ci.yml" {
		t.Errorf("lines: %v", paths)
	}
}

// A query that is a forge's URL, with or without its scheme, comes back as
// a link to the web address, and only where a web address is served.
func TestSearchWeb(t *testing.T) {
	s, _, _ := serveTree(t)
	var res found
	w := get(s.handler(), "/?search="+url.QueryEscape("github.com/junegunn/fzf"))
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Web != nil {
		t.Errorf("web hit without a web address: %+v", res.Web)
	}
	s.webURL = "http://127.31.1.2:8080"
	for q, want := range map[string]string{
		"github.com/junegunn/fzf":                               "http://127.31.1.2:8080/github.com/junegunn/fzf/",
		" https://github.com/junegunn/fzf/blob/master/main.go ": "http://127.31.1.2:8080/github.com/junegunn/fzf/main.go",
	} {
		res = found{}
		w = get(s.handler(), "/?search="+url.QueryEscape(q))
		json.Unmarshal(w.Body.Bytes(), &res)
		if res.Web == nil || res.Web.Href != want {
			t.Errorf("%q: %+v, want %s", q, res.Web, want)
		}
	}
	res = found{}
	w = get(s.handler(), "/?search=readme")
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.Web != nil {
		t.Errorf("web hit for a word: %+v", res.Web)
	}
}
