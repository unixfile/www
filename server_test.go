package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serveTree builds a small doc tree and returns a server holding it at /.
func serveTree(t *testing.T) (*server, *tree, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.md":             "# Title\nline one\nline two\n\n[code](a/b.go)\n\nsee https://example.com/x\n",
		"a/b.go":               "package a\n\nfunc F() {}\n",
		"a/README.md":          "Readme of a\n",
		"a/page.html":          "<script>alert(1)</script>\n",
		"127.31.1.1-8080.lock": "url: x\n",
		"bin.dat":              "\x00\x01\x02",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Symlink("/etc", filepath.Join(dir, "escape"))
	s := newServer()
	tr := addTree(t, s, dir, "/", filepath.Base(dir))
	return s, tr, dir
}

func addTree(t *testing.T, s *server, dir, prefix, label string) *tree {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	tr := newTree(root, dir, prefix, label)
	s.add(tr)
	return tr
}

func get(h http.Handler, path string, hdr ...string) *httptest.ResponseRecorder {
	return do(h, "GET", path, hdr...)
}

func do(h http.Handler, method, path string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Host = "127.31.1.1:8080"
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func needPandoc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pandoc"); err != nil {
		t.Skip("pandoc not installed")
	}
}

func TestForeignHostRejected(t *testing.T) {
	s, _, _ := serveTree(t)
	if rec := get(s.handler(), "/", "Host", "evil.example:7417"); rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want 421", rec.Code)
	}
	for _, host := range []string{"localhost:9", "[::1]:7417", "127.0.0.1"} {
		if rec := get(s.handler(), "/bin.dat", "Host", host); rec.Code != http.StatusOK {
			t.Errorf("host %s: status %d, want 200", host, rec.Code)
		}
	}
}

func TestEveryResponseForbidsScripts(t *testing.T) {
	s, _, _ := serveTree(t)
	rec := get(s.handler(), "/a/page.html?raw")
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("no CSP: %v", rec.Header())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control %q, want no-cache", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("raw html served as %q, want text/plain", ct)
	}
}

// Rendered pages stay out of the back/forward cache; raw files revalidate.
func TestCacheHeaders(t *testing.T) {
	s, _, _ := serveTree(t)
	for p, want := range map[string]string{"/missing": "no-store", "/bin.dat": "no-cache"} {
		if cc := get(s.handler(), p).Header().Get("Cache-Control"); cc != want {
			t.Errorf("%s: Cache-Control %q, want %q", p, cc, want)
		}
	}
}

