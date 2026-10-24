package main

import (
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
	if body := get(h, "/a/b.go?log").Body.String(); !strings.Contains(body, `<a class="then" href="`+at+`a/b.go">browse</a>`) {
		t.Errorf("history links no version:\n%s", body)
	}
}
