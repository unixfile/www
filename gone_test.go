package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A missing path's 404 says where git saw it go: deleted, moved, or
// deleted only in the work tree, with a link to its last version.
func TestNotFoundSaysWhereItWent(t *testing.T) {
	s, dir := repoTree(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("rm", "-q", "index.md")
	git("mv", "bin.dat", "data.bin")
	commitAll(t, dir, "Tidy the top")
	git("mv", "a", "lib")
	commitAll(t, dir, "Move a to lib")
	os.Remove(filepath.Join(dir, "lib", "page.html"))
	h := s.handler()
	for p, want := range map[string][]string{
		"/index.md":       {"Deleted in <a href=\"/?commit=", "Tidy the top", "Last version: <a href=\"/@", "/index.md\">", "Add the docs"},
		"/bin.dat":        {`Moved to <a href="/data.bin">data.bin</a> in`, "Tidy the top", "/bin.dat\">"},
		"/a/b.go":         {`Moved to <a href="/lib/b.go">lib/b.go</a> in`, "Move a to lib", "/a/b.go\">", "Rename the package"},
		"/a/":             {`Moved to <a href="/lib">lib</a> in`, "/a/\">"},
		"/lib/page.html":  {"Deleted since the last commit", "Last version: <a href=\"/@", "/lib/page.html\">", "Move a to lib"},
		"/untracked/y.md": {},
	} {
		rec := get(h, p)
		body := rec.Body.String()
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: lacks %q:\n%s", p, w, body)
			}
		}
		if len(want) == 0 && strings.Contains(body, "Last version") {
			t.Errorf("%s: a path git never held has a last version:\n%s", p, body)
		}
	}
	// Each link leads somewhere.
	body := get(h, "/index.md").Body.String()
	i := strings.Index(body, "Last version: <a href=\"") + len("Last version: <a href=\"")
	if u := body[i : i+strings.Index(body[i:], `"`)]; get(h, u).Code != http.StatusOK {
		t.Errorf("last version %s: status %d", u, get(h, u).Code)
	}
}

// Names close to a missing one are listed: a slip of letters, another
// extension, a deeper path whose rest the tree holds. Far names are not.
func TestNotFoundCloseNames(t *testing.T) {
	s, _, _ := serveTree(t)
	h := s.handler()
	for p, want := range map[string]string{
		"/indx.md":      `<li><a href="/index.md">index.md</a></li>`,
		"/index.txt":    `<li><a href="/index.md">index.md</a></li>`,
		"/a/b.og":       `<li><a href="/a/b.go">a/b.go</a></li>`,
		"/A/README.md":  `<li><a href="/a/README.md">a/README.md</a></li>`,
		"/a/READ":       `<li><a href="/a/README.md">a/README.md</a></li>`,
		"/b/README.md":  `<li><a href="/a/README.md">a/README.md</a></li>`,
		"/a/REAMDE.txt": `<li><a href="/a/README.md">a/README.md</a></li>`,
		"/a/nothing.md": "",
	} {
		body := get(h, p).Body.String()
		if want == "" {
			if strings.Contains(body, "Close names") {
				t.Errorf("%s: lists far names:\n%s", p, body)
			}
			continue
		}
		if !strings.Contains(body, want) {
			t.Errorf("%s: lacks %s:\n%s", p, want, body)
		}
	}
	if body := get(h, "/127.31.1.1-8080.loc").Body.String(); strings.Contains(body, ".lock") {
		t.Errorf("a lock is offered as a close name:\n%s", body)
	}
}

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"", "", 0}, {"abc", "", 3}, {"kitten", "sitting", 3}, {"b.og", "b.go", 2}, {"åäö", "aäö", 1}} {
		if d := editDistance(c.a, c.b); d != c.d {
			t.Errorf("%q %q: %d, want %d", c.a, c.b, d, c.d)
		}
	}
}
