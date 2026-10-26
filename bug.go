package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// maxBug caps a bug report, snapshot included. A code page of jquery.js,
// 10,907 lines, is about 2 MB of HTML.
const maxBug = 16 << 20

// handleBug logs a page as a bug, from the bug button with --debug: one
// line in the log, which `journalctl --user -t www -g '^bug:'` finds, and
// the page as the reader saw it in a file under the state directory. The
// line names the URL, the selected text as the note, the window's size
// and scroll, the browser, the build and the snapshot's path.
func (s *server) handleBug(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "a bug report must come from an "+progName+" page", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBug)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad bug report: "+err.Error(), http.StatusRequestEntityTooLarge)
		logErr("bug report refused: %s", err)
		return
	}
	snap, err := saveSnapshot(r.PostFormValue("page"))
	if err != nil {
		logErr("bug snapshot not saved: %s", err)
		snap = "none"
	}
	note := r.PostFormValue("note")
	if len(note) > 500 {
		note = note[:500] + "..."
	}
	logInfo("bug: %s note=%s view=%s ua=%s build=%s snapshot=%s", r.PostFormValue("url"), strconv.Quote(note),
		r.PostFormValue("view"), strconv.Quote(r.PostFormValue("ua")), buildCommit, snap)
	w.WriteHeader(http.StatusNoContent)
}

// saveSnapshot writes page, a page's HTML as the browser held it, to
// $XDG_STATE_HOME/www/bugs, named by the time, and returns its path. The
// stylesheet goes inline, so the file shows as it did under a later
// build, whose stylesheet has another name.
func saveSnapshot(page string) (string, error) {
	if page == "" {
		return "", fmt.Errorf("the report holds no page")
	}
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, progName, "bugs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	css := strings.ReplaceAll(string(styleCSS), `url("fonts/`, `url("`+ownPath+`fonts/`)
	page = strings.Replace(page, `<link rel="stylesheet" href="`+styleURL+`">`, "<style>"+css+"</style>", 1)
	p := filepath.Join(dir, time.Now().Format("20060102-150405.000")+".html")
	return p, os.WriteFile(p, []byte(page), 0o600)
}
