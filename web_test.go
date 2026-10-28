package main

import (
	"context"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// forge serves the bare repos below its root over git's smart HTTP, as a
// forge does, with partial clone allowed. api answers other paths, or nil
// for 404.
type forge struct {
	root string
	host string // host:port, as a URL on the web address names it
	work map[string]string
	// packs counts git's requests for objects
	packs atomic.Int32
}

func newForge(t *testing.T, api http.Handler) *forge {
	t.Helper()
	needGit(t)
	gitPath, _ := exec.LookPath("git")
	f := &forge{root: t.TempDir(), work: map[string]string{}}
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + f.root, "GIT_HTTP_EXPORT_ALL=1"}, Stderr: io.Discard}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/info/refs") || strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			if strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
				f.packs.Add(1)
			}
			backend.ServeHTTP(w, r)
			return
		}
		if api != nil {
			api.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	f.host = strings.TrimPrefix(srv.URL, "http://")
	old := webScheme
	webScheme = "http"
	t.Cleanup(func() { webScheme = old })
	return f
}

// repo makes the bare repo name with files committed, and returns its
// commit.
func (f *forge) repo(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	work, ok := f.work[name]
	if !ok {
		work = t.TempDir()
		f.work[name] = work
		bare := filepath.Join(f.root, filepath.FromSlash(name))
		for _, args := range [][]string{
			{"init", "-q", "--bare", "-b", "main", bare},
			{"-C", bare, "config", "uploadpack.allowFilter", "true"},
			{"-C", bare, "config", "uploadpack.allowAnySHA1InWant", "true"},
			{"init", "-q", "-b", "main", work},
			{"-C", work, "remote", "add", "origin", bare},
		} {
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(work, p)), 0o755)
		os.WriteFile(filepath.Join(work, p), []byte(body), 0o644)
	}
	commitAll(t, work, "change "+name)
	if out, err := exec.Command("git", "-C", work, "push", "-q", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git push: %v\n%s", err, out)
	}
	out, _ := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	return strings.TrimSpace(string(out))
}

func testWeb(t *testing.T, limit int64) *web {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := cacheRoot
	w, err := newWeb(ctx, t.TempDir(), limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		w.wg.Wait()
		cacheRoot = old
	})
	return w
}

// webGet asks the web address for path.
func webGet(w *web, path string, hdr ...string) *httptest.ResponseRecorder {
	return get(w.srv.handler(), path, append([]string{"Host", "127.31.1.2"}, hdr...)...)
}

// read visits path, which starts reading its repo, and waits for the page.
func read(t *testing.T, w *web, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := webGet(w, path)
	for end := time.Now().Add(20 * time.Second); rec.Code == http.StatusAccepted && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
		rec = webGet(w, path)
	}
	return rec
}

