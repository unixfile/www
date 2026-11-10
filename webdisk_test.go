package www

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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
