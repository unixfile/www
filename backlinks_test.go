package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A doc's backlinks name each doc that links to it, by any relative
// spelling, and follow edits, new files and deleted ones.
func TestBacklinks(t *testing.T) {
	s, _, dir := serveTree(t)
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/c.md", "[up](../index.md) [self](c.md) [again](./../index.md#x)\n")
	write("d/e.md", "[ref][r]\n\n[r]: ../index.md\n")
	write("d/f.md", "[elsewhere](../a/b.go) and index.md in prose\n")
	write(".git/g.md", "[hidden](../index.md)\n")
	links := func(name string) []string {
		t.Helper()
		w := get(s.handler(), name+"?backlinks")
		if w.Code != 200 {
			t.Fatalf("%s: status %d: %s", name, w.Code, w.Body)
		}
		var res backlinks
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range res.Links {
			out = append(out, h.Href)
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %q, want %q", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %q, want %q", got, want)
			}
		}
	}
	eq(links("/index.md"), "/a/c.md", "/d/e.md")
	eq(links("/a/b.go"), "/d/f.md", "/index.md")
	eq(links("/a/c.md"))

	// An edit is read again by its time, a deletion drops the file.
	write("d/e.md", "no link now\n")
	os.Chtimes(filepath.Join(dir, "d/e.md"), time.Now(), time.Now().Add(time.Second))
	os.Remove(filepath.Join(dir, "a/c.md"))
	write("h.md", "[i](index.md)\n")
	eq(links("/index.md"), "/h.md")
}
