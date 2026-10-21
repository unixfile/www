package main

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// shareTree builds a shared server with one tree at /docs/ and one under
// the token s3cret. The docs tree holds files other users can read, and
// some they cannot.
func shareTree(t *testing.T) *server {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "docs")
	hid := filepath.Join(base, "hid")
	files := []struct {
		path string
		mode os.FileMode
	}{
		{"docs/pub.txt", 0o644},
		{"docs/secret", 0o600},
		{"docs/open/x.txt", 0o644},
		{"docs/shut/y.txt", 0o644},
		{"hid/z.txt", 0o644},
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
	for _, d := range []string{dir, filepath.Join(dir, "open"), hid} {
		os.Chmod(d, 0o755)
	}
	os.Chmod(filepath.Join(dir, "shut"), 0o700)
	s := newServer()
	s.shared = true
	for _, tc := range []struct{ dir, token string }{{dir, ""}, {hid, "s3cret"}} {
		prefix, label := shareMount(tc.dir, tc.token)
		tr := addTree(t, s, tc.dir, prefix, label)
		tr.shared, tr.secret = true, tc.token != ""
	}
	return s
}

func TestShareServesWhatOthersCanRead(t *testing.T) {
	h := shareTree(t).handler()
	for p, want := range map[string]string{
		"/docs/pub.txt?raw":    "docs/pub.txt",
		"/docs/open/x.txt?raw": "docs/open/x.txt",
		"/s3cret/z.txt?raw":    "hid/z.txt",
	} {
		rec := get(h, p)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: status %d, body %q; want 200 with %q", p, rec.Code, rec.Body.String(), want)
		}
	}
	for p, want := range map[string]string{
		"/docs/secret?raw":     "Refused: secret",
		"/docs/shut/y.txt?raw": "Refused: shut/y.txt",
	} {
		rec := get(h, p)
		body := rec.Body.String()
		if rec.Code != http.StatusForbidden || !strings.Contains(body, want) {
			t.Errorf("%s: status %d, body %q; want 403 with %q", p, rec.Code, body, want)
		}
		if strings.Contains(body, "mode") || strings.Contains(body, string(filepath.Separator)+"tmp") {
			t.Errorf("%s: the refusal names a path or mode: %q", p, body)
		}
	}
}

func TestShareListingHidesReasons(t *testing.T) {
	h := shareTree(t).handler()
	body := get(h, "/docs/").Body.String()
	if !strings.Contains(body, `<li class="refused">secret <span`) || !strings.Contains(body, `<li class="refused">shut/ <span`) {
		t.Errorf("listing does not mark private entries as refused:\n%s", body)
	}
	if strings.Contains(body, "title=\"secret") || strings.Contains(body, ownPath+"stop") {
		t.Errorf("listing carries a reason or a stop button:\n%s", body)
	}
}

func TestShareIndexListsOnlyOpenTrees(t *testing.T) {
	h := shareTree(t).handler()
	body := get(h, "/").Body.String()
	if !strings.Contains(body, `<a href="/docs/">docs/</a>`) {
		t.Errorf("index does not list docs:\n%s", body)
	}
	if strings.Contains(body, "s3cret") || strings.Contains(body, "hid") || strings.Contains(body, ownPath+"stop") {
		t.Errorf("index shows the secret tree or a button:\n%s", body)
	}
}

func TestShareTakesAnyHostAndNoStop(t *testing.T) {
	h := shareTree(t).handler()
	if rec := get(h, "/docs/pub.txt?raw", "Host", "laptop.lan"); rec.Code != http.StatusOK {
		t.Errorf("Host laptop.lan: status %d, want 200", rec.Code)
	}
	if rec := do(h, "POST", ownPath+"stop"); rec.Code == http.StatusOK {
		t.Error("a shared server answered a stop")
	}
}

