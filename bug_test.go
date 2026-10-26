package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postBug sends a bug report to h as the bug button does.
func postBug(h http.Handler, form url.Values, site string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", ownPath+"bug", strings.NewReader(form.Encode()))
	req.Host = "127.31.1.1:8080"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", site)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Every page's foot holds the bug button. A report gets the next number,
// one line in the log, N.kv and N.html, its stylesheet inline; a comment
// joins N.kv and the log. A deleted bug's number is not given again.
func TestBugReport(t *testing.T) {
	useJournal(t, "")
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, progName, "bugs")
	form := url.Values{
		"url":  {"http://127.31.1.1:8080/nowhere.md"},
		"note": {"the coin\nis cut off"},
		"view": {"1280x800+0"},
		"ua":   {"Brave"},
		"page": {`<!doctype html><html><head><link rel="stylesheet" href="` + styleURL + `"></head><body>seen</body></html>`},
	}
	s, _, _ := serveTree(t)
	h := s.handler()
	if !strings.Contains(get(h, "/").Body.String(), `<p class="bug"><button class="bug" data-post="`+ownPath+`bug"`) {
		t.Errorf("no bug button at the foot")
	}
	if rec := postBug(h, form, "cross-site"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site report: status %d, want 403", rec.Code)
	}
	rec := postBug(h, form, "same-origin")
	if rec.Code != http.StatusOK || rec.Body.String() != "1" {
		t.Fatalf("report: status %d body %q, want 200 and 1", rec.Code, rec.Body.String())
	}
	b, _ := os.ReadFile(filepath.Join(dir, "1.html"))
	if !strings.Contains(string(b), "<style>") || strings.Contains(string(b), styleURL) || !strings.Contains(string(b), "seen") {
		t.Errorf("snapshot lacks the inline stylesheet or the page: %.200s", b)
	}
	kv, _ := os.ReadFile(filepath.Join(dir, "1.kv"))
	for _, want := range []string{"url http://127.31.1.1:8080/nowhere.md\n", `note the coin\nis cut off` + "\n", "view 1280x800+0\n", "ua Brave\n", "build " + buildCommit + "\n", "time 20"} {
		if !strings.Contains(string(kv), want) {
			t.Errorf("1.kv lacks %q:\n%s", want, kv)
		}
	}
	if rec := postBug(h, url.Values{"bug": {"1"}, "comment": {"after a restart"}}, "same-origin"); rec.Code != http.StatusNoContent {
		t.Errorf("comment: status %d", rec.Code)
	}
	postBug(h, url.Values{"bug": {"1"}, "comment": {"again"}}, "same-origin")
	if kv, _ := os.ReadFile(filepath.Join(dir, "1.kv")); !strings.HasSuffix(string(kv), "comment.0 after a restart\ncomment.1 again\n") {
		t.Errorf("comments not in 1.kv:\n%s", kv)
	}
	for _, n := range []string{"9", "../next", "0"} {
		if rec := postBug(h, url.Values{"bug": {n}, "comment": {"x"}}, "same-origin"); rec.Code == http.StatusNoContent {
			t.Errorf("comment on bug %q taken", n)
		}
	}
	os.Remove(filepath.Join(dir, "1.kv"))
	if rec := postBug(h, form, "same-origin"); rec.Body.String() != "2" {
		t.Errorf("after deleting bug 1, got number %q, want 2", rec.Body.String())
	}
	path, _ := logFile()
	log, _ := os.ReadFile(path)
	for _, want := range []string{
		`bug 1: http://127.31.1.1:8080/nowhere.md note="the coin\nis cut off" view=1280x800+0 ua="Brave" build=` + buildCommit + " snapshot=" + filepath.Join(dir, "1.html"),
		`bug 1 comment: "after a restart"`,
		"bug 2: ",
	} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

// --no-bug hides the button and refuses reports; a share never has either.
func TestNoBug(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	form := url.Values{"url": {"x"}, "page": {"x"}}
	defer func() { bugOff = false }()
	bugOff = true
	s, _, _ := serveTree(t)
	if strings.Contains(get(s.handler(), "/").Body.String(), `class="bug"`) {
		t.Errorf("bug button with --no-bug")
	}
	if rec := postBug(s.handler(), form, "same-origin"); rec.Code == http.StatusOK {
		t.Errorf("report taken with --no-bug")
	}
	bugOff = false
	s, tr, _ := serveTree(t)
	s.shared, tr.shared = true, true
	if strings.Contains(get(s.handler(), "/").Body.String(), `class="bug"`) {
		t.Errorf("bug button on a share")
	}
	if rec := postBug(s.handler(), form, "same-origin"); rec.Code == http.StatusOK {
		t.Errorf("report taken on a share")
	}
}
