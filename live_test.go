package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// liveTree is serveTree with live reload, behind a real HTTP server so
// streams flush.
func liveTree(t *testing.T) (*server, *tree, string, *httptest.Server) {
	t.Helper()
	s, tr, dir := serveTree(t)
	s.live = newLive()
	t.Cleanup(s.live.close)
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	return s, tr, dir, ts
}

// stream opens the live stream of the page at urlPath as rendered now and
// returns the lines it sends.
func stream(t *testing.T, s *server, ts *httptest.Server, urlPath string) <-chan string {
	t.Helper()
	u := liveURL(s.withLive(httptest.NewRequest("GET", urlPath, nil)))
	return openStream(t, ts, u)
}

func openStream(t *testing.T, ts *httptest.Server, u string) <-chan string {
	t.Helper()
	resp, err := http.Get(ts.URL + u)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream served as %q", ct)
	}
	lines := make(chan string, 16)
	go func() {
		defer resp.Body.Close()
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	t.Cleanup(func() { resp.Body.Close() })
	return lines
}

// reloads reports whether the stream sends reload within d.
func reloads(lines <-chan string, d time.Duration) bool {
	end := time.After(d)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return false
			}
			if l == "data: reload" {
				return true
			}
		case <-end:
			return false
		}
	}
}

// The page carries the script, and the CSP allows that script alone.
func TestScriptAllowedByHash(t *testing.T) {
	s, _, _, _ := liveTree(t)
	rec := get(s.handler(), "/missing")
	m := regexp.MustCompile(`(?s)<script data-live="([^"]+)">(.*?)</script>`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no live script in page:\n%s", rec.Body.String())
	}
	sum := sha256.Sum256([]byte(m[2]))
	want := "script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	for _, d := range strings.Split(rec.Header().Get("Content-Security-Policy"), ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") && d != want {
			t.Errorf("CSP has %q, want %q", d, want)
		}
	}
	if !strings.Contains(m[1], "/_mdserve/live?path=%2Fmissing&amp;v=") {
		t.Errorf("stream URL %q", m[1])
	}
}

// A raw file is not a page and gets no script; nor does a server without
// live reload.
func TestNoScriptWithoutLive(t *testing.T) {
	s, _, _ := serveTree(t)
	if strings.Contains(get(s.handler(), "/missing").Body.String(), "<script") {
		t.Error("script without live reload")
	}
	s, _, _, _ = liveTree(t)
	if strings.Contains(get(s.handler(), "/a/page.html?raw").Body.String(), "data-live") {
		t.Error("script in a raw file")
	}
}

// A file written in place reloads its page; a file beside it does not.
func TestLiveReloadsOnWrite(t *testing.T) {
	s, _, dir, ts := liveTree(t)
	lines := stream(t, s, ts, "/a/b.go")
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "a", "other.txt"), []byte("x\n"), 0o644)
	if reloads(lines, 500*time.Millisecond) {
		t.Fatal("reload for another file")
	}
	os.WriteFile(filepath.Join(dir, "a", "b.go"), []byte("package b\n"), 0o644)
	if !reloads(lines, 2*time.Second) {
		t.Fatal("no reload after a write")
	}
}

// An editor that renames a new file over the old one still reloads the
// page, without a stop at the page that says the file is gone.
func TestLiveReloadsOnRenameSave(t *testing.T) {
	s, _, dir, ts := liveTree(t)
	lines := stream(t, s, ts, "/index.md")
	time.Sleep(50 * time.Millisecond)
	p := filepath.Join(dir, "index.md")
	os.Rename(p, p+"~")
	os.WriteFile(p+".new", []byte("# New\n"), 0o644)
	os.Rename(p+".new", p)
	os.Remove(p + "~")
	if !reloads(lines, 2*time.Second) {
		t.Fatal("no reload after a rename save")
	}
}

// A listing reloads when a file joins it, and not when a file in it
// changes, its README included.
func TestListingVersion(t *testing.T) {
	s, _, dir, _ := liveTree(t)
	version := func() string { return s.version("/a/") }
	v := version()
	os.WriteFile(filepath.Join(dir, "a", "b.go"), []byte("package b\n"), 0o644)
	if version() != v {
		t.Error("listing changed with a file's content")
	}
	os.WriteFile(filepath.Join(dir, "a", "README.md"), []byte("New readme\n"), 0o644)
	if version() != v {
		t.Error("listing changed with its README")
	}
	os.WriteFile(filepath.Join(dir, "a", "c.go"), nil, 0o644)
	if version() == v {
		t.Error("listing unchanged with a new file")
	}
	os.WriteFile(filepath.Join(dir, "a", "127.31.1.1-8080.lock"), nil, 0o644)
	os.Remove(filepath.Join(dir, "a", "c.go"))
	if version() != v {
		t.Error("lock in a listing counts")
	}
}

