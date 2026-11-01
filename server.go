package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed assets/fonts.css
var fontsCSS []byte

//go:embed assets/style.css
var mainCSS []byte

// styleCSS is the stylesheet pages link: the font faces, which make fonts
// writes, then the program's own rules.
var styleCSS = append(append([]byte{}, fontsCSS...), mainCSS...)

// styleURL names the stylesheet by a hash of its content, so the browser
// keeps it for a year and a binary with a changed stylesheet links a new
// URL.
var styleURL = func() string {
	sum := sha256.Sum256(styleCSS)
	return ownPath + "style." + hex.EncodeToString(sum[:6]) + ".css"
}()

//go:embed assets/layout.html
var layoutHTML string

// fontFS holds subsets of STIX Two Text and Iosevka, which a page takes
// when the machine lacks the font. A changed font needs a new file name,
// since browsers keep these for a year.
//
//go:embed assets/fonts/*.woff2
var fontFS embed.FS

var layout = template.Must(template.New("layout").Funcs(template.FuncMap{
	"liveJS":   func() template.JS { return template.JS(liveJS) },
	"styleURL": func() string { return styleURL },
	"ownPath":  func() string { return ownPath },
	"progName": func() string { return progName },
}).Parse(layoutHTML))

// csp forbids every script but live reload's, which it names by hash.
// Pandoc passes raw HTML through, and a README in a cloned repo must not
// run code on this origin.
var csp = "default-src 'none'; script-src " + scriptSrc(liveJS) + "; connect-src 'self'; " +
	"img-src 'self' data:; media-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; " +
	"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// server answers for every tree served on one endpoint. A request goes to
// the tree with the longest prefix of its path.
type server struct {
	mu     sync.RWMutex
	trees  []*tree
	stop   func([]*tree, string) // withdraws trees for a cause; set by the hub
	about  about
	shared bool   // on an address beyond loopback: it shows the trees and nothing of the machine
	public bool   // the web address of --public, which strangers reach by any name
	live   *live  // nil in tests that do without live reload
	web    *web   // set on the web address, whose trees are repos in the cache
	webURL string // the web address's URL, for the index, or ""
	// homeURL is, on the web address, the default address's URL, which
	// its index links back to.
	homeURL string
}

func newServer() *server {
	return &server{stop: func([]*tree, string) {}}
}

// tree is one served directory, exposed with everything below it.
type tree struct {
	dir      string
	realDir  string // dir with its links resolved
	prefix   string // URL path of the tree, eg /scratch/notes/
	label    string // the tree as the first breadcrumb, eg ~/scratch/notes
	fsys     fs.FS
	root     *os.Root
	lock     *lock       // nil only in tests
	link     string      // registry link
	linkInfo os.FileInfo // the link this server knows, to tell it from a new join
	wd       int         // inotify watch on dir, or -1
	shared   bool        // served beyond loopback: only what other users could read
	secret   bool        // mounted under a token, and left off the index
	web      *webRepo    // the cached repo this tree is a worktree of, on the web address
	public   bool        // a stranger's repo on a public server: no link out, no path of the machine
	srv      *server     // the server that serves it, set when it joins
	linked   linkIndex   // the relative links of each doc, for ?backlinks
	at       *commit     // for a version, the commit it shows
	atLabel  string      // for a version, its commit's stardate, day and x, or hash
	base     *tree       // for a version, the tree it is a version of
	versions sync.Map    // the versions served so far, by full hash
}

func newTree(root *os.Root, dir, prefix, label string) *tree {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = dir
	}
	t := &tree{dir: dir, realDir: real, prefix: prefix, label: label, root: root, wd: -1}
	t.fsys = treeFS{t}
	return t
}

func (s *server) add(t *tree) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.srv = s
	s.trees = append(s.trees, t)
	s.live.kick()
}

// remove reports whether t was served.
func (s *server) remove(t *tree) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.trees)
	s.trees = slices.DeleteFunc(s.trees, func(x *tree) bool { return x == t })
	s.live.kick()
	return len(s.trees) < n
}

// list returns the served trees sorted by prefix.
func (s *server) list() []*tree {
	s.mu.RLock()
	out := slices.Clone(s.trees)
	s.mu.RUnlock()
	slices.SortFunc(out, func(a, b *tree) int { return strings.Compare(a.prefix, b.prefix) })
	return out
}

func (s *server) byPrefix(prefix string) *tree {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.trees {
		if t.prefix == prefix {
			return t
		}
	}
	return nil
}

// lookup returns the tree that serves path, or a version of, eg
// /x/www@261024.4/. A tree inside another wins, so it keeps its own label
// and lock.
func (s *server) lookup(path string) *tree {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *tree
	for _, t := range s.trees {
		_, _, version := t.versionIn(path)
		if (version || strings.HasPrefix(path, t.prefix)) && (best == nil || len(t.prefix) > len(best.prefix)) {
			best = t
		}
	}
	return best
}

// mount returns the URL prefix and label for dir. Inside home the prefix is
// the path relative to home, so the URL names the tree; outside home it is
// the absolute path.
func mount(dir, home string) (prefix, label string) {
	rel, err := filepath.Rel(home, dir)
	switch {
	case home == "" || err != nil || rel == ".." || strings.HasPrefix(rel, "../"):
		return strings.TrimSuffix(dir, "/") + "/", dir
	case rel == ".":
		return "/", "~"
	default:
		return "/" + filepath.ToSlash(rel) + "/", "~/" + filepath.ToSlash(rel)
	}
}