func TestWebReadsARepoOnTheVisit(t *testing.T) {
	f := newForge(t, nil)
	first := f.repo(t, "acct/docs", map[string]string{"README.md": "# Old\n", "guide/a.md": "Guide text\n"})
	head := f.repo(t, "acct/docs", map[string]string{"README.md": "# New\n"})
	w := testWeb(t, defaultDisk)
	path := "/" + f.host + "/acct/docs/guide/a.md"

	rec := webGet(w, path)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "reading") {
		t.Fatalf("first visit: %d\n%s", rec.Code, rec.Body)
	}
	// A second page of the repo starts a job of its own, and the two
	// serve one tree.
	webGet(w, "/"+f.host+"/acct/docs/README.md")
	read(t, w, "/"+f.host+"/acct/docs/README.md")
	rec = read(t, w, path)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Guide text") {
		t.Fatalf("after reading: %d\n%s", rec.Code, rec.Body)
	}
	if n := len(w.srv.list()); n != 1 {
		t.Errorf("%d trees serve one repo", n)
	}
	if !strings.Contains(rec.Body.String(), `@ <a href="/`+f.host+`/acct/docs@`+head[:12]+`/guide/a.md">`+head[:7]+"</a> · fetched ") {
		t.Errorf("no commit and fetch time in the header:\n%s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "Delete lock") {
		t.Error("a repo on the web address has a Delete lock button")
	}
	// The worktree lacks the first commit's blobs; its diff fetches them.
	rec = webGet(w, "/"+f.host+"/acct/docs/README.md?commit="+first)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "# Old") {
		t.Errorf("first commit's diff: %d\n%s", rec.Code, rec.Body)
	}
	// A version shows the repo as it was, fetching the old file; a hash
	// redirects to the commit's stardate, and the old address, @ below the
	// repo, to the version on its name.
	rec = webGet(w, "/"+f.host+"/acct/docs/@"+first[:7]+"/README.md")
	for i := 0; i < 3 && rec.Code == http.StatusMovedPermanently; i++ {
		rec = webGet(w, rec.Header().Get("Location"))
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Old</h1>") {
		t.Errorf("first commit's version: %d\n%s", rec.Code, rec.Body)
	}
	if rec := webGet(w, "/"+f.host+"/acct/docs.git"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/"+f.host+"/acct/docs" {
		t.Errorf(".git: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := webGet(w, "/"+f.host+"/acct/docs"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/"+f.host+"/acct/docs/" {
		t.Errorf("repo without slash: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := webGet(w, "/").Body.String(); !strings.Contains(body, f.host+"/acct/docs/") {
		t.Errorf("the root does not list the cached repo:\n%s", body)
	}
	// A cache deleted under the server is read again.
	os.RemoveAll(filepath.Join(w.root, f.host))
	if rec := webGet(w, path); rec.Code != http.StatusAccepted {
		t.Errorf("after the cache went: %d", rec.Code)
	}
	if rec := read(t, w, path); rec.Code != http.StatusOK {
		t.Errorf("read again: %d\n%s", rec.Code, rec.Body)
	}
}

// A version's listing fetches the files its partial clone lacks in one
// request, where ls-tree -l would fetch them one by one for their sizes.
func TestWebVersionFetchesADirectoryAtOnce(t *testing.T) {
	f := newForge(t, nil)
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		files["src/"+n+".go"] = "package " + n + "\n"
	}
	first := f.repo(t, "acct/code", files)
	for p := range files {
		files[p] += "// changed\n"
	}
	f.repo(t, "acct/code", files)
	w := testWeb(t, defaultDisk)
	if rec := read(t, w, "/"+f.host+"/acct/code/"); rec.Code != http.StatusOK {
		t.Fatalf("head: %d\n%s", rec.Code, rec.Body)
	}
	before := f.packs.Load()
	rec := webGet(w, "/"+f.host+"/acct/code@"+first[:12]+"/src/")
	for i := 0; i < 3 && rec.Code == http.StatusMovedPermanently; i++ {
		rec = webGet(w, rec.Header().Get("Location"))
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "e.go") {
		t.Fatalf("first commit's src/: %d\n%s", rec.Code, rec.Body)
	}
	if n := f.packs.Load() - before; n != 1 {
		t.Errorf("the listing took %d requests, want 1", n)
	}
}

// A visit after webRefresh fetches in the background, and the page moves
// to the new commit.
func TestWebRefreshes(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "one\n"})
	w := testWeb(t, defaultDisk)
	path := "/" + f.host + "/acct/r/README.md"
	if rec := read(t, w, path); !strings.Contains(rec.Body.String(), "one") {
		t.Fatalf("first read: %d\n%s", rec.Code, rec.Body)
	}
	head := f.repo(t, "acct/r", map[string]string{"README.md": "two\n"})
	if body := webGet(w, path).Body.String(); !strings.Contains(body, "one") {
		t.Fatalf("a fresh fetch fetched again:\n%s", body)
	}
	r := w.repos[f.host+"/acct/r"]
	r.mu.Lock()
	r.fetched, r.tried = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
	old := r.tree.dir
	r.mu.Unlock()
	webGet(w, path)
	var body string
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if body = webGet(w, path).Body.String(); strings.Contains(body, "two") {
			break
		}
	}
	if !strings.Contains(body, "two") || !strings.Contains(body, ">"+head[:7]+"</a> · fetched") {
		t.Fatalf("not refreshed:\n%s", body)
	}
	if exists(old) {
		t.Errorf("the old worktree %s is still there", old)
	}
}

// Another site cannot make the browser download a repo.
func TestWebAsksWhenAnotherSiteSends(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	rec := webGet(w, "/"+f.host+"/acct/r/", "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Another site sent you here") {
		t.Fatalf("cross-site: %d\n%s", rec.Code, rec.Body)
	}
	if len(w.jobs) != 0 {
		t.Error("a cross-site visit started a job")
	}
	// The page runs the script, which keeps the link's fragment, but
	// holds no stream that could reload it into a fetch.
	if body := rec.Body.String(); !strings.Contains(body, `<script data-live="">`) {
		t.Errorf("confirm page script: %s", body)
	}
	if rec := webGet(w, "/"+f.host+"/acct/r/", "Sec-Fetch-Site", "same-origin"); rec.Code != http.StatusAccepted {
		t.Errorf("same-origin: %d", rec.Code)
	}
}

