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

	if body := get(h, at+"a/b.go").Body.String(); strings.Contains(body, "package b") || !strings.Contains(body, "<details class=\"own latest\" data-versions=\"/a/b.go?versions\">") || !strings.Contains(body, "now</a>") {
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
	// The menu lists every version of the page: now, each commit's
	// version, then the history.
	if body := get(h, "/a/b.go?versions").Body.String(); !strings.Contains(body, `<li><a href="/a/b.go">now</a></li>`) ||
		!strings.Contains(body, `<li><a href="`+at+`a/b.go" title="`+short) || !strings.Contains(body, `<li class="log"><a href="?log">`) {
		t.Errorf("versions of a/b.go:\n%s", body)
	}
	// A version runs the page's script, for keys, search and line marks,
	// but holds no stream, since it never changes.
	s.live = newLive()
	if body := get(s.handler(), at+"a/b.go").Body.String(); !strings.Contains(body, `<script data-live="">`) {
		t.Errorf("version page without the script, or with a stream:\n%s", body)
	}
}
