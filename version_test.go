package main

import (
	"os"
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

	if body := get(h, at+"a/b.go").Body.String(); strings.Contains(body, "package b") || !strings.Contains(body, "<details class=\"own latest\" data-timeline=\"/a/b.go?timeline\" data-here=\""+at+"a/b.go\">") {
		t.Errorf("a/b.go as first committed:\n%s", body)
	}
	if body := get(h, "/a/b.go").Body.String(); !strings.Contains(body, `package</span> b`) {
		t.Errorf("a/b.go now:\n%s", body)
	}
	listing := get(h, at+"a/").Body.String()
	if !strings.Contains(listing, " @ "+short+"</a>") || !strings.Contains(listing, `href="b.go"`) || strings.Contains(listing, "Delete lock") || strings.Contains(listing, `<li class="log">`) {
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
	if body := get(h, "/a/b.go?log").Body.String(); !strings.Contains(body, `<a class="then" href="`+at+`a/b.go">browse</a>`) {
		t.Errorf("history links no version:\n%s", body)
	}
	// The menu lists every version of the page, then the history. The
	// newest leads to the current page while it matches the commit; once
	// it changes, a row of its own says so.
	second := head(t, dir, "HEAD")[:7]
	body := get(h, "/a/b.go?timeline").Body.String()
	if !strings.Contains(body, `<li data-also="/@`+second+`/a/b.go"><a href="/a/b.go" title="`+second) || !strings.Contains(body, "· current</span>") ||
		!strings.Contains(body, `<li><a href="`+at+`a/b.go" title="`+short) || !strings.Contains(body, `<li class="log"><a href="?log">`) || strings.Contains(body, "uncommitted") {
		t.Errorf("versions of a/b.go:\n%s", body)
	}
	// A version through a commit that left the page alone names the
	// commit that last changed it, which the menu marks.
	if body := get(h, "/@"+second+"/index.md").Body.String(); !strings.Contains(body, `data-here="`+at+`index.md"`) || !strings.Contains(body, `">Add the docs</span>`) {
		t.Errorf("index.md as of %s:\n%s", second, body)
	}
	os.WriteFile(filepath.Join(dir, "a", "b.go"), []byte("package c\n"), 0o644)
	body = get(h, "/a/b.go?timeline").Body.String()
	if !strings.Contains(body, `<li><a href="/a/b.go">current</a> <span class="date">uncommitted changes</span></li>`) || !strings.Contains(body, `<li><a href="/@`+second+`/a/b.go"`) {
		t.Errorf("versions of a/b.go with changes:\n%s", body)
	}
	// A version runs the page's script, for keys, search and line marks,
	// but holds no stream, since it never changes.
	s.live = newLive()
	if body := get(s.handler(), at+"a/b.go").Body.String(); !strings.Contains(body, `<script data-live="">`) {
		t.Errorf("version page without the script, or with a stream:\n%s", body)
	}
}