// A share hides dot entries, so they leave its listing's version alone.
func TestSharedListingVersionSkipsDots(t *testing.T) {
	s, _, dir, _ := liveTree(t)
	s.lookup("/a/").shared = true
	v := s.version("/a/")
	os.WriteFile(filepath.Join(dir, "a", ".b.go.swp"), nil, 0o644)
	if s.version("/a/") != v {
		t.Error("dot entry counts on a share")
	}
}

// A share keeps a private repo's commits from showing by a reload.
func TestSharedClosedRepoVersion(t *testing.T) {
	needGit(t)
	s, _, dir, _ := liveTree(t)
	exec.Command("git", "init", "-q", dir).Run()
	commitAll(t, dir, "First")
	os.Chmod(filepath.Join(dir, ".git"), 0o700)
	s.lookup("/a/").shared = true
	v := s.version("/a/")
	os.WriteFile(filepath.Join(dir, "new.txt"), nil, 0o644)
	commitAll(t, dir, "Second")
	if s.version("/a/") != v {
		t.Error("commit in a closed repo changes a shared page")
	}
}

// A page from an earlier server reloads at once, as after --restart.
func TestLiveReloadsForOtherInstance(t *testing.T) {
	s, _, _, ts := liveTree(t)
	u := liveURL(s.withLive(httptest.NewRequest("GET", "/a/b.go", nil)))
	u = strings.Replace(u, "v="+s.live.id, "v=earlier", 1)
	if !reloads(openStream(t, ts, u), time.Second) {
		t.Fatal("page from another instance kept")
	}
}

// A tree that leaves and comes back within goneWait reloads no page; one
// that stays gone reloads its pages, which then say so.
func TestLiveWaitsForGoneTree(t *testing.T) {
	old := goneWait
	goneWait = 500 * time.Millisecond
	t.Cleanup(func() { goneWait = old })
	s, tr, _, ts := liveTree(t)
	lines := stream(t, s, ts, "/a/b.go")
	time.Sleep(50 * time.Millisecond)
	s.remove(tr)
	time.Sleep(200 * time.Millisecond)
	s.add(tr)
	if reloads(lines, time.Second) {
		t.Fatal("reload for a tree that came back")
	}
	s.remove(tr)
	if !reloads(lines, 2*time.Second) {
		t.Fatal("no reload for a tree that stayed gone")
	}
}

// The index reloads when a tree joins.
func TestLiveIndexSeesTrees(t *testing.T) {
	s, _, _ := serveTree(t)
	s.trees[0].prefix = "/one/"
	s.live = newLive()
	t.Cleanup(s.live.close)
	ts := httptest.NewServer(s.handler())
	t.Cleanup(ts.Close)
	lines := stream(t, s, ts, "/")
	time.Sleep(50 * time.Millisecond)
	addTree(t, s, t.TempDir(), "/two/", "two")
	if !reloads(lines, 2*time.Second) {
		t.Fatal("index kept after a tree joined")
	}
}

// Without inotify live reload polls, and still sees a write.
func TestLivePolls(t *testing.T) {
	old := inotifyInit
	inotifyInit = func(int) (int, error) { return -1, syscall.EMFILE }
	t.Cleanup(func() { inotifyInit = old })
	s, _, dir, ts := liveTree(t)
	lines := stream(t, s, ts, "/a/b.go")
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "a", "b.go"), []byte("package b\n"), 0o644)
	if !reloads(lines, 3*time.Second) {
		t.Fatal("no reload while polling")
	}
}

// Closing live ends every stream without a reload, so Shutdown can finish.
func TestLiveCloseEndsStreams(t *testing.T) {
	s, _, _, ts := liveTree(t)
	lines := stream(t, s, ts, "/a/b.go")
	time.Sleep(50 * time.Millisecond)
	s.live.close()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return
			}
			if l == "data: reload" {
				t.Fatal("reload on close")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("stream still open after close")
		}
	}
}