func TestDirectoryRedirectsToSlash(t *testing.T) {
	s, _, _ := serveTree(t)
	rec := get(s.handler(), "/a")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/a/" {
		t.Fatalf("status %d location %q, want 301 to /a/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestListingSkipsLockAndReadme(t *testing.T) {
	needPandoc(t)
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

// Lines of a doc join into one paragraph, as standard Markdown has them.
func TestLinesJoin(t *testing.T) {
	needPandoc(t)
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/index.md").Body.String()
	if !strings.Contains(body, "<p>line one line two</p>") {
		t.Errorf("lines not joined into one paragraph: %s", body)
	}
}

func TestBareURLIsLink(t *testing.T) {
	needPandoc(t)
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/index.md").Body.String()
	if !strings.Contains(body, `<a href="https://example.com/x"`) {
		t.Errorf("bare URL not linked: %s", body)
	}
}

func TestCodePageIsHighlighted(t *testing.T) {
	needPandoc(t)
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/a/b.go").Body.String()
	if !strings.Contains(body, `class="sourceCode`) || !strings.Contains(body, `<span id="L3"><a href="#L3">`) {
		t.Errorf("no highlighted code with GitHub line anchors: %s", body)
	}
	if strings.Contains(body, "cb1-") {
		t.Errorf("pandoc line ids left in: %s", body)
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

func TestStopRefusesOtherSites(t *testing.T) {
	s, _, _ := serveTree(t)
	stopped := make(chan struct{}, 1)
	s.stop = func([]*tree, string) { stopped <- struct{}{} }
	h := s.handler()
	if rec := do(h, "POST", "/_mdserve/stop", "Sec-Fetch-Site", "cross-site"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site stop: status %d, want 403", rec.Code)
	}
	if rec := do(h, "POST", "/_mdserve/stop", "Origin", "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("foreign origin stop: status %d, want 403", rec.Code)
	}
	select {
	case <-stopped:
		t.Fatal("refused stop still stopped the server")
	case <-time.After(50 * time.Millisecond):
	}
	if rec := do(h, "POST", "/_mdserve/stop", "Origin", "null"); rec.Code != http.StatusForbidden {
		t.Errorf("null origin without Sec-Fetch-Site: status %d, want 403", rec.Code)
	}
	// A browser may send Origin: null on its own same-origin form post.
	for _, origin := range []string{"http://127.31.1.1:8080", "null"} {
		rec := do(h, "POST", "/_mdserve/stop", "Sec-Fetch-Site", "same-origin", "Origin", origin)
		if rec.Code != http.StatusOK {
			t.Fatalf("same-origin stop with Origin %s: status %d", origin, rec.Code)
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatalf("same-origin stop with Origin %s did not stop the server", origin)
		}
	}
}

// Delete lock on the index returns to the index while other trees remain;
// the last tree's button ends on the stopped page.
func TestIndexDeleteReturns(t *testing.T) {
	s, tr, dir := serveTree(t)
	tr.prefix, tr.label = "/notes/", "~/notes"
	addTree(t, s, filepath.Join(dir, "a"), "/other/", "~/other")
	var got []*tree
	s.stop = func(ts []*tree, _ string) {
		got = ts
		for _, t := range ts {
			s.remove(t)
		}
	}
	post := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/_mdserve/stop", strings.NewReader(form))
		req.Host = "127.31.1.1:8080"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.handler().ServeHTTP(rec, req)
		return rec
	}
	rec := post("tree=%2Fnotes%2F&from=index")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || len(got) != 1 || got[0] != tr {
		t.Errorf("first delete: status %d location %q stopped %d", rec.Code, rec.Header().Get("Location"), len(got))
	}
	rec = post("tree=%2Fother%2F&from=index")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Stopped serving ~/other") {
		t.Errorf("last delete: status %d: %s", rec.Code, rec.Body.String())
	}
	// The server closes as this page leaves, so the page carries its style.
	if body := rec.Body.String(); !strings.Contains(body, "<style>") || strings.Contains(body, "/_mdserve/style.css") {
		t.Errorf("stopped page links its stylesheet instead of carrying it")
	}
}

// The fonts come from mdserve, cached for a year; nothing else leaves the
// font directory.
func TestFonts(t *testing.T) {
	s, _, _ := serveTree(t)
	rec := get(s.handler(), "/_mdserve/fonts/STIXTwoText-Regular.otf")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "font/otf" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || rec.Body.Len() < 1000 {
		t.Errorf("font: status %d type %q cache %q size %d", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"), rec.Body.Len())
	}
	for _, p := range []string{"/_mdserve/fonts/STIXTwoText-OFL.txt", "/_mdserve/fonts/..%2Fstyle.css", "/_mdserve/fonts/nope.otf"} {
		if rec := get(s.handler(), p); rec.Code == http.StatusOK {
			t.Errorf("%s: status 200, want 404", p)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); strings.Contains(csp, "googleapis") || strings.Contains(csp, "gstatic") {
		t.Errorf("CSP still allows Google: %s", csp)
	}
}

func TestMount(t *testing.T) {
	for dir, want := range map[string][2]string{
		"/home/u":               {"/", "~"},
		"/home/u/scratch/notes": {"/scratch/notes/", "~/scratch/notes"},
		"/home/user2/x":         {"/home/user2/x/", "/home/user2/x"},
		"/tmp/x":                {"/tmp/x/", "/tmp/x"},
		"/":                     {"/", "/"},
	} {
		prefix, label := mount(dir, "/home/u")
		if prefix != want[0] || label != want[1] {
			t.Errorf("%s: %q %q, want %q %q", dir, prefix, label, want[0], want[1])
		}
	}
}

// The served tree lives under its home-relative prefix; / lists the trees.
func TestPrefixedTree(t *testing.T) {
	needPandoc(t)
	s, tr, _ := serveTree(t)
	tr.prefix, tr.label = "/scratch/my docs/", "~/scratch/my docs"
	h := s.handler()
	rec := get(h, "/scratch/my%20docs")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/scratch/my%20docs/" {
		t.Errorf("status %d location %q, want 302 to the prefix", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(h, "/index.md"); rec.Code != http.StatusNotFound {
		t.Errorf("path outside the prefix: status %d, want 404", rec.Code)
	}
	body := get(h, "/scratch/my%20docs/a/b.go").Body.String()
	if !strings.Contains(body, `<a href="/scratch/my%20docs/" title="~/scratch/my docs">my docs</a>&nbsp;/ <a href="/scratch/my%20docs/a/">a</a>&nbsp;/ <span>b.go</span>`) {
		t.Errorf("breadcrumbs lack the prefix: %s", body)
	}
}

// The index at / lists every served tree with a button that deletes its
// lock, and offers to stop them all.
func TestIndexListsTrees(t *testing.T) {
	s, tr, dir := serveTree(t)
	tr.prefix, tr.label = "/notes/", "~/notes"
	addTree(t, s, filepath.Join(dir, "a"), "/other/", "~/other")
	rec := get(s.handler(), "/")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `<a href="/notes/" title=""><span class="seg">~/</span><wbr><span class="seg">notes</span></a>`) || !strings.Contains(body, `<a href="/other/" title=""><span class="seg">~/</span><wbr><span class="seg">other</span></a>`) {
		t.Errorf("status %d, index lacks the trees: %s", rec.Code, body)
	}
	if !strings.Contains(body, `<button class="kill">Kill server</button>`) {
		t.Errorf("index lacks the button that stops every tree: %s", body)
	}
	if !strings.Contains(body, `name="tree" value="/notes/"><input type="hidden" name="from" value="index"><button>Delete lock</button>`) {
		t.Errorf("index lacks a Delete lock button per tree: %s", body)
	}
	if rec := get(s.handler(), "/elsewhere/"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `href="/notes/"`) {
		t.Errorf("path outside every tree: status %d, want 404 with the index", rec.Code)
	}
}

// A path breaks after its slashes, and a URL keeps // with its scheme.
func TestSegments(t *testing.T) {
	for in, want := range map[string]string{
		"~/a b/c":        `<span class="seg">~/</span><wbr><span class="seg">a b/</span><wbr><span class="seg">c</span>`,
		"/tmp/x/":        `<span class="seg">/</span><wbr><span class="seg">tmp/</span><wbr><span class="seg">x/</span>`,
		"http://h:80/a/": `<span class="seg">http://</span><wbr><span class="seg">h:80/</span><wbr><span class="seg">a/</span>`,
		"<x>/y":          `<span class="seg">&lt;x&gt;/</span><wbr><span class="seg">y</span>`,
	} {
		if got := segments(in); got != want {
			t.Errorf("segments(%q) = %s, want %s", in, got, want)
		}
	}
}

// A tree inside another answers for its own paths.
func TestNestedTreeWins(t *testing.T) {
	s, _, dir := serveTree(t)
	addTree(t, s, filepath.Join(dir, "a"), "/a/", "inner")
	body := get(s.handler(), "/a/b.go?raw").Body.String()
	if body != "package a\n\nfunc F() {}\n" {
		t.Errorf("nested tree served %q", body)
	}
	if body := get(s.handler(), "/a/").Body.String(); !strings.Contains(body, `<nav><a href="/">index</a>&nbsp;/ <span title="inner">inner</span></nav>`) {
		t.Errorf("nested tree lost its label: %s", body)
	}
}

// A tree's button withdraws that tree only.
func TestStopWithdrawsOneTree(t *testing.T) {
	s, tr, dir := serveTree(t)
	other := addTree(t, s, filepath.Join(dir, "a"), "/other/", "~/other")
	var got []*tree
	s.stop = func(ts []*tree, _ string) { got = ts }
	body := get(s.handler(), "/other/").Body.String()
	if !strings.Contains(body, `<input type="hidden" name="tree" value="/other/">`) {
		t.Fatalf("tree page lacks its stop form: %s", body)
	}
	req := httptest.NewRequest("POST", "/_mdserve/stop", strings.NewReader("tree=%2Fother%2F"))
	req.Host = "127.31.1.1:8080"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(got) != 1 || got[0] != other {
		t.Fatalf("status %d, withdrew %v, want only the other tree", rec.Code, got)
	}
	if got[0] == tr {
		t.Fatal("withdrew the wrong tree")
	}
}

func TestLocalHost(t *testing.T) {
	hosts := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(hosts, []byte("127.31.1.1 docs docs.local # mdserve\n192.168.1.5 nas\n# 127.0.0.1 commented\n"), 0o644)
	old := hostsFile
	hostsFile = hosts
	t.Cleanup(func() { hostsFile = old })
	for host, want := range map[string]bool{
		"127.31.1.1": true, "127.31.200.1": true, "127.0.0.1": true, "::1": true, "localhost": true,
		"docs": true, "DOCS.local": true,
		"nas": false, "commented": false, "evil.example": false, "10.0.0.1": false,
	} {
		if got := localHost(host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}