// A lock names the tree's path, so a share hides it.
func TestShareHidesLocks(t *testing.T) {
	s := shareTree(t)
	dir := s.byPrefix("/docs/").dir
	if err := os.WriteFile(filepath.Join(dir, "10.0.0.1-80.lock"), []byte("root: "+dir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := get(s.handler(), "/docs/10.0.0.1-80.lock?raw"); rec.Code != http.StatusNotFound {
		t.Errorf("lock: status %d, want 404", rec.Code)
	}
}

// .git holds the history and deleted files, so a share hides dot entries
// at any depth, while loopback shows them.
func TestShareHidesDotEntries(t *testing.T) {
	s := shareTree(t)
	dir := s.byPrefix("/docs/").dir
	for _, p := range []string{".git/HEAD", "open/.env", ".notes/n.md"} {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		os.Chmod(filepath.Dir(f), 0o755)
		if err := os.WriteFile(f, []byte(p+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(".git", filepath.Join(dir, "history")); err != nil {
		t.Fatal(err)
	}
	h := s.handler()
	for _, p := range []string{"/docs/.git/", "/docs/.git/HEAD?raw", "/docs/open/.env?raw", "/docs/.notes/n.md", "/docs/history/HEAD?raw"} {
		if rec := get(h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
	for _, p := range []string{"/docs/", "/docs/open/"} {
		body := get(h, p).Body.String()
		if strings.Contains(body, ".git") || strings.Contains(body, ".env") || strings.Contains(body, ".notes") || strings.Contains(body, "history") {
			t.Errorf("%s lists a dot entry:\n%s", p, body)
		}
	}
	s.byPrefix("/docs/").shared = false
	if rec := get(h, "/docs/.git/HEAD?raw"); rec.Code != http.StatusOK {
		t.Errorf("loopback .git/HEAD: status %d, want 200", rec.Code)
	}
}

func TestShareMount(t *testing.T) {
	for _, tc := range []struct{ dir, token, prefix, label string }{
		{"/home/u/scratch/notes", "", "/notes/", "notes"},
		{"/home/u/scratch/notes", "k3x9", "/k3x9/", "notes"},
		{"/", "", "/", "/"},
	} {
		if p, l := shareMount(tc.dir, tc.token); p != tc.prefix || l != tc.label {
			t.Errorf("shareMount(%q, %q) = %q, %q; want %q, %q", tc.dir, tc.token, p, l, tc.prefix, tc.label)
		}
	}
}

// -a takes loopback alone and --share everything else.
func TestShareEndpoint(t *testing.T) {
	for addr, want := range map[string]string{
		"192.168.1.5":    "192.168.1.5:8080",
		"0.0.0.0:9000":   "0.0.0.0:9000",
		"10.0.0.1:80":    "10.0.0.1:80",
		"127.31.1.1":     "",
		"::1":            "",
		"192.168.1.5:0":  "",
		"not-an-address": "",
	} {
		ep, err := shareEndpoint(addr)
		if want == "" {
			if err == nil {
				t.Errorf("%s: %v, want an error", addr, ep)
			}
			continue
		}
		if err != nil || ep != netip.MustParseAddrPort(want) {
			t.Errorf("%s: %v, %v; want %s", addr, ep, err, want)
		}
	}
	if _, err := endpointFor("192.168.1.5", 1001); err == nil {
		t.Error("-a took an address beyond loopback")
	}
}

// A registry link carries a shared tree's token after the lock path.
func TestSplitTarget(t *testing.T) {
	for target, want := range map[string][2]string{
		"/a/b/127.31.1.1-80.lock":        {"/a/b/127.31.1.1-80.lock", ""},
		"/a/b#c/10.0.0.1-80.lock":        {"/a/b#c/10.0.0.1-80.lock", ""},
		"/a/b#c/10.0.0.1-80.lock#k3x9_-": {"/a/b#c/10.0.0.1-80.lock", "k3x9_-"},
	} {
		if l, tok := splitTarget(target); l != want[0] || tok != want[1] {
			t.Errorf("splitTarget(%q) = %q, %q; want %q, %q", target, l, tok, want[0], want[1])
		}
	}
	if tok := newToken(); len(tok) != 26 || !validToken(tok) {
		t.Errorf("newToken() = %q, want 26 valid characters", tok)
	}
}

// Listeners come from /proc/net/tcp and tcp6, whose addresses the kernel
// prints in host order, little-endian here.
func TestListeners(t *testing.T) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("big-endian host")
	}
	table := `  sl  local_address rem_address   st
   0: 01011F7F:0050 00000000:0000 0A 00000000:00000000
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000
   2: 0100007F:0050 0100007F:9C40 01 00000000:00000000
   3: 00000000000000000000000000000000:0050 00000000000000000000000000000000:0000 0A 0
   4: 0000000000000000FFFF00000501A8C0:0051 00000000000000000000000000000000:0000 0A 0
`
	var got []string
	for _, l := range listeners(table) {
		got = append(got, l.String())
	}
	want := []string{"127.31.1.1:80", "0.0.0.0:8080", "[::]:80", "192.168.1.5:81"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("listeners: %v, want %v", got, want)
	}
}

// A bare --share takes the default route's address, which is never
// loopback; without a route it says to name one.
func TestShareDefaultRoute(t *testing.T) {
	ep, err := shareEndpoint("")
	if err != nil {
		if !strings.Contains(err.Error(), "--share=") {
			t.Errorf("no route: %v, want advice to name an address", err)
		}
		t.Skip("no default route: ", err)
	}
	if ep.Addr().IsLoopback() || ep.Port() != defaultPort {
		t.Errorf("shareEndpoint(\"\") = %v", ep)
	}
}

// A name resolves to an address this machine holds; localhost is loopback,
// which --share refuses.
func TestShareName(t *testing.T) {
	for _, addr := range []string{"localhost", "localhost:9000", "no-such-host.invalid", "localhost:0"} {
		if ep, err := shareEndpoint(addr); err == nil {
			t.Errorf("%s: %v, want an error", addr, ep)
		}
	}
}

// An address after a bare --share lands as the directory; the hint names
// the joined form, and a real directory or a plain typo gets none.
func TestSpacedShareHint(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		c    config
		hint bool
	}{
		{config{sharing: true, dir: "192.168.1.5"}, true},
		{config{sharing: true, dir: "192.168.1.5:9000"}, true},
		{config{sharing: true, dir: dir}, false},
		{config{sharing: true, dir: filepath.Join(dir, "no such dir")}, false},
		{config{sharing: true, share: "10.0.0.1", dir: "192.168.1.5"}, false},
		{config{dir: "192.168.1.5"}, false},
	} {
		got := spacedShare(tc.c)
		if (got != "") != tc.hint || tc.hint && !strings.Contains(got, "--share="+tc.c.dir) {
			t.Errorf("%+v: %q", tc.c, got)
		}
	}
}

// WWW_SHARE fills a bare --share and WWW_ADDR a start without -a.
// Flags win, --list, --kill and --restart keep covering every server, and
// a bad value names its variable.
func TestEnvironmentDefaults(t *testing.T) {
	t.Setenv(shareEnv, "10.9.8.7:81")
	t.Setenv(addrEnv, "127.31.9.9:82")
	for _, tc := range []struct {
		c    config
		want string
	}{
		{config{sharing: true}, "10.9.8.7:81"},
		{config{sharing: true, share: "10.0.0.1:80"}, "10.0.0.1:80"},
		{config{sharing: true, kill: true}, "10.9.8.7:81"},
		{config{dir: "."}, "127.31.9.9:82"},
		{config{dir: ".", addr: "127.31.1.2:80"}, "127.31.1.2:80"},
		{config{list: true}, "127.31.0.1:" + strconv.Itoa(int(defaultPort))},
	} {
		ep, err := endpointOf(tc.c, 1000)
		if err != nil || ep.String() != tc.want {
			t.Errorf("%+v: %v, %v; want %s", tc.c, ep, err, tc.want)
		}
	}
	t.Setenv(addrEnv, "10.0.0.1")
	if _, err := endpointOf(config{dir: "."}, 1000); err == nil || !strings.HasPrefix(err.Error(), addrEnv+"=10.0.0.1: ") {
		t.Errorf("non-loopback "+envPrefix+"ADDR: %v, want an error naming it", err)
	}
}

// A share's search names no dot entry, as its listing shows none.
func TestShareSearchHidesDotEntries(t *testing.T) {
	s := shareTree(t)
	dir := s.lookup("/docs/").dir
	os.WriteFile(filepath.Join(dir, ".envkey"), []byte("env\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "open", ".envdir"), 0o755)
	os.WriteFile(filepath.Join(dir, "open", ".envhidden"), []byte("env\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "open", "env.txt"), []byte("env\n"), 0o644)
	var res found
	json.Unmarshal(get(s.handler(), "/docs/?search=env").Body.Bytes(), &res)
	var names []string
	for _, h := range res.Names {
		names = append(names, h.Path)
	}
	if strings.Join(names, " ") != "open/env.txt" {
		t.Errorf("names: %v", names)
	}
}