// shareMount returns the prefix and label of a shared tree: its base name,
// or the token under --secret. Neither tells a visitor where the tree lies.
func shareMount(dir, token string) (prefix, label string) {
	label = filepath.Base(dir)
	switch {
	case token != "":
		return "/" + token + "/", label
	case label == "/":
		return "/", label
	default:
		return "/" + label + "/", label
	}
}

func (t *tree) prefixURL() string {
	return (&url.URL{Path: t.prefix}).String()
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+styleURL, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(styleCSS)
	})
	mux.HandleFunc("GET "+ownPath+"fonts/{name}", func(w http.ResponseWriter, r *http.Request) {
		b, err := fontFS.ReadFile("assets/fonts/" + path.Base(r.PathValue("name")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/woff2")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(b)
	})
	// A shared server has no buttons: no visitor may stop it. The web
	// address has none either; its server stops with the default address.
	if !s.shared && s.web == nil {
		mux.HandleFunc("POST "+ownPath+"stop", s.handleStop)
	}
	// The bug button is on every page but a share's, unless --no-bug.
	if !bugOff && !s.shared {
		mux.HandleFunc("POST "+ownPath+"bug", s.handleBug)
	}
	mux.HandleFunc("GET "+ownPath+"live", s.handleLive)
	mux.HandleFunc("GET /", s.handlePath)
	return s.logged(guard(mux, s.shared || s.public))
}

// handleStop serves the stop buttons: a tree's button withdraws that tree,
// the index's withdraws every tree. Any site can submit a form to
// localhost, so the request must come from a page of this server.
func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "stop must come from an "+progName+" page", http.StatusForbidden)
		return
	}
	trees, crumbs, why, kill := s.list(), []crumb{{Label: progName}}, "Kill server button", true
	if prefix := r.PostFormValue("tree"); prefix != "" {
		t := s.byPrefix(prefix)
		if t == nil {
			s.serveIndex(w, r, http.StatusNotFound, errorLine("Not served: "+prefix))
			return
		}
		trees, crumbs, why, kill = []*tree{t}, []crumb{t.rootCrumb()}, "Delete lock button", false
		// While other trees remain the server lives on: a button on the
		// index returns there, and a tree's page links it.
		if len(s.list()) > 1 {
			if r.PostFormValue("from") == "index" {
				s.stop(trees, why+" on the index")
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			crumbs = []crumb{indexCrumb, t.rootCrumb()}
		}
	}
	render(w, r, http.StatusGone, page{
		Title:  progName + " stopped",
		Crumbs: crumbs,
		Kind:   "stopped",
		// The server may close its listener before the browser asks for
		// the stylesheet, so the page carries it.
		Style:   template.CSS(styleCSS),
		Body:    template.HTML(coin("410") + `<table class="gone">` + "\n" + stoppedRows(trees, kill, len(s.list())-len(trees)) + "</table>\n"),
		Stopped: true,
	})
	// stop only withdraws; the hub shuts down on its own goroutine, since
	// Shutdown waits for this response to finish.
	s.stop(trees, why)
}

// stoppedRows say what a stop ended, in a table as a 404's: each tree,
// its lock, removed, or kept when Kill server leaves a persistent .LOCK
// for the next server, and then the server: stopped, or how many trees
// it still serves.
func stoppedRows(trees []*tree, kill bool, left int) string {
	var b strings.Builder
	for _, t := range trees {
		b.WriteString(goneRow("stopped", segments(t.label)))
		if t.lock == nil {
			continue
		}
		state := "removed"
		if kill && isPersist(t.lock.path) {
			state = "kept"
		}
		// A lock in its tree's directory goes by its name, since the row
		// above names the directory.
		where := tildePath(t.lock.path)
		if filepath.Dir(t.lock.path) == t.dir {
			where = filepath.Base(t.lock.path)
		}
		b.WriteString(goneRow(state, segments(where)))
	}
	switch left {
	case 0:
		b.WriteString(goneRow("server", "stopped; close this tab"))
	case 1:
		b.WriteString(goneRow("server", `<a href="/">serves 1 more tree</a>`))
	default:
		b.WriteString(goneRow("server", fmt.Sprintf(`<a href="/">serves %d more trees</a>`, left)))
	}
	return b.String()
}

// handlePath maps the URL path one to one onto the file tree, so relative
// links in the docs work unchanged.
func (s *server) handlePath(w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(context.WithValue(r.Context(), startKey{}, time.Now()))
	if _, raw := r.URL.Query()["raw"]; !raw {
		r = s.withLive(r)
	}
	t := s.lookup(r.URL.Path)
	switch {
	case s.web != nil:
		s.web.serve(w, r, t)
	case t != nil:
		t.serve(w, r)
	case s.byPrefix(r.URL.Path+"/") != nil:
		// Temporary: the tree may be withdrawn and another served here.
		http.Redirect(w, r, s.byPrefix(r.URL.Path+"/").prefixURL(), http.StatusFound)
	case r.URL.Path == "/":
		s.serveIndex(w, r, http.StatusOK, "")
	default:
		s.serveStray(w, r)
	}
}
