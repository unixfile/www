package www

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// serveTree builds a small doc tree and returns a server holding it at /.
func serveTree(t *testing.T) (*server, *tree, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.md":             "# Title\nline one\nline two\n- item\n\n[code](a/b.go)\n\nsee https://example.com/x\n",
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
	s := newServer().local().local()
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

func TestStopRefusesOtherSites(t *testing.T) {
	s, _, _ := serveTree(t)
	stopped := make(chan struct{}, 1)
	s.stop = func([]*tree, string) { stopped <- struct{}{} }
	h := s.handler()
	if rec := do(h, "POST", ownPath+"stop", "Sec-Fetch-Site", "cross-site"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site stop: status %d, want 403", rec.Code)
	}
	if rec := do(h, "POST", ownPath+"stop", "Origin", "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("foreign origin stop: status %d, want 403", rec.Code)
	}
	select {
	case <-stopped:
		t.Fatal("refused stop still stopped the server")
	case <-time.After(50 * time.Millisecond):
	}
	if rec := do(h, "POST", ownPath+"stop", "Origin", "null"); rec.Code != http.StatusForbidden {
		t.Errorf("null origin without Sec-Fetch-Site: status %d, want 403", rec.Code)
	}
	// A browser may send Origin: null on its own same-origin form post.
	for _, origin := range []string{"http://127.31.1.1:8080", "null"} {
		rec := do(h, "POST", ownPath+"stop", "Sec-Fetch-Site", "same-origin", "Origin", origin)
		if rec.Code != http.StatusGone {
			t.Fatalf("same-origin stop with Origin %s: status %d", origin, rec.Code)
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatalf("same-origin stop with Origin %s did not stop the server", origin)
		}
	}
}

// The fonts come from www, cached for a year: every file the stylesheet
// names, and nothing else from the font directory.
func TestFonts(t *testing.T) {
	s, _, _ := serveTree(t)
	urls := regexp.MustCompile(`url\("fonts/([^"]+)"\)`).FindAllSubmatch(styleCSS, -1)
	if len(urls) < 16 {
		t.Errorf("stylesheet names %d font files, want 16", len(urls))
	}
	var rec *httptest.ResponseRecorder
	for _, u := range urls {
		rec = get(s.handler(), ownPath+"fonts/"+string(u[1]))
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "font/woff2" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || rec.Body.Len() < 1000 {
			t.Errorf("%s: status %d type %q cache %q size %d", u[1], rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"), rec.Body.Len())
		}
	}
	for _, p := range []string{ownPath + "fonts/OFL.txt", ownPath + "fonts/subset.py", ownPath + "fonts/..%2Fstyle.css", ownPath + "fonts/nope.woff2"} {
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
	req := httptest.NewRequest("POST", ownPath+"stop", strings.NewReader("tree=%2Fother%2F"))
	req.Host = "127.31.1.1:8080"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusGone || len(got) != 1 || got[0] != other {
		t.Fatalf("status %d, withdrew %v, want only the other tree", rec.Code, got)
	}
	if got[0] == tr {
		t.Fatal("withdrew the wrong tree")
	}
}

// The stylesheet's URL names its content, so the browser keeps it for a
// year, and every page links that URL.
func TestStylesheetCached(t *testing.T) {
	s, _, _ := serveTree(t)
	h := s.handler()
	rec := get(h, styleURL)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("%s: status %d, Cache-Control %q", styleURL, rec.Code, rec.Header().Get("Cache-Control"))
	}
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(ownPath) + `style\.[0-9a-f]{12}\.css$`).MatchString(styleURL) {
		t.Errorf("stylesheet URL %q does not name a hash", styleURL)
	}
	if body := get(h, "/a/").Body.String(); !strings.Contains(body, `<link rel="stylesheet" href="`+styleURL+`">`) {
		t.Errorf("page does not link %s: %s", styleURL, body)
	}
}
