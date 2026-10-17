package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// liveJS is the one script a page may run. It holds an EventSource while
// the tab is visible and reloads the page on its one event, or when the
// stream stays lost for three seconds, since the server is gone. A hidden
// tab lets go, since a browser opens no more than six connections to a
// host, and asks again when it shows. It also loads a commit's diff into a
// history when its fold opens.
//
//go:embed assets/live.js
var liveJS string

// scriptSrc allows liveJS and no other script.
func scriptSrc(js string) string {
	sum := sha256.Sum256([]byte(js))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// settle lets an editor finish a save, which takes several steps.
const settle = 100 * time.Millisecond

// goneWait is how long a page waits for its tree or file to come back
// before it reloads to say it is gone. A tree leaves for a moment on
// --restart, and a file while an editor renames a new one over it. Tests
// shorten it.
var goneWait = 2 * time.Second

// live tells open pages when what they show has changed. A page names its
// version: the server instance that rendered it and what the page showed.
// It keeps a stream open on /_mdserve/live, which wakes on any change in
// the directories the page reads from and sends one event, reload, when
// the version is no longer current.
type live struct {
	id   string // this server instance; a page from an earlier one reloads
	key  []byte // keys versions, which pages show to visitors
	w    *watcher
	mu   sync.Mutex
	subs map[chan struct{}]bool
	wds  map[int]int // open pages per watch descriptor
	done chan struct{}
	once sync.Once
}

func newLive() *live {
	l := &live{id: rand.Text()[:12], key: []byte(rand.Text()), w: newWatcher(pageMask), subs: map[chan struct{}]bool{},
		wds: map[int]int{}, done: make(chan struct{})}
	go l.loop()
	return l
}

// subscribe watches dirs for one page and returns the channel that wakes
// it. release undoes both.
func (l *live) subscribe(dirs []string) (wake chan struct{}, release func()) {
	wake = make(chan struct{}, 1)
	var wds []int
	l.mu.Lock()
	l.subs[wake] = true
	for _, dir := range dirs {
		// A directory reached twice, through a link, has one descriptor.
		if wd := l.w.add(dir); wd >= 0 && !slices.Contains(wds, wd) {
			wds = append(wds, wd)
			l.wds[wd]++
		}
	}
	l.mu.Unlock()
	return wake, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.subs, wake)
		for _, wd := range wds {
			if l.wds[wd]--; l.wds[wd] == 0 {
				delete(l.wds, wd)
				l.w.remove(wd)
			}
		}
	}
}

// loop wakes every page once a change has settled, and every second when
// inotify is unavailable. Each page checks its own version.
func (l *live) loop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-l.w.wake:
			select {
			case <-time.After(settle):
			case <-l.done:
				return
			}
			select {
			case <-l.w.wake:
			default:
			}
		case <-tick.C:
			if !l.w.poll.Load() {
				continue
			}
		}
		l.mu.Lock()
		for wake := range l.subs {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
		l.mu.Unlock()
	}
}

// kick wakes every page, as when a tree joins or leaves.
func (l *live) kick() {
	if l != nil {
		l.w.signal()
	}
}

// close ends every stream without a reload: the server is going, and a
// page reloaded now would find nothing. The browser asks again, and the
// next server's pages differ in instance.
func (l *live) close() {
	if l != nil {
		l.once.Do(func() {
			close(l.done)
			l.w.close()
		})
	}
}

type liveKey struct{}

// withLive gives the request the URL of its page's stream. The version is
// taken before the page is read, so a change while the page renders still
// reloads the page.
func (s *server) withLive(r *http.Request) *http.Request {
	if s.live == nil {
		return r
	}
	q := url.Values{"path": {r.URL.Path}, "v": {s.live.id + "." + s.version(r.URL.Path)}}
	return r.WithContext(context.WithValue(r.Context(), liveKey{}, "/_mdserve/live?"+q.Encode()))
}

func liveURL(r *http.Request) string {
	u, _ := r.Context().Value(liveKey{}).(string)
	return u
}

