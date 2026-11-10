package www

import (
	"net/http"
	"path/filepath"
	"strings"
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
