package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
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
	// The header names the commit by its stardate, and says nothing of
	// the fetch until one fails.
	r := w.repos[f.host+"/acct/docs"]
	star := stardatesIn(context.Background(), r.tree.dir).byHash[head]
	if star == "" {
		t.Fatal("the repo's head has no stardate")
	}
	if body := rec.Body.String(); !strings.Contains(body, `@ <a href="/`+f.host+`/acct/docs@`+star+`/guide/a.md" title="`+head[:7]+` `) || !strings.Contains(body, `">`+star+"</a></span>") {
		t.Errorf("no stardate in the header:\n%s", body)
	}
	r.mu.Lock()
	fetched := r.fetched
	r.fetched, r.tried, r.failed = time.Now(), time.Now(), "no route"
	r.mu.Unlock()
	if body := webGet(w, path).Body.String(); !strings.Contains(body, star+"</a> · fetched "+clock(time.Now())+" · fetch at ") {
		t.Errorf("a failed fetch hides when the copy was fetched:\n%s", body)
	}
	r.mu.Lock()
	r.fetched, r.failed = fetched, ""
	r.mu.Unlock()
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

// A path that names no repo lists the account's repos: from the forge's
// A public server shows a stranger nothing of the machine and nothing of
// what others read: no cache at / or in a listing, no path of the machine,
// no bug button, and it answers to any name.
func TestWebPublic(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	w.public, w.srv.public = true, true
	// The forge listens on loopback, which a public server's listings
	// refuse; here it stands for the open internet.
	open := reachable
	reachable = func(netip.Addr) bool { return true }
	t.Cleanup(func() { reachable = open })
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

// A public server counts a visitor by address, or, behind a proxy on
// loopback, by the address the proxy appended; IPv6 by its /64.
func TestVisitor(t *testing.T) {
	for _, c := range []struct{ remote, xff, want string }{
		{"198.51.100.7:4000", "", "198.51.100.7/32"},
		{"198.51.100.7:4000", "203.0.113.9", "198.51.100.7/32"},
		{"127.0.0.1:4000", "203.0.113.9", "203.0.113.9/32"},
		{"127.0.0.1:4000", "10.0.0.1, 203.0.113.9", "203.0.113.9/32"},
		{"127.0.0.1:4000", "", "127.0.0.1/32"},
		{"[::1]:4000", "2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:3:4:5:6]:4000", "", "2001:db8:1:2::/64"},
		{"[::ffff:198.51.100.7]:4000", "", "198.51.100.7/32"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := visitor(r).String(); got != c.want {
			t.Errorf("%s with %q: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

// A public server lets one address start visitorJobs reads an hour, one
// at a time; another address and a cached repo are not limited.
func TestWebPublicLimits(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	depths[f.host] = 3
	t.Cleanup(func() { delete(depths, f.host) })
	w := testWeb(t, defaultDisk)
	w.public, w.srv.public = true, true
	from := func(addr, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		w.srv.handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := read(t, w, "/"+f.host+"/acct/r/"); rec.Code != http.StatusOK {
		t.Fatalf("first read: %d\n%s", rec.Code, rec.Body)
	}
	for i := 1; i < visitorJobs; i++ {
		if rec := read(t, w, fmt.Sprintf("/%s/acct/gone%d", f.host, i)); rec.Code != http.StatusBadGateway {
			t.Fatalf("read %d: %d\n%s", i, rec.Code, rec.Body)
		}
	}
	rec := webGet(w, "/"+f.host+"/acct/one-more")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "20 reads") {
		t.Errorf("read over the limit: %d, Retry-After %q\n%s", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
	}
	if rec := webGet(w, "/"+f.host+"/acct/r/README.md"); rec.Code != http.StatusOK {
		t.Errorf("a cached repo over the limit: %d", rec.Code)
	}
	if rec := from("198.51.100.7:4000", "/"+f.host+"/acct/one-more"); rec.Code != http.StatusAccepted {
		t.Errorf("another address: %d\n%s", rec.Code, rec.Body)
	}
	// A read still running holds the address's next.
	busy, _ := netip.MustParseAddr("203.0.113.9").Prefix(32)
	w.mu.Lock()
	w.visitors[busy] = []*webJob{{urlPath: "/" + f.host + "/acct/slow", started: time.Now()}}
	w.mu.Unlock()
	if rec := from("203.0.113.9:4000", "/"+f.host+"/acct/other"); rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "acct/slow is still being read") {
		t.Errorf("a second read at once: %d\n%s", rec.Code, rec.Body)
	}
}

// A public server reaches no address of the machine or its network, by
// an address or by a name, and holds git to the addresses it checked.
func TestWebPublicReach(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	w.public, w.srv.public = true, true
	publicNet.Store(true)
	t.Cleanup(func() { publicNet.Store(false) })
	clear(reachCache)
	_, port, _ := net.SplitHostPort(f.host)
	for _, p := range []string{"/" + f.host + "/acct/r/", "/localhost:" + port + "/acct/r/", "/169.254.169.254/latest/meta-data", "/[::1]:" + port + "/acct/r/"} {
		if rec := read(t, w, p); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "not on the open internet") {
			t.Errorf("%s: %d\n%s", p, rec.Code, rec.Body)
		}
	}
	if _, err := publicClient.Get("http://" + f.host + "/"); err == nil || !strings.Contains(err.Error(), "not on the open internet") {
		t.Errorf("listing client reached loopback: %v", err)
	}
	// Where loopback counts as the open internet, git reaches the forge
	// by its name at the address checked.
	old := reachable
	reachable = func(netip.Addr) bool { return true }
	t.Cleanup(func() { reachable = old })
	clear(reachCache)
	w2 := testWeb(t, defaultDisk)
	w2.public, w2.srv.public = true, true
	depths["localhost:"+port] = 3
	t.Cleanup(func() { delete(depths, "localhost:"+port) })
	if rec := read(t, w2, "/localhost:"+port+"/acct/r/README.md"); rec.Code != http.StatusOK {
		t.Errorf("a forge by name: %d\n%s", rec.Code, rec.Body)
	}
	if args, _ := reach(context.Background(), "localhost:"+port); !slices.Contains(args, "http.followRedirects=false") ||
		!slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "http.curloptResolve=localhost:"+port+":") }) {
		t.Errorf("git's options: %q", args)
	}
}

// A public repo shows nothing of the machine: not its .git, which names
// the cache, nor where a link out leads, which it never follows.
func TestWebPublicHidesTheMachine(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	work := f.work["acct/r"]
	for name, to := range map[string]string{"up": "../../../../../..", "etc": "/etc", "g": ".git", "gc": ".git/../.git"} {
		if err := os.Symlink(to, filepath.Join(work, name)); err != nil {
			t.Fatal(err)
		}
	}
	commitAll(t, work, "links")
	if out, err := exec.Command("git", "-C", work, "push", "-q", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git push: %v\n%s", err, out)
	}
	w := testWeb(t, defaultDisk)
	w.public, w.srv.public = true, true
	// A marker above the cache would open a link out on a reader's
	// server, but not on a public one.
	touch(filepath.Join(filepath.Dir(w.root), followMarker))
	read(t, w, "/"+f.host+"/acct/r/")
	for _, p := range []string{"", ".git", "g", "gc", "up/", "etc/", "etc/passwd", "up/?log"} {
		rec := get(w.srv.handler(), "/"+f.host+"/acct/r/"+p, "Host", "visitor.example.org")
		body := rec.Body.String()
		if strings.Contains(body, filepath.Dir(w.root)) || strings.Contains(body, "gitdir") || strings.Contains(body, "root:") {
			t.Errorf("%s shows the machine: %d\n%s", p, rec.Code, body)
		}
		if p == "" && strings.Contains(body, ">.git<") {
			t.Errorf("the listing shows .git:\n%s", body)
		}
	}
}

// A missing path in a public repo gets the 404 coin, not a word about
// the cache.
func TestWebPublicMissingPathShowsTheCoin(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	w.public, w.srv.public = true, true
	read(t, w, "/"+f.host+"/acct/r/")
	for _, p := range []string{"nope.md", "dir/nope.go", "nope/", "nope.md?log", "nope.md?raw", "nope.md?diff", "nope.md?tree", "README.md/nope"} {
		rec := get(w.srv.handler(), "/"+f.host+"/acct/r/"+p, "Host", "visitor.example.org")
		body := rec.Body.String()
		if rec.Code != http.StatusNotFound || !strings.Contains(body, `class="coin"`) || strings.Contains(strings.ToLower(body), "cache") {
			t.Errorf("%s: %d, want the 404 coin and no cache\n%s", p, rec.Code, body)
		}
	}
}