// handleLive holds a page's stream open until the page is out of date,
// then sends reload. A page whose tree or file has gone waits goneWait
// for it to come back.
func (s *server) handleLive(w http.ResponseWriter, r *http.Request) {
	l := s.live
	fl, ok := w.(http.Flusher)
	if l == nil || !ok {
		http.NotFound(w, r)
		return
	}
	p, seen := r.URL.Query().Get("path"), r.URL.Query().Get("v")
	wake, release := l.subscribe(s.watchDirs(p))
	defer release()
	w.Header().Set("Content-Type", "text/event-stream")
	// A dropped stream comes back after a second, as on --restart. One lost
	// for three seconds reloads its page, so a page goes down with its
	// server.
	fmt.Fprint(w, "retry: 1000\n\n")
	fl.Flush()
	var grace <-chan time.Time
	expired := false
	for {
		now := l.id + "." + s.version(p)
		switch {
		case now == seen:
			grace, expired = nil, false
		case absent(now) && !absent(seen) && !expired:
			if grace == nil {
				grace = time.After(goneWait)
			}
		default:
			select {
			case <-l.done:
			default:
				fmt.Fprint(w, "data: reload\n\n")
				fl.Flush()
			}
			return
		}
		select {
		case <-wake:
		case <-grace:
			expired = true
		case <-l.done:
			return
		case <-r.Context().Done():
			return
		}
	}
}

// absent reports whether a version is of a page that says its tree or file
// is not there.
func absent(v string) bool {
	_, v, _ = strings.Cut(v, ".")
	return v == "" || v[0] == '-' || v[0] == '!'
}

// version names what the page at urlPath shows: a hash of the stats it
// reads, marked f for a file, d for a directory, ! for a page within a
// tree that shows an error and - for the index, which lists the trees.
// The hash is keyed, since it covers the tree's path, which a shared
// server must not let a visitor guess.
func (s *server) version(urlPath string) string {
	h := hmac.New(sha256.New, s.live.key)
	kind := byte('-')
	if t := s.lookup(urlPath); t != nil {
		kind = t.version(h, t.name(urlPath))
	} else {
		for _, t := range s.list() {
			// A secret tree is not listed, so it must not show by a reload.
			if !(s.shared && t.secret) {
				io.WriteString(h, t.prefix+"\n")
			}
		}
	}
	return string(kind) + hex.EncodeToString(h.Sum(nil)[:8])
}

// version writes what the page at name reads to h and returns its kind.
func (t *tree) version(h io.Writer, name string) byte {
	// Another tree at the same prefix is another page.
	io.WriteString(h, t.dir+"\n")
	info, err := fs.Stat(t.fsys, name)
	if err != nil {
		io.WriteString(h, err.Error())
		return '!'
	}
	// Pages in a repo show its history, which a commit changes.
	_, head := t.gitHead(name)
	io.WriteString(h, head+"\n")
	if !info.IsDir() {
		fmt.Fprintf(h, "%d %d", info.ModTime().UnixNano(), info.Size())
		return 'f'
	}
	// A listing shows names. A change to a file in the directory leaves the
	// page as it was.
	entries, err := fs.ReadDir(t.fsys, name)
	if err != nil {
		io.WriteString(h, err.Error())
		return '!'
	}
	for _, e := range entries {
		n := e.Name()
		if t.unlisted(name, n) {
			continue
		}
		fmt.Fprintf(h, "%s %v\n", n, e.Type())
	}
	return 'd'
}

// watchDirs are the directories whose changes may change the page at
// urlPath: the directory a listing shows, or the one holding the file, and
// the same for the target when the path leads through a link, and the
// repo's git directory, which a commit writes. The
// index needs none, since trees that join or leave kick every page.
func (s *server) watchDirs(urlPath string) []string {
	t := s.lookup(urlPath)
	if t == nil {
		return nil
	}
	target := filepath.Join(t.dir, filepath.FromSlash(t.name(urlPath)))
	paths := []string{target}
	if real, err := filepath.EvalSymlinks(target); err == nil && real != target {
		paths = append(paths, real)
	}
	var dirs []string
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			dirs = append(dirs, p)
		} else {
			dirs = append(dirs, filepath.Dir(p))
		}
	}
	if gitDir, _ := t.gitHead(t.name(urlPath)); gitDir != "" {
		dirs = append(dirs, gitDir)
	}
	return dirs
}
