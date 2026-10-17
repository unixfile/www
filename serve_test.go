package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEndpointFor(t *testing.T) {
	for _, c := range []struct {
		addr string
		uid  int
		want string
	}{
		{"", 1000, "127.31.0.1:8080"},
		{"", 1001, "127.31.1.1:8080"},
		{"", 1255, "127.31.255.1:8080"},
		{"127.31.1.2", 1001, "127.31.1.2:8080"},
		{"127.31.1.2:9000", 0, "127.31.1.2:9000"},
	} {
		ep, err := endpointFor(c.addr, c.uid)
		if err != nil || ep.String() != c.want {
			t.Errorf("%q uid %d: %v %v, want %s", c.addr, c.uid, ep, err, c.want)
		}
	}
	for _, c := range []struct {
		addr string
		uid  int
	}{{"", 999}, {"", 1256}, {"", 0}, {"192.168.1.5", 1001}, {"::1", 1001}, {"127.31.1.2:0", 1001}, {"docs", 1001}} {
		if ep, err := endpointFor(c.addr, c.uid); err == nil {
			t.Errorf("%q uid %d: %v, want refusal", c.addr, c.uid, ep)
		}
	}
}

func TestCheckDirRefusesHome(t *testing.T) {
	home := t.TempDir()
	os.Mkdir(filepath.Join(home, "docs"), 0o755)
	os.Symlink(home, filepath.Join(t.TempDir(), "h"))
	if _, err := checkDir(home, home); errnoOf(err) != 1 {
		t.Errorf("home: %v, want EPERM", err)
	}
	if _, err := checkDir(filepath.Join(home, "docs"), home); err != nil {
		t.Errorf("below home: %v", err)
	}
	if _, err := checkDir(filepath.Join(home, "missing"), home); err == nil {
		t.Error("missing directory accepted")
	}
}

// The watcher wakes on a change in a watched directory.
func TestWatcherWakes(t *testing.T) {
	w := newWatcher(lockMask)
	defer w.close()
	dir := t.TempDir()
	if w.add(dir) < 0 {
		t.Skip("inotify unavailable")
	}
	os.WriteFile(filepath.Join(dir, "x"), nil, 0o644)
	select {
	case <-w.wake:
	case <-time.After(2 * time.Second):
		t.Fatal("no wake after a file was created")
	}
}

// testEndpoint returns a free port on an address no real server uses, and
// keeps the registry inside the test.
func testEndpoint(t *testing.T) netip.AddrPort {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.31.250.1:0")
	if err != nil {
		t.Skip("cannot listen on 127.31.250.1:", err)
	}
	ep := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()
	return ep
}

// start runs mdserve for dir as a detached child would and returns its
// report lines once it finished reporting, and a channel with its exit code.
func start(t *testing.T, c config) ([]string, chan int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	code := make(chan int, 1)
	go func() { code <- run(c, &reporter{pipe: w}) }()
	b, _ := io.ReadAll(bufio.NewReader(r))
	r.Close()
	return strings.Split(strings.TrimSpace(string(b)), "\n"), code
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out waiting for " + what)
}

