package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A version serves the tree as it was in one commit: files, listings and
// relative links stay in it, and its page says which commit it is. Its
// address names the commit by stardate.
func TestVersion(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	first := head(t, dir, "HEAD~1")
	short := first[:7]
	stars := stardatesIn(context.Background(), dir)
	star, second, secondStar := stars.byHash[first], head(t, dir, "HEAD")[:7], stars.byHash[head(t, dir, "HEAD")]
	if !regexp.MustCompile(`^[0-9]{6}\.1$`).MatchString(star) || secondStar == "" {
		t.Fatalf("stardates %q and %q", star, secondStar)
	}
	at := "/@" + star + "/"

	if body := get(h, at+"a/b.go").Body.String(); strings.Contains(body, "package b") || !strings.Contains(body, "<details class=\"own latest\" data-timeline=\"/a/b.go?timeline\" data-here=\""+at+"a/b.go\">") {
		t.Errorf("a/b.go as first committed:\n%s", body)
	}
	if body := get(h, "/a/b.go").Body.String(); !strings.Contains(body, `package</span> b`) {
		t.Errorf("a/b.go now:\n%s", body)
	}
	listing := get(h, at+"a/").Body.String()
	if !strings.Contains(listing, " @ "+star+"</a>") || !strings.Contains(listing, `href="b.go"`) || strings.Contains(listing, "Delete lock") || strings.Contains(listing, `<li class="log">`) {
		t.Errorf("listing of a/ as of %s:\n%s", short, listing)
	}
	if body := get(h, at+"untracked/x.txt"); body.Code != 404 {
		t.Errorf("untracked file found in a version: %d", body.Code)
	}
	for _, hash := range []string{first, short} {
		if rec := get(h, "/@"+hash+"/a/b.go"); rec.Header().Get("Location") != at+"a/b.go" {
			t.Errorf("hash %s redirects to %q", hash, rec.Header().Get("Location"))
		}
	}
	if rec := get(h, "/@"+star+"9/a/b.go"); rec.Code != 404 {
		t.Errorf("unknown stardate: %d", rec.Code)
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
	body := get(h, "/a/b.go?timeline").Body.String()
	if !strings.Contains(body, `<li data-also="/@`+secondStar+`/a/b.go"><a href="/a/b.go" title="`+second) || !strings.Contains(body, "· current</span>") ||
		!strings.Contains(body, `<li><a href="`+at+`a/b.go" title="`+short) || !strings.Contains(body, `<li class="log"><a href="?log">`) || strings.Contains(body, "uncommitted") {
		t.Errorf("versions of a/b.go:\n%s", body)
	}
	// A version through a commit that left the page alone names the
	// commit that last changed it, which the menu marks.
	if body := get(h, "/@"+secondStar+"/index.md").Body.String(); !strings.Contains(body, `data-here="`+at+`index.md"`) || !strings.Contains(body, `">Add the docs</span>`) {
		t.Errorf("index.md as of %s:\n%s", second, body)
	}
	os.WriteFile(filepath.Join(dir, "a", "b.go"), []byte("package c\n"), 0o644)
	body = get(h, "/a/b.go?timeline").Body.String()
	if !strings.Contains(body, `<li><a href="/a/b.go">current</a> <span class="date">uncommitted changes</span></li>`) || !strings.Contains(body, `<li><a href="/@`+secondStar+`/a/b.go"`) {
		t.Errorf("versions of a/b.go with changes:\n%s", body)
	}
	// A commit off the main line, eg on a branch, keeps its hash.
	exec.Command("git", "-C", dir, "checkout", "-qb", "side").Run()
	commitAll(t, dir, "On a side branch")
	side := head(t, dir, "HEAD")[:7]
	if body := get(h, "/a/b.go?timeline").Body.String(); !strings.Contains(body, `<li data-also="/@`+side+`/a/b.go">`) {
		t.Errorf("versions of a/b.go on a branch:\n%s", body)
	}
	// A version runs the page's script, for keys, search and line marks,
	// but holds no stream, since it never changes.
	s.live = newLive()
	if body := get(s.handler(), at+"a/b.go").Body.String(); !strings.Contains(body, `<script data-live="">`) {
		t.Errorf("version page without the script, or with a stream:\n%s", body)
	}
}
