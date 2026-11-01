package main

import (
	"net/http"
	"strings"
	"testing"
)

func TestDirectoryRedirectsToSlash(t *testing.T) {
	s, _, _ := serveTree(t)
	rec := get(s.handler(), "/a")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/a/" {
		t.Fatalf("status %d location %q, want 301 to /a/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestListingSkipsLockAndReadme(t *testing.T) {
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/").Body.String()
	if strings.Contains(body, "8080.lock") {
		t.Error("listing shows the lockfile")
	}
	if !strings.Contains(body, `href="a/"`) || !strings.Contains(body, `href="index.md"`) {
		t.Errorf("listing lacks entries: %s", body)
	}
	if body := get(s.handler(), "/a/").Body.String(); strings.Contains(body, "Readme of a") {
		t.Errorf("README rendered below listing: %s", body)
	}
}

func TestCodePageIsHighlighted(t *testing.T) {
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/a/b.go").Body.String()
	if !strings.Contains(body, `class="sourceCode`) || !strings.Contains(body, `<span id="L3"><a href="#L3">`) {
		t.Errorf("no highlighted code with GitHub line anchors: %s", body)
	}
	if !strings.Contains(body, `<span class="kw">package</span>`) {
		t.Errorf("Go keyword not classed as pandoc does: %s", body)
	}
}

func TestBinaryGoesRaw(t *testing.T) {
	s, _, _ := serveTree(t)
	rec := get(s.handler(), "/bin.dat")
	if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/") {
		t.Errorf("binary served as %q", ct)
	}
}

func TestNoEscapeFromRoot(t *testing.T) {
	s, _, _ := serveTree(t)
	for _, p := range []string{"/../../etc/passwd", "/escape/passwd", "/escape/"} {
		if rec := get(s.handler(), p); rec.Code == http.StatusOK {
			t.Errorf("%s: status 200, want refusal", p)
		}
	}
}