func status(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A second tree joins the running server; each lock withdraws its tree, and
// the server exits with the last one.
func TestTreesShareOneServer(t *testing.T) {
	ep := testEndpoint(t)
	one, two := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(one, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(two, "b.txt"), []byte("two\n"), 0o644)
	base := "http://" + ep.String()

	lines, served := start(t, config{dir: one, ep: ep})
	if !strings.HasPrefix(lines[0], "url "+base+one+"/") {
		t.Fatalf("server report: %q", lines)
	}
	lines, joined := start(t, config{dir: two, ep: ep})
	if code := <-joined; code != 0 {
		t.Fatalf("join failed with %d: %q", code, lines)
	}
	if !strings.HasPrefix(lines[0], "url "+base+two+"/") {
		t.Fatalf("join report: %q", lines)
	}
	lock1, lock2 := filepath.Join(one, lockName(ep)), filepath.Join(two, lockName(ep))
	if b, _ := os.ReadFile(lock2); parseLock(b).pid != os.Getpid() {
		t.Errorf("joined lock does not name the server: %q", b)
	}
	for _, p := range []string{one + "/a.txt?raw", two + "/b.txt?raw"} {
		if code := status(t, base+p); code != http.StatusOK {
			t.Errorf("%s: status %d", p, code)
		}
	}
	lines, again := start(t, config{dir: two, ep: ep})
	if <-again != 0 || lines[0] != "url "+base+two+"/" {
		t.Errorf("second run in a served tree: %q", lines)
	}

	os.Remove(lock1)
	waitFor(t, "the first tree to go", func() bool { return status(t, base+one+"/a.txt?raw") == http.StatusNotFound })
	if code := status(t, base+two+"/b.txt?raw"); code != http.StatusOK {
		t.Fatalf("second tree went with the first: status %d", code)
	}
	reg := filepath.Join(os.Getenv("XDG_STATE_HOME"), progName, endpointName(ep))
	if _, err := os.Lstat(filepath.Join(reg, linkName(one))); err == nil {
		t.Error("withdrawn tree keeps its registry link")
	}

	os.Remove(lock2)
	select {
	case code := <-served:
		if code != 0 {
			t.Errorf("server exited with %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server outlived its last lock")
	}
	if entries, _ := os.ReadDir(reg); len(entries) != 0 {
		t.Errorf("registry not empty after exit: %v", entries)
	}
}

// A connection that never sends a request does not hold the server past
// its last lock. Browsers open such connections ahead of need, and so may
// a client's pool, as in TestTreesShareOneServer.
func TestSilentConnectionDoesNotHoldServer(t *testing.T) {
	ep := testEndpoint(t)
	dir := t.TempDir()
	_, served := start(t, config{dir: dir, ep: ep})
	c, err := net.Dial("tcp", ep.String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(50 * time.Millisecond)
	os.Remove(filepath.Join(dir, lockName(ep)))
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("a connection without a request held the server")
	}
}

// Without inotify the server polls, and still sees a deleted lock.
func TestPollingFallback(t *testing.T) {
	ep := testEndpoint(t)
	old := inotifyInit
	inotifyInit = func(int) (int, error) { return -1, syscall.EMFILE }
	t.Cleanup(func() { inotifyInit = old })
	dir := t.TempDir()
	_, served := start(t, config{dir: dir, ep: ep})
	os.Remove(filepath.Join(dir, lockName(ep)))
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("polling server outlived its lock")
	}
}

// A server starting on an endpoint clears links and locks a crash left.
func TestStartDropsLeftovers(t *testing.T) {
	ep := testEndpoint(t)
	dir, stale := t.TempDir(), t.TempDir()
	reg, err := openRegistry(ep)
	if err != nil {
		t.Fatal(err)
	}
	staleLock := filepath.Join(stale, lockName(ep))
	os.WriteFile(staleLock, []byte("url: x\npid: 1\n"), 0o644)
	reg.add(staleLock, "")
	reg.close()

	_, served := start(t, config{dir: dir, ep: ep})
	if _, err := os.Stat(staleLock); err == nil {
		t.Error("stale lock survived the start")
	}
	links, _ := (&registry{dir: reg.dir}).links()
	if len(links) != 1 {
		t.Errorf("registry holds %v, want only the new tree", links)
	}
	os.Remove(filepath.Join(dir, lockName(ep)))
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("server outlived its lock")
	}
}

// The tree's stop button withdraws it, which stops a server with one tree.
func TestStopButtonEndsLastTree(t *testing.T) {
	ep := testEndpoint(t)
	dir := t.TempDir()
	_, served := start(t, config{dir: dir, ep: ep})
	resp, err := http.PostForm("http://"+ep.String()+ownPath+"stop", map[string][]string{"tree": {dir + "/"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("server kept running after its last tree was stopped")
	}
	if _, err := os.Stat(filepath.Join(dir, lockName(ep))); err == nil {
		t.Error("lock left behind")
	}
}
