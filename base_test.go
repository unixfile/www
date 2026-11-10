package www

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// withBase serves under base for one test.
func withBase(t *testing.T, base string) {
	t.Helper()
	setBase(base)
	t.Cleanup(func() { setBase("") })
}

func TestCheckBase(t *testing.T) {
	for in, want := range map[string]string{
		"/x":        "/x",
		"/x/":       "/x",
		"/a/b-c.d_": "/a/b-c.d_",
	} {
		if got, err := checkBase(in); err != nil || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/", "x", "x/", "//", "/a//b", "/.", "/..", "/a/../b", "/_x", "/a b", "/a?b", "/a#b", "/a%20", "/å"} {
		if got, err := checkBase(in); err == nil {
			t.Errorf("%q: %q and no error", in, got)
		}
	}
}

func TestUnrooted(t *testing.T) {
	withBase(t, "/x")
	for in, want := range map[string]string{"/x": "/", "/x/": "/", "/x/a/b": "/a/b"} {
		if got, ok := unrooted(in); !ok || got != want {
			t.Errorf("%q: %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"/", "/xy", "/xy/a", "/a/x/"} {
		if got, ok := unrooted(in); ok {
			t.Errorf("%q: %q, but it lies outside the base", in, got)
		}
	}
}

// Both executables take --base, joined or apart, and neither takes a bad
// one. A base belongs to a start, so --restart, -l and --kill refuse it.
func TestParseBase(t *testing.T) {
	for _, args := range [][]string{{"--base=/x"}, {"--base", "/x/"}} {
		if c, err := parseArgs(args); err != nil || c.base != "/x" {
			t.Errorf("%q: %+v, %v", args, c, err)
		}
		if c, err := parsePublicArgs(append([]string{"--disk=1G", "-a", "127.0.0.1"}, args...)); err != nil || c.base != "/x" {
			t.Errorf("public %q: %+v, %v", args, c, err)
		}
	}
	for _, args := range [][]string{{"--base"}, {"--base="}, {"--base=x"}, {"--base=/_x"},
		{"--restart", "--base=/x"}, {"-l", "--base=/x"}, {"--kill", "--base=/x"}, {"--commit=HEAD", "--base=/x"}} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	for _, args := range [][]string{{"--base"}, {"--base="}, {"--base=x"}} {
		if _, err := parsePublicArgs(append([]string{"--disk=1G", "-a", "127.0.0.1"}, args...)); err == nil {
			t.Errorf("public %q: no error", args)
		}
	}
}

// urlAttr matches each attribute that can hold a URL: links, sources, form
// targets and the data attributes the script fetches.
var urlAttr = regexp.MustCompile(` (href|src|action|data-[a-z]+)="([^"]*)"`)

// crawl walks every page that start leads to through h and fails on a URL
// outside base: a path from the root in a page, a redirect or a relative
// link that climbs out. follow limits which pages are fetched; every page
// fetched is checked whole. It returns the pages by path and query.
func crawl(t *testing.T, h http.Handler, base, start string, follow func(*url.URL) bool, hdr ...string) map[string]int {
	t.Helper()
	seen := map[string]int{}
	queue := []string{start}
	inside := func(from, ref string) *url.URL {
		u, err := url.Parse(from)
		if err != nil {
			t.Fatalf("%s: %v", from, err)
		}
		to, err := u.Parse(ref)
		if err != nil || to.Host != "" && to.Host != u.Host {
			return nil
		}
		if !strings.HasPrefix(to.Path, base+"/") {
			t.Errorf("%s: %q leads to %s, outside %s/", from, ref, to.Path, base)
			return nil
		}
		to.Fragment = ""
		return to
	}
	for len(queue) > 0 && len(seen) < 400 {
		at := queue[0]
		queue = queue[1:]
		if _, ok := seen[at]; ok {
			continue
		}
		rec := get(h, at, hdr...)
		seen[at] = rec.Code
		if rec.Code >= 500 {
			t.Errorf("%s: %d", at, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			if to := inside(at, loc); to != nil {
				queue = append(queue, to.String())
			}
			continue
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			continue
		}
		for _, m := range urlAttr.FindAllStringSubmatch(rec.Body.String(), -1) {
			name, ref := m[1], html.UnescapeString(m[2])
			// A data attribute holds a URL when it starts as one; others
			// hold numbers, names and times.
			if strings.HasPrefix(name, "data-") && !strings.HasPrefix(ref, "/") && !strings.HasPrefix(ref, "?") {
				continue
			}
			if strings.Contains(ref, "://") || strings.HasPrefix(ref, "mailto:") || strings.HasPrefix(ref, "#") || ref == "" {
				continue
			}
			if to := inside(at, ref); to != nil && name != "action" && follow(to) {
				queue = append(queue, to.String())
			}
		}
	}
	return seen
}

// has reports whether the crawl fetched a page whose address holds part,
// with a 200.
func has(seen map[string]int, part string) bool {
	for at, code := range seen {
		if code == http.StatusOK && strings.Contains(at, part) {
			return true
		}
	}
	return false
}

// Under --base every URL a reader's server writes starts with the base:
// the index, listings, docs, code, histories, commits, the stylesheet, the
// forms and each redirect. The base alone redirects to its index, and a
// path outside it is a 404.
func TestBaseHoldsEveryLocalURL(t *testing.T) {
	withBase(t, "/x")
	s, _ := repoTree(t)
	h := s.handler()
	// git's own directory holds nothing of the pages and many files.
	seen := crawl(t, h, "/x", "/x", func(u *url.URL) bool { return !strings.Contains(u.Path, "/.git/") })
	for _, part := range []string{"/x/a/b.go", "/x/a/README.md", "?log", "?commit=", "/x/_" + progName + "/style.", "?details", "?raw"} {
		if !has(seen, part) {
			t.Errorf("the crawl reached no page with %s; it saw %d pages", part, len(seen))
		}
	}
	if seen["/x"] != http.StatusMovedPermanently || seen["/x/"] != http.StatusOK {
		t.Errorf("/x: %d, /x/: %d", seen["/x"], seen["/x/"])
	}
	for _, p := range []string{"/", "/a/b.go", "/xy/", "/_" + progName + "/live"} {
		if rec := get(h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s, outside the base: %d", p, rec.Code)
		}
	}
	// A missing page and a search answer name only URLs below the base.
	for _, p := range []string{"/x/nope.md", "/x/a/nope/", "/x/?search=readme"} {
		body := get(h, p).Body.String()
		for _, m := range regexp.MustCompile(`(?:href=\\?"|"href":")(/[^"\\]*)`).FindAllStringSubmatch(body, -1) {
			if !strings.HasPrefix(m[1], "/x/") {
				t.Errorf("%s names %s", p, m[1])
			}
		}
	}
	if rec := do(h, "POST", "/x/_"+progName+"/stop", "Origin", "http://127.31.1.1:8080"); rec.Code == http.StatusNotFound {
		t.Errorf("the stop form is not below the base: %d", rec.Code)
	}
}

// Under --base the web address reads a repo below the base, and every URL
// on its pages starts with the base too. A link to a forge leads to the
// repo below the base.
func TestBaseHoldsEveryWebURL(t *testing.T) {
	withBase(t, "/x")
	f := newForge(t, nil)
	f.repo(t, "acct/docs", map[string]string{"README.md": "# Old\n", "guide/a.md": "Guide text\n"})
	f.repo(t, "acct/docs", map[string]string{
		"README.md": "# New\n\n[guide](guide/a.md) and [other](https://github.com/junegunn/fzf)\n",
		"main.go":   "package main\n",
	})
	w := testWeb(t, defaultDisk)
	repo := "/x/" + f.host + "/acct/docs/"
	if rec := read(t, w, repo+"README.md"); rec.Code != http.StatusOK {
		t.Fatalf("%sREADME.md: %d\n%s", repo, rec.Code, rec.Body)
	} else if want := `href="/x/github.com/junegunn/fzf/"`; !strings.Contains(rec.Body.String(), want) {
		t.Errorf("the forge link is not below the base; want %s in\n%s", want, rec.Body)
	}
	// Only the repo and the root are followed: a host's or an account's
	// page would ask the forge for a listing.
	seen := crawl(t, w.srv.handler(), "/x", "/x", func(u *url.URL) bool {
		return u.Path == "/x/" || strings.HasPrefix(u.Path, repo)
	}, "Host", "127.31.1.2")
	for _, part := range []string{repo + "main.go", repo + "guide/a.md", "?log", "?commit="} {
		if !has(seen, part) {
			t.Errorf("the crawl reached no page with %s; it saw %d pages", part, len(seen))
		}
	}
	if rec := webGet(w, "/"+f.host+"/acct/docs/"); rec.Code != http.StatusNotFound {
		t.Errorf("a repo outside the base: %d", rec.Code)
	}
	// A URL pasted whole after the base redirects below the base.
	if rec := webGet(w, "/x/http://"+f.host+"/acct/docs.git"); !strings.HasPrefix(rec.Header().Get("Location"), "/x/") {
		t.Errorf("a pasted URL redirects to %q", rec.Header().Get("Location"))
	}
}
