package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkTree builds a tree with links into a marked store and into an
// unmarked directory, and returns a server holding it at /.
func linkTree(t *testing.T) *server {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "tree")
	store := filepath.Join(base, "store")
	other := filepath.Join(base, "other")
	files := []struct {
		path string
		mode os.FileMode
	}{
		{"tree/index.txt", 0o644},
		{"store/" + followMarker, 0o644},
		{"store/pub.txt", 0o644},
		{"store/secret", 0o600},
		{"store/sub/x.txt", 0o644},
		{"store/sub/hidden", 0o600},
		{"store/priv/f", 0o644},
		{"other/y.txt", 0o644},
	}
	for _, f := range files {
		p := filepath.Join(base, f.path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.path+"\n"), f.mode); err != nil {
			t.Fatal(err)
		}
		os.Chmod(p, f.mode)
	}
	for _, d := range []string{dir, store, filepath.Join(store, "sub"), other} {
		os.Chmod(d, 0o755)
	}
	os.Chmod(filepath.Join(store, "priv"), 0o700)
	links := map[string]string{
		"pub.txt": filepath.Join(store, "pub.txt"),
		"secret":  filepath.Join(store, "secret"),
		"sub":     filepath.Join(store, "sub"),
		"priv":    filepath.Join(store, "priv"),
		"nomark":  filepath.Join(other, "y.txt"),
		"back":    filepath.Join(dir, "index.txt"),
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	s := newServer()
	addTree(t, s, dir, "/", "tree")
	return s
}

func TestLinksFollowIntoMarkedStore(t *testing.T) {
	h := linkTree(t).handler()
	for p, want := range map[string]string{
		"/pub.txt?raw":   "store/pub.txt",
		"/sub/x.txt?raw": "store/sub/x.txt",
		"/back?raw":      "tree/index.txt",
	} {
		rec := get(h, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: status %d, body %q; want 200 with %q", p, rec.Code, rec.Body.String(), want)
		}
	}
	if rec := get(h, "/sub/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "x.txt") {
		t.Errorf("/sub/: status %d, want a listing with x.txt", rec.Code)
	}
}

func TestLinksRefusedSayWhy(t *testing.T) {
	h := linkTree(t).handler()
	for p, want := range map[string]string{
		"/secret?raw":     "Other users cannot read it (mode 600)",
		"/priv/f?raw":     "Other users cannot read it (mode 700)",
		"/sub/hidden?raw": "hidden (mode 600)",
		"/nomark?raw":     "Create it in the target&#39;s directory or one above it.",
	} {
		rec := get(h, p)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: status %d, body %q; want 403 with %q", p, rec.Code, rec.Body.String(), want)
		}
		if strings.Contains(rec.Body.String(), `class="coin"`) {
			t.Errorf("%s: a refusal turns the 404 coin", p)
		}
	}
}

func TestListingMarksRefusedLinks(t *testing.T) {
	h := linkTree(t).handler()
	body := get(h, "/").Body.String()
	for _, name := range []string{"secret", "priv/", "nomark"} {
		if !strings.Contains(body, `<li class="refused"><a href="`+name+`"`) {
			t.Errorf("listing does not mark %s as refused", name)
		}
	}
	for _, name := range []string{"pub.txt", "sub/", "back"} {
		if !strings.Contains(body, `<li><a href="`+name+`">`) {
			t.Errorf("listing marks %s, which is served", name)
		}
	}
	if sub := get(h, "/sub/").Body.String(); !strings.Contains(sub, `<li class="refused"><a href="hidden"`) {
		t.Error("listing of a followed directory does not mark a private file")
	}
}