// When the cache outgrows its limit, the repo read longest ago goes.
func TestWebTrimsTheCache(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/a", map[string]string{"README.md": strings.Repeat("a", 1<<16)})
	f.repo(t, "acct/b", map[string]string{"README.md": strings.Repeat("b", 1<<16)})
	w := testWeb(t, defaultDisk)
	read(t, w, "/"+f.host+"/acct/a/")
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(w.root, f.host, "acct", "a", ".used"), past, past)
	w.limit = w.onDisk()[0].size * 3 / 2
	if rec := read(t, w, "/"+f.host+"/acct/b/"); rec.Code != http.StatusOK {
		t.Fatalf("b: %d\n%s", rec.Code, rec.Body)
	}
	if exists(filepath.Join(w.root, f.host, "acct", "a")) {
		t.Error("a, read longest ago, is still cached")
	}
	if rec := webGet(w, "/"+f.host+"/acct/a/README.md"); rec.Code != http.StatusAccepted {
		t.Errorf("a after it went: %d, want a new read", rec.Code)
	}
	read(t, w, "/"+f.host+"/acct/a/README.md")
	w.limit = 1
	f.repo(t, "acct/c", map[string]string{"README.md": "c\n"})
	rec := read(t, w, "/"+f.host+"/acct/c/")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "over the cache") {
		t.Errorf("a repo over the limit: %d\n%s", rec.Code, rec.Body)
	}
}

// The servers on every address share the cache, so a trim leaves a repo
// whose lock another server holds, eg while it fetches the repo.
func TestWebTrimLeavesALockedRepo(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/a", map[string]string{"README.md": strings.Repeat("a", 1<<16)})
	f.repo(t, "acct/b", map[string]string{"README.md": strings.Repeat("b", 1<<16)})
	w := testWeb(t, defaultDisk)
	read(t, w, "/"+f.host+"/acct/a/")
	a := filepath.Join(w.root, f.host, "acct", "a")
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(a, ".used"), past, past)
	w.limit = w.onDisk()[0].size * 3 / 2
	// A lock of its own open file stands for another server's.
	unlock, err := flockDir(a, syscall.LOCK_EX)
	if err != nil {
		t.Fatal(err)
	}
	if rec := read(t, w, "/"+f.host+"/acct/b/"); rec.Code != http.StatusOK {
		t.Fatalf("b: %d\n%s", rec.Code, rec.Body)
	}
	if !exists(filepath.Join(a, ".git", "HEAD")) {
		t.Error("a went while another server held its lock")
	}
	unlock()
	if err := w.trim(""); err != nil {
		t.Fatal(err)
	}
	if exists(a) {
		t.Error("a stayed once its lock was free")
	}
}

// A path that names no repo lists the account's repos: from the forge's
// A public server shows a stranger nothing of the machine and nothing of
// what others read: no cache at / or in a listing, no path of the machine,
// no bug button, and it answers to any name.
func TestWebPublic(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	w.public, w.srv.public = true, true
	get := func(path string) *httptest.ResponseRecorder {
		return get(w.srv.handler(), path, "Host", "visitor.example.org")
	}
	rec := get("/" + f.host + "/acct/r/")
	if rec.Code != http.StatusAccepted || strings.Contains(rec.Body.String(), w.root) ||
		strings.Contains(rec.Body.String(), tildePath(w.root)) {
		t.Errorf("reading page on any host: %d\n%s", rec.Code, rec.Body)
	}
	read(t, w, "/"+f.host+"/acct/r/")
	if body := get("/").Body.String(); strings.Contains(body, "acct/r") || strings.Contains(body, "The cache") {
		t.Errorf("the root shows the cache:\n%s", body)
	}
	if rec := read(t, w, "/"+f.host+"/acct"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "acct/r") {
		t.Errorf("a listing falls back to the cache: %d\n%s", rec.Code, rec.Body)
	}
	if rec := get("/" + f.host + "/acct/r/README.md"); rec.Code != http.StatusOK {
		t.Errorf("a cached repo: %d\n%s", rec.Code, rec.Body)
	}
	if got := w.where(filepath.Join(w.root, "h", "a", "r", ".git")); got != "cache/h/a/r/.git" {
		t.Errorf("a cache path reads %s", got)
	}
	// git's error names the path it was given.
	depths[f.host] = 3
	t.Cleanup(func() { delete(depths, f.host) })
	if rec := read(t, w, "/"+f.host+"/acct/gone"); rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), w.root) {
		t.Errorf("a failed clone: %d\n%s", rec.Code, rec.Body)
	}
	old := bugOff
	bugOff = true
	t.Cleanup(func() { bugOff = old })
	if rec := do(w.srv.handler(), "POST", ownPath+"bug", "Host", "visitor.example.org"); rec.Code == http.StatusOK {
		t.Errorf("bug report: %d", rec.Code)
	}
}

