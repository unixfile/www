package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A search lists matching names, then matching lines, ignoring case, and
// leaves out locks, dot entries and binary files.
func TestSearch(t *testing.T) {
	s, _, dir := serveTree(t)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(dir, ".hidden", "readme.md"), []byte("readme\n"), 0o644)
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
