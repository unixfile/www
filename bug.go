package main

import (
	"errors"
	"fmt"
	"io/fs"
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

// handleBug files a page as a numbered bug, from the bug button at each
// page's foot, or adds a comment to one. A report is one line in the log,
// which `journalctl --user -t www -g '^bug '` finds; N.kv under the state
// directory, as keyval: the time, URL, selected text as the note, the
// window's size and scroll, the browser, the build and the server's
// state from bugState; and N.html, the page as the reader saw it. It
// answers with the number. A comment names the number and is added to
// the log and to N.kv.
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
	dir, err := bugDir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		logErr("bug report not filed: %s", err)
		return
	}
	if n := r.PostFormValue("bug"); n != "" {
		comment := clip(r.PostFormValue("comment"))
		if err := commentBug(dir, n, comment); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, fs.ErrNotExist) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		logInfo("bug %s comment: %s", n, strconv.Quote(comment))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	n, err := fileBug(dir, append([][2]string{
		{"time", time.Now().Format(time.RFC3339)},
		{"url", r.PostFormValue("url")},
		{"note", clip(r.PostFormValue("note"))},
		{"view", r.PostFormValue("view")},
		{"ua", r.PostFormValue("ua")},
		{"build", buildCommit},
	}, s.bugState()...))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		logErr("bug report not filed: %s", err)
		return
	}
	snap := filepath.Join(dir, strconv.Itoa(n)+".html")
	if err := saveSnapshot(snap, r.PostFormValue("page")); err != nil {
		logErr("bug %d snapshot not saved: %s", n, err)
		snap = "none"
	}
	logInfo("bug %d: %s note=%s view=%s ua=%s build=%s snapshot=%s", n, r.PostFormValue("url"), strconv.Quote(clip(r.PostFormValue("note"))),
		r.PostFormValue("view"), strconv.Quote(r.PostFormValue("ua")), buildCommit, snap)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, n)
}

// bugState is the server's part of a report: its process, its binary and
// whether a rebuild has replaced it, the address, when it started, the
// command line, and each tree it serves as tree.0, tree.1 and on. The
// button reports bugs in the program, so a report names the server that
// filed it, eg a stale one on another port.
func (s *server) bugState() [][2]string {
	stale := "no"
	if s.about.stale() {
		stale = "yes"
	}
	started := ""
	if !s.about.started.IsZero() {
		started = s.about.started.Format(time.RFC3339)
	}
	addr := ""
	if s.about.ep.IsValid() {
		addr = s.about.ep.String()
	}
	fields := [][2]string{
		{"pid", strconv.Itoa(os.Getpid())},
		{"exe", s.about.exe},
		{"stale", stale},
		{"addr", addr},
		{"started", started},
		{"command", commandLine(os.Args[1:])},
	}
	for i, t := range s.list() {
		fields = append(fields, [2]string{"tree." + strconv.Itoa(i), t.dir})
	}
	return fields
}

// clip cuts a note or comment to 2,000 bytes, so a stray paste of a whole
// page does not fill the log.
func clip(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "..."
	}
	return s
}

// bugDir is where bug reports go, $XDG_STATE_HOME/www/bugs, made on first
// use.
func bugDir() (string, error) {
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, progName, "bugs")
	return dir, os.MkdirAll(dir, 0o700)
}

// fileBug writes fields as keyval to the next number's N.kv and returns
// the number. The counter in next only grows, so a number deleted once
// its bug is fixed is never given again. Creating N.kv exclusively keeps
// two servers that file at once from taking one number.
func fileBug(dir string, fields [][2]string) (int, error) {
	n := 1
	if b, err := os.ReadFile(filepath.Join(dir, "next")); err == nil {
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && v > 0 {
			n = v
		}
	}
	var b strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&b, "%s %s\n", f[0], kvValue(f[1]))
	}
	for ; ; n++ {
		f, err := os.OpenFile(filepath.Join(dir, strconv.Itoa(n)+".kv"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		_, err = f.WriteString(b.String())
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return 0, err
		}
		return n, os.WriteFile(filepath.Join(dir, "next"), []byte(strconv.Itoa(n+1)+"\n"), 0o600)
	}
}

// commentBug adds comment to bug n's N.kv as the next comment.N key.
func commentBug(dir, n, comment string) error {
	if v, err := strconv.Atoi(n); err != nil || v < 1 {
		return fmt.Errorf("no bug %q", n)
	}
	p := filepath.Join(dir, n+".kv")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	i := strings.Count("\n"+string(b), "\ncomment.")
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "comment.%d %s\n", i, kvValue(comment))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// saveSnapshot writes page, a page's HTML as the browser held it, to p.
// The stylesheet goes inline, so the file shows as it did under a later
// build, whose stylesheet has another name.
func saveSnapshot(p, page string) error {
	if page == "" {
		return fmt.Errorf("the report holds no page")
	}
	css := strings.ReplaceAll(string(styleCSS), `url("fonts/`, `url("`+ownPath+`fonts/`)
	page = strings.Replace(page, `<link rel="stylesheet" href="`+styleURL+`">`, "<style>"+css+"</style>", 1)
	return os.WriteFile(p, []byte(page), 0o600)
}
