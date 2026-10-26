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
		"/index.md":       {"<th>deleted</th><td><a href=\"/?commit=", "Tidy the top", "<th>last version</th><td><a href=\"/@", "/index.md\">", "Add the docs"},
		"/bin.dat":        {"<th>moved</th>", "Tidy the top", `<th>to</th><td><a href="/data.bin"><span class="seg">data.bin</span></a>`, "/bin.dat\">"},
		"/a/b.go":         {"<th>moved</th>", "Move a to lib", `<th>to</th><td><a href="/lib/b.go"><span class="seg">lib/</span><wbr><span class="seg">b.go</span></a>`, "/a/b.go\">", "Rename the package"},
		"/a/":             {`<th>to</th><td><a href="/lib"><span class="seg">lib</span></a>`, "/a/\">"},
		"/lib/page.html":  {"<th>deleted</th><td>since the last commit", "<th>last version</th><td><a href=\"/@", "/lib/page.html\">", "Move a to lib"},
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
		if len(want) == 0 && strings.Contains(body, "last version") {
			t.Errorf("%s: a path git never held has a last version:\n%s", p, body)
		}
	}
	// Each link leads somewhere.
	body := get(h, "/index.md").Body.String()
	i := strings.Index(body, "<th>last version</th><td><a href=\"") + len("<th>last version</th><td><a href=\"")
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
		"/indx.md":      `<a href="/index.md"><span class="seg">index.md</span></a>`,
		"/index.txt":    `<a href="/index.md"><span class="seg">index.md</span></a>`,
		"/a/b.og":       `<a href="/a/b.go"><span class="seg">a/</span><wbr><span class="seg">b.go</span></a>`,
		"/A/README.md":  `<a href="/a/README.md"><span class="seg">a/</span><wbr><span class="seg">README.md</span></a>`,
		"/a/READ":       `<a href="/a/README.md"><span class="seg">a/</span><wbr><span class="seg">README.md</span></a>`,
		"/b/README.md":  `<a href="/a/README.md"><span class="seg">a/</span><wbr><span class="seg">README.md</span></a>`,
		"/a/REAMDE.txt": `<a href="/a/README.md"><span class="seg">a/</span><wbr><span class="seg">README.md</span></a>`,
		"/a/nothing.md": "",
	} {
		body := get(h, p).Body.String()
		if want == "" {
			if strings.Contains(body, "<th>close</th>") {
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
