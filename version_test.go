package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A version serves the tree as it was in one commit: files, listings and
// relative links stay in it, and its page says which commit it is.
func TestVersion(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	first := head(t, dir, "HEAD~1")
	short := first[:7]
	at := "/@" + short + "/"

	if body := get(h, at+"a/b.go").Body.String(); strings.Contains(body, "package b") || !strings.Contains(body, "as of <a class=\"hash\"") || !strings.Contains(body, "now</a>") {
		t.Errorf("a/b.go as first committed:\n%s", body)
	}
	if body := get(h, "/a/b.go").Body.String(); !strings.Contains(body, `package</span> b`) {
		t.Errorf("a/b.go now:\n%s", body)
	}
	listing := get(h, at+"a/").Body.String()
	if !strings.Contains(listing, "@"+short+"</a>") || !strings.Contains(listing, `href="b.go"`) || strings.Contains(listing, "Delete lock") || strings.Contains(listing, "?log") {
		t.Errorf("listing of a/ as of %s:\n%s", short, listing)
	}
	if body := get(h, at+"untracked/x.txt"); body.Code != 404 {
		t.Errorf("untracked file found in a version: %d", body.Code)
	}
	if rec := get(h, "/@"+first+"/a/b.go"); rec.Header().Get("Location") != at+"a/b.go" {
		t.Errorf("full hash redirects to %q", rec.Header().Get("Location"))
	}
	if rec := get(h, "/@0000000/a/b.go"); rec.Code != 404 {
		t.Errorf("unknown commit: %d", rec.Code)
	}
	// Older and newer walk a page's versions; the current page starts
	// the walk.
	if body := get(h, "/a/b.go").Body.String(); !strings.Contains(body, `<a href="`+at+`a/b.go" title="Add the docs">‹ older</a>`) {
		t.Errorf("current page has no older link:\n%s", body)
	}
	second := head(t, dir, "HEAD")[:7]
	if body := get(h, at+"a/b.go").Body.String(); !strings.Contains(body, `<a href="/@`+second+`/a/b.go" title="Rename the package &lt;b&gt;">newer ›</a>`) || strings.Contains(body, "older</a>") {
		t.Errorf("first version has no newer link, or an older one:\n%s", body)
	}
	if body := get(h, "/a/b.go?log").Body.String(); !strings.Contains(body, `<a class="then" href="`+at+`a/b.go">browse</a>`) {
		t.Errorf("history links no version:\n%s", body)
	}
}

// A version's older link leads to the commit before the one whose content
// it shows, even when rev itself left the page alone.
func TestVersionOlderSkipsUntouched(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	first, second := head(t, dir, "HEAD~1")[:7], head(t, dir, "HEAD")[:7]
	os.WriteFile(filepath.Join(dir, "index.md"), []byte("# Title\nchanged\n"), 0o644)
	commitAll(t, dir, "Touch the index")
	third := head(t, dir, "HEAD")[:7]
	body := get(h, "/@"+third+"/a/b.go").Body.String()
	if !strings.Contains(body, `<a href="/@`+first+`/a/b.go" title="Add the docs">‹ older</a>`) {
		t.Errorf("older from %s skips no version:\n%s", third, body)
	}
	if body := get(h, "/@"+second+"/a/b.go").Body.String(); strings.Contains(body, "newer ›") {
		t.Errorf("newer from %s, though no later commit touches a/b.go:\n%s", second, body)
	}
}

// A page whose history comes from another repo, or through a link, has no
// older or browse links, since the tree's versions would not hold it.
func TestVersionLinksOnlyForOwnHistory(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	if out, err := exec.Command("git", "init", "-q", sub).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for _, text := range []string{"one\n", "two\n"} {
		os.WriteFile(filepath.Join(sub, "x.md"), []byte(text), 0o644)
		commitAll(t, sub, "Write "+text)
	}
	os.WriteFile(filepath.Join(dir, "index.md"), []byte("# Title\nchanged\n"), 0o644)
	os.Symlink("index.md", filepath.Join(dir, "link.md"))
	commitAll(t, dir, "Change the index and link it")
	for _, p := range []string{"/sub/x.md", "/link.md"} {
		if body := get(h, p).Body.String(); !strings.Contains(body, "?log") || strings.Contains(body, "older</a>") {
			t.Errorf("%s: want a history line and no older link:\n%s", p, body)
		}
		if body := get(h, p+"?log").Body.String(); strings.Contains(body, "browse</a>") {
			t.Errorf("%s?log links versions:\n%s", p, body)
		}
	}
	if body := get(h, "/index.md").Body.String(); !strings.Contains(body, "older</a>") {
		t.Errorf("index.md lost its older link:\n%s", body)
	}
}
