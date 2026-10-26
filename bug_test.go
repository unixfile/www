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

// With --debug, each page's stamp holds the bug button, and a report
// writes one line to the log and the page, its stylesheet inline, to the
// state directory. Without --debug, neither the button nor its address
// is there.
func TestBugReport(t *testing.T) {
	useJournal(t, "")
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	defer func() { debugOn = false }()
	form := url.Values{
		"url":  {"http://127.31.1.1:8080/nowhere.md"},
		"note": {"the coin\nis cut off"},
		"view": {"1280x800+0"},
		"ua":   {"Brave"},
		"page": {`<!doctype html><html><head><link rel="stylesheet" href="` + styleURL + `"></head><body>seen</body></html>`},
	}

	s, _, _ := serveTree(t)
	if strings.Contains(get(s.handler(), "/").Body.String(), "button class=\"bug\"") {
		t.Errorf("bug button without --debug")
	}
	if rec := postBug(s.handler(), form, "same-origin"); rec.Code == http.StatusNoContent {
		t.Errorf("bug report taken without --debug")
	}

	debugOn = true
	s, _, _ = serveTree(t)
	h := s.handler()
	if !strings.Contains(get(h, "/").Body.String(), `<button class="bug" data-post="`+ownPath+`bug"`) {
		t.Errorf("no bug button with --debug")
	}
	if rec := postBug(h, form, "cross-site"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site report: status %d, want 403", rec.Code)
	}
	if rec := postBug(h, form, "same-origin"); rec.Code != http.StatusNoContent {
		t.Fatalf("report: status %d: %s", rec.Code, rec.Body.String())
	}
	snaps, _ := filepath.Glob(filepath.Join(state, progName, "bugs", "*.html"))
	if len(snaps) != 1 {
		t.Fatalf("snapshots %v, want one", snaps)
	}
	b, _ := os.ReadFile(snaps[0])
	if !strings.Contains(string(b), "<style>") || strings.Contains(string(b), styleURL) || !strings.Contains(string(b), "seen") {
		t.Errorf("snapshot lacks the inline stylesheet or the page: %.200s", b)
	}
	path, _ := logFile()
	log, _ := os.ReadFile(path)
	want := `bug: http://127.31.1.1:8080/nowhere.md note="the coin\nis cut off" view=1280x800+0 ua="Brave" build=` + buildCommit + " snapshot=" + snaps[0]
	if !strings.Contains(string(log), want) {
		t.Errorf("log lacks %q:\n%s", want, log)
	}
}
