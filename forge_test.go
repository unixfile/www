package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
)

// A link to a repo on a forge, or a file or directory in one, maps to its
// path on the web address; other pages of a forge, and other hosts, map
// to nothing.
func TestWebPath(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/junegunn/fzf":                                               "/github.com/junegunn/fzf/",
		"https://www.github.com/junegunn/fzf.git":                                       "/github.com/junegunn/fzf/",
		"https://github.com/junegunn/fzf/blob/master/README.md":                         "/github.com/junegunn/fzf/README.md",
		"https://github.com/junegunn/fzf/tree/v0.50/shell":                              "/github.com/junegunn/fzf/shell/",
		"https://github.com/junegunn/fzf/tree/master":                                   "/github.com/junegunn/fzf/",
		"https://codeberg.org/forgejo/forgejo/src/branch/main/go.mod":                   "/codeberg.org/forgejo/forgejo/go.mod",
		"https://gitlab.com/a/b/c/-/blob/main/x.md":                                     "/gitlab.com/a/b/c/x.md",
		"https://gitlab.com/a/b":                                                        "/gitlab.com/a/b/",
		"https://bitbucket.org/a/b/src/main/x.md":                                       "/bitbucket.org/a/b/x.md",
		"https://github.com/junegunn/fzf/blob/26ff936/BUILD.md":                         "/github.com/junegunn/fzf@26ff936/BUILD.md",
		"https://github.com/junegunn/fzf/tree/26ff936df038b6a62ec42cf18db27b2ea2bb8ec3": "/github.com/junegunn/fzf@26ff936df038b6a62ec42cf18db27b2ea2bb8ec3/",
		"https://github.com/junegunn/fzf/blob/cafe/x.md":                                "/github.com/junegunn/fzf/x.md",
		"https://github.com/junegunn/fzf/raw/master/x.png":                              "/github.com/junegunn/fzf/x.png",
		"https://raw.githubusercontent.com/rivo/uniseg/master/LICENSE.txt":              "/github.com/rivo/uniseg/LICENSE.txt",
		"https://raw.githubusercontent.com/rivo/uniseg/refs/heads/main/a/b.go":          "/github.com/rivo/uniseg/a/b.go",
		"https://raw.githubusercontent.com/rivo/uniseg/0123abcd/LICENSE":                "/github.com/rivo/uniseg@0123abcd/LICENSE",
		"https://raw.githubusercontent.com/rivo/uniseg":                                 "",
		"https://raw.githubusercontent.com/rivo/uniseg/master":                          "",
		"https://codeberg.org/a/b/raw/branch/main/x.md":                                 "/codeberg.org/a/b/x.md",
		"https://gitlab.com/a/b/-/raw/main/x.md":                                        "/gitlab.com/a/b/x.md",
		"https://github.com/junegunn/fzf/issues/3":                                      "",
		"https://github.com/junegunn":                                                   "",
		"https://github.com/sponsors/junegunn":                                          "",
		"https://example.com/a/b":                                                       "",
		"ftp://github.com/a/b":                                                          "",
		"https://github.com/a/../b":                                                     "",
	} {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, got := webPath(u); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

// A URL pasted whole after the address, or a forge's view of a file,
// redirects to the path that reads it. The mux has folded the URL's //
// into / before.
func TestWebPastedURL(t *testing.T) {
	w := testWeb(t, defaultDisk)
	for in, want := range map[string]string{
		"/https:/git.suckless.org/dwm/":                       "/git.suckless.org/dwm",
		"/https:/git.suckless.org/dwm":                        "/git.suckless.org/dwm",
		"/http:/github.com/junegunn/fzf":                      "/github.com/junegunn/fzf/",
		"/https:/github.com/junegunn/fzf/blob/master/main.go": "/github.com/junegunn/fzf/main.go",
		"/github.com/junegunn/fzf/tree/master/shell":          "/github.com/junegunn/fzf/shell/",
		"/raw.githubusercontent.com/junegunn/fzf/master/x.md": "/github.com/junegunn/fzf/x.md",
	} {
		rec := webGet(w, in)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %s, want %s", in, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	for _, in := range []string{"/github.com/junegunn", "/github.com/junegunn/fzf/README.md"} {
		if rec := webGet(w, in); rec.Code == http.StatusMovedPermanently {
			t.Errorf("%s redirects to %s", in, rec.Header().Get("Location"))
		}
	}
}

// A page of a repo on the web address links its forge's own permanent
// URL: a file or directory at the commit shown, or a commit.
func TestForgeLink(t *testing.T) {
	needGit(t)
	_, tr, dir := serveTree(t)
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	commitAll(t, dir, "one")
	out, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	head := strings.TrimSpace(string(out))
	tr.web = &webRepo{key: "github.com/junegunn/fzf"}
	for _, c := range []struct{ path, kind, want string }{
		{"/a/b.go", "code", "https://github.com/junegunn/fzf/blob/" + head + "/a/b.go"},
		{"/a/", "dir", "https://github.com/junegunn/fzf/tree/" + head + "/a"},
		{"/", "dir", "https://github.com/junegunn/fzf/tree/" + head},
		{"/?commit=" + head, "commit", "https://github.com/junegunn/fzf/commit/" + head},
		{"/?log", "log", ""},
	} {
		got, lines := tr.forgeLink(httptest.NewRequest("GET", c.path, nil), c.kind)
		if got != c.want || c.want != "" && lines != "github" {
			t.Errorf("%s: %q %q, want %q", c.path, got, lines, c.want)
		}
	}
	tr.web.key = "gitlab.com/a/b/c"
	if got, lines := tr.forgeLink(httptest.NewRequest("GET", "/a/b.go", nil), "code"); got != "https://gitlab.com/a/b/c/-/blob/"+head+"/a/b.go" || lines != "gitlab" {
		t.Errorf("gitlab: %q %q", got, lines)
	}
	tr.web.key = "git.suckless.org/dwm"
	if got, _ := tr.forgeLink(httptest.NewRequest("GET", "/a/b.go", nil), "code"); got != "" {
		t.Errorf("an unknown forge: %q", got)
	}
}