// API where it answers, else from the cache.
func TestWebLists(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/users/org/repos" {
			w.Write([]byte(`[{"full_name":"org/tool","description":"a tool"}]`))
			return
		}
		http.NotFound(w, r)
	})
	f := newForge(t, api)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	read(t, w, "/"+f.host+"/acct/r/")

	rec := read(t, w, "/"+f.host+"/org")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `href="/`+f.host+`/org/tool/"`) ||
		!strings.Contains(body, "a tool") || !strings.Contains(body, "the Forgejo API") {
		t.Errorf("API listing: %d\n%s", rec.Code, body)
	}
	rec = read(t, w, "/"+f.host+"/acct")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `href="/`+f.host+`/acct/r/"`) ||
		!strings.Contains(body, "listed by the cache") {
		t.Errorf("cache listing: %d\n%s", rec.Code, body)
	}
	if rec := read(t, w, "/"+f.host+"/nobody"); rec.Code != http.StatusNotFound {
		t.Errorf("nothing there: %d\n%s", rec.Code, rec.Body)
	}
	// A failed clone leaves nothing in the cache. A forge that keeps
	// repos at one depth clones without a probe.
	depths[f.host] = 3
	t.Cleanup(func() { delete(depths, f.host) })
	if rec := read(t, w, "/"+f.host+"/acct/gone"); rec.Code != http.StatusBadGateway {
		t.Errorf("no repo: %d\n%s", rec.Code, rec.Body)
	}
	if exists(filepath.Join(w.root, f.host, "acct", "gone")) {
		t.Error("a failed read left its directory")
	}
}

func TestWebEndpoint(t *testing.T) {
	ep, _ := endpointFor("", 1001)
	if w, ok := webEndpoint(ep, 1001); !ok || w.String() != "127.31.1.2:"+ep.String()[len("127.31.1.1:"):] {
		t.Errorf("default: %v %v", w, ok)
	}
	other, _ := endpointFor("127.31.1.5", 1001)
	if _, ok := webEndpoint(other, 1001); ok {
		t.Error("a server on another address serves the web")
	}
}

// A link to a repo on a forge, or a file or directory in one, maps to its
// path on the web address; other pages of a forge, and other hosts, map
// to nothing.
func TestWebPath(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/junegunn/fzf":                             "/github.com/junegunn/fzf/",
		"https://www.github.com/junegunn/fzf.git":                     "/github.com/junegunn/fzf/",
		"https://github.com/junegunn/fzf/blob/master/README.md":       "/github.com/junegunn/fzf/README.md",
		"https://github.com/junegunn/fzf/tree/v0.50/shell":            "/github.com/junegunn/fzf/shell/",
		"https://github.com/junegunn/fzf/tree/master":                 "/github.com/junegunn/fzf/",
		"https://codeberg.org/forgejo/forgejo/src/branch/main/go.mod": "/codeberg.org/forgejo/forgejo/go.mod",
		"https://gitlab.com/a/b/c/-/blob/main/x.md":                   "/gitlab.com/a/b/c/x.md",
		"https://gitlab.com/a/b":                                      "/gitlab.com/a/b/",
		"https://bitbucket.org/a/b/src/main/x.md":                     "/bitbucket.org/a/b/x.md",
		"https://github.com/junegunn/fzf/issues/3":                    "",
		"https://github.com/junegunn":                                 "",
		"https://github.com/sponsors/junegunn":                        "",
		"https://example.com/a/b":                                     "",
		"ftp://github.com/a/b":                                        "",
		"https://github.com/a/../b":                                   "",
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

// The web address's index links back to the trees on the default address.
func TestWebLinksHome(t *testing.T) {
	w := testWeb(t, defaultDisk)
	if strings.Contains(webGet(w, "/").Body.String(), "Trees:") {
		t.Errorf("a link home without a home")
	}
	w.srv.homeURL = "http://127.31.1.1:8080"
	if body := webGet(w, "/").Body.String(); !strings.Contains(body, `Trees: <a href="http://127.31.1.1:8080/">127.31.1.1:8080/</a>`) {
		t.Errorf("no link home: %s", body)
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
