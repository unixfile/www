package www

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
	r.mu.Lock()
	dir := r.tree.dir
	r.mu.Unlock()
	if !strings.Contains(body, "two") || !strings.Contains(body, ">"+stardatesIn(context.Background(), dir).byHash[head]+"</a>") {
		t.Fatalf("not refreshed:\n%s", body)
	}
	if exists(old) {
		t.Errorf("the old worktree %s is still there", old)
	}
}

// A clone waits for a slot, and says so on its page.
func TestWebCloneWaitsForASlot(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	w.slots = make(chan struct{}, 1)
	w.slots <- struct{}{}
	path := "/" + f.host + "/acct/r/"
	rec := webGet(w, path)
	for end := time.Now().Add(10 * time.Second); !strings.Contains(rec.Body.String(), "waiting: 1 clones run at once") && time.Now().Before(end); {
		time.Sleep(20 * time.Millisecond)
		rec = webGet(w, path)
	}
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "waiting") {
		t.Fatalf("clone without a slot: %d\n%s", rec.Code, rec.Body)
	}
	<-w.slots
	if rec := read(t, w, path); rec.Code != http.StatusOK {
		t.Errorf("clone after a slot frees: %d\n%s", rec.Code, rec.Body)
	}
}
