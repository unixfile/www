package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Delete lock on the index returns to the index while other trees remain;
// the last tree's button ends on the stopped page.
func TestIndexDeleteReturns(t *testing.T) {
	s, tr, dir := serveTree(t)
	tr.prefix, tr.label = "/notes/", "~/notes"
	addTree(t, s, filepath.Join(dir, "a"), "/other/", "~/other")
	var got []*tree
	s.stop = func(ts []*tree, _ string) {
		got = ts
		for _, t := range ts {
			s.remove(t)
		}
	}
	post := func(form string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", ownPath+"stop", strings.NewReader(form))
		req.Host = "127.31.1.1:8080"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		s.handler().ServeHTTP(rec, req)
		return rec
	}
	rec := post("tree=%2Fnotes%2F&from=index")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || len(got) != 1 || got[0] != tr {
		t.Errorf("first delete: status %d location %q stopped %d", rec.Code, rec.Header().Get("Location"), len(got))
	}
	rec = post("tree=%2Fother%2F&from=index")
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "<th>stopped</th><td><span class=\"seg\">~/</span><wbr><span class=\"seg\">other</span></td>") ||
		!strings.Contains(rec.Body.String(), "<th>server</th><td>stopped; close this tab</td>") || !strings.Contains(rec.Body.String(), `<span>410</span>`) {
		t.Errorf("last delete: status %d: %s", rec.Code, rec.Body.String())
	}
	// The server closes as this page leaves, so the page carries its style.
	if body := rec.Body.String(); !strings.Contains(body, "<style>") || strings.Contains(body, ownPath+"style.") {
		t.Errorf("stopped page links its stylesheet instead of carrying it")
	}
}

// The index at / lists every served tree with a button that deletes its
// lock, and offers to stop them all.
func TestIndexListsTrees(t *testing.T) {
	s, tr, dir := serveTree(t)
	tr.prefix, tr.label = "/notes/", "~/notes"
	addTree(t, s, filepath.Join(dir, "a"), "/other/", "~/other")
	rec := get(s.handler(), "/")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `<a href="/notes/" title=""><span class="seg">~/</span><wbr><span class="seg">notes</span></a>`) || !strings.Contains(body, `<a href="/other/" title=""><span class="seg">~/</span><wbr><span class="seg">other</span></a>`) {
		t.Errorf("status %d, index lacks the trees: %s", rec.Code, body)
	}
	if !strings.Contains(body, `<button class="kill">Kill server</button>`) {
		t.Errorf("index lacks the button that stops every tree: %s", body)
	}
	if !strings.Contains(body, `name="tree" value="/notes/"><input type="hidden" name="from" value="index"><button>Delete lock</button>`) {
		t.Errorf("index lacks a Delete lock button per tree: %s", body)
	}
	if rec := get(s.handler(), "/elsewhere/"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `href="/notes/"`) {
		t.Errorf("path outside every tree: status %d, want 404 with the index", rec.Code)
	}
}

// The index lists every server in the order of their addresses, each
// with its binary, so it reads the same from any of them. Only this
// server's trees carry buttons; another's address links to its index.
// The stale warning's command names the binary by path when PATH leads
// to another binary.
func TestIndexListsEveryServer(t *testing.T) {
	ep := testEndpoint(t)
	ln, err := net.Listen("tcp", "127.31.250.1:0")
	if err != nil {
		t.Skip(err)
	}
	other := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()
	dir := t.TempDir()
	_, served := start(t, config{dir: dir, ep: other})
	defer func() {
		os.Remove(filepath.Join(dir, lockName(other)))
		<-served
	}()

	s, tr, _ := serveTree(t)
	tr.prefix, tr.label = "/notes/", "~/notes"
	s.about = about{ep: ep, started: time.Now(), exe: "/opt/other/" + progName}
	body := get(s.handler(), "/").Body.String()
	own := "<h2 class=\"own\">" + ep.String() + ", this page</h2>\n<p class=\"own\"><code>" + segments(s.about.exe) + "</code>"
	theirs := "<h2 class=\"own\"><a href=\"" + origin(other) + "/\">" + other.String() + "</a></h2>"
	i, j := strings.Index(body, own), strings.Index(body, theirs)
	if i < 0 || j < 0 {
		t.Fatalf("index lacks a server: %s", body)
	}
	if (ep.Compare(other) < 0) != (i < j) {
		t.Errorf("servers out of address order: %s", body)
	}
	if strings.Count(body, "<button>Delete lock</button>") != 1 {
		t.Errorf("want one Delete lock button, for this server's tree: %s", body)
	}
	if !strings.Contains(body, `<a href="`+origin(other)+dir+`/" title="`+filepath.Join(dir, lockName(other))+`">`+segments(dir)+"</a>") {
		t.Errorf("index lacks the other server's tree: %s", body)
	}
	if got, want := s.about.restartCmd(), s.about.exe+" --restart"; got != want {
		t.Errorf("restart command %q, want %q", got, want)
	}
}

// A server's build and start show on the index only with --debug; they
// serve debugging alone.
func TestIndexTimesOnlyWithStamp(t *testing.T) {
	defer func() { debugOn = false }()
	b := indexServer{binary: "/usr/bin/" + progName, built: time.Now(), commit: "1f2241d", since: time.Now()}
	for _, show := range []bool{false, true} {
		debugOn = show
		got := b.account()
		if has := strings.Contains(got, "built ") && strings.Contains(got, "up since "); has != show {
			t.Errorf("stamp %t: %s", show, got)
		}
		if !strings.Contains(got, progName) {
			t.Errorf("stamp %t: the binary is missing: %s", show, got)
		}
	}
	debugOn = false
	s, _, _ := serveTree(t)
	if body := get(s.handler(), "/").Body.String(); strings.Contains(body, "since ") || strings.Contains(body, "class=\"since") {
		t.Errorf("index without --debug shows times: %s", body)
	}
}
