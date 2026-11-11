package www

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

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

// A listing from the cache names the cached repos without measuring them.
// Measuring reads every file of every repo: on a cache of some GiB a
// host's page, eg /github.com/, stayed on its reading page for over a
// minute. A public server's front page measures nothing either.
func TestWebListsTheCacheWithoutMeasuringIt(t *testing.T) {
	f := newForge(t, nil)
	f.repo(t, "acct/r", map[string]string{"README.md": "x\n"})
	w := testWeb(t, defaultDisk)
	read(t, w, "/"+f.host+"/acct/r/")
	// The read's own trim measures the cache; it has ended with the job.
	w.wg.Wait()
	measure := diskUse
	var measured atomic.Int32
	diskUse = func(dir string) int64 {
		measured.Add(1)
		return measure(dir)
	}
	t.Cleanup(func() {
		w.wg.Wait()
		diskUse = measure
	})
	for _, p := range []string{"/" + f.host + "/acct", "/" + f.host} {
		rec := read(t, w, p)
		if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `href="/`+f.host+`/acct/r/"`) ||
			!strings.Contains(body, "listed by the cache") {
			t.Errorf("%s: %d\n%s", p, rec.Code, body)
		}
	}
	w.wg.Wait()
	if n := measured.Load(); n != 0 {
		t.Errorf("listing from the cache measured %d directories", n)
	}
	w.public, w.srv.public = true, true
	if rec := webGet(w, "/"); rec.Code != http.StatusOK {
		t.Errorf("a public front page: %d", rec.Code)
	}
	if n := measured.Load(); n != 0 {
		t.Errorf("a public front page measured %d directories", n)
	}
}
