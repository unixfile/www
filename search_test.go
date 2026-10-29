package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// A directory's own entries come first and are marked here; deeper ones
// follow unmarked.
func TestSearchHereFirst(t *testing.T) {
	s, _, dir := serveTree(t)
	os.WriteFile(filepath.Join(dir, "a", "deep.txt"), []byte("needle\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "z.txt"), []byte("needle\n"), 0o644)
	var res found
	json.Unmarshal(get(s.handler(), "/?search=needle").Body.Bytes(), &res)
	if len(res.Lines) != 2 || res.Lines[0].Path != "z.txt" || !res.Lines[0].Here || res.Lines[1].Path != "a/deep.txt" || res.Lines[1].Here {
		t.Errorf("lines: %+v", res.Lines)
	}
	json.Unmarshal(get(s.handler(), "/a/?search=needle").Body.Bytes(), &res)
	if len(res.Lines) != 1 || res.Lines[0].Path != "a/deep.txt" || !res.Lines[0].Here {
		t.Errorf("lines below a: %+v", res.Lines)
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
