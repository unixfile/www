package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

//go:embed assets/style.css
var styleCSS []byte

// styleURL names the stylesheet by a hash of its content, so the browser
// keeps it for a year and a binary with a changed stylesheet links a new
// URL.
var styleURL = func() string {
	sum := sha256.Sum256(styleCSS)
	return ownPath + "style." + hex.EncodeToString(sum[:6]) + ".css"
}()

//go:embed assets/layout.html
var layoutHTML string

// fontFS holds STIX Two Text, which the stylesheet takes from the machine
// when installed and from here otherwise. A changed font needs a new file
// name, since browsers keep these for a year.
//
//go:embed assets/fonts/*.otf
var fontFS embed.FS

var layout = template.Must(template.New("layout").Funcs(template.FuncMap{
	"liveJS":   func() template.JS { return template.JS(liveJS) },
	"styleURL": func() string { return styleURL },
	"ownPath":  func() string { return ownPath },
}).Parse(layoutHTML))

// maxCode caps the size of a file shown as a code page; larger files go raw.
const maxCode = 1 << 20

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
	live   *live  // nil in tests that do without live reload
	web    *web   // set on the web address, whose trees are repos in the cache
	webURL string // the web address's URL, for the index, or ""
}

// about describes the server for its index: where it listens, since when,
// and the binary it runs, so the index can tell when a rebuild has left it
// behind. Tests leave it zero.
type about struct {
	ep      netip.AddrPort
	started time.Time
	exe     string
	exeInfo os.FileInfo
}

func newAbout(ep netip.AddrPort) about {
	a := about{ep: ep, started: time.Now()}
	if exe, err := os.Executable(); err == nil {
		if info, err := os.Stat(exe); err == nil {
			a.exe, a.exeInfo = exe, info
		}
	}
	return a
}

// stale reports whether the binary on disk is no longer the one running.
func (a about) stale() bool {
	if a.exeInfo == nil {
		return false
	}
	info, err := os.Stat(a.exe)
	return err != nil || !os.SameFile(info, a.exeInfo) || !info.ModTime().Equal(a.exeInfo.ModTime())
}

// restartCmd is the command that moves this server's trees to the new
// binary; with no address it moves every server's.
func (a about) restartCmd() string {
	return progName + " --restart"
}

// status is the index header's account of the server's age. The build
// time is on every page, above the header.
func (a about) status() string {
	if a.started.IsZero() {
		return ""
	}
	return "up since " + clock(a.started)
}

// built is when the running binary was written, read once at start, so
// each page can say which build served it. showBuilt, set by --stamp,
// puts it above the header.
var showBuilt bool

var buildCommit = buildRev()

var built = func() time.Time {
	exe, err := os.Executable()
	if err != nil {
		return time.Time{}
	}
	info, err := os.Stat(exe)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}()

// versionLine is --version's account of the build, in the words of the
// stamp, with the full date since a build may be old.
func versionLine(t time.Time, commit string) string {
	when := "at an unknown time"
	if !t.IsZero() {
		when = t.Format("2006-01-02 15:04")
	}
	return progName + " built " + when + ", from " + commit
}

// stamp is the build as a page shows it: the time of day, the seconds
// since, which live.js counts on from Millis, the commit, and how long
// the page took to render.
type stamp struct {
	Clock    string
	Millis   int64
	Ago      int
	Commit   string
	Rendered string
}

type startKey struct{}

// rendered says how long the request has taken so far, in milliseconds
// with a decimal below 10, since most docs render in under one.
func rendered(r *http.Request) string {
	start, ok := r.Context().Value(startKey{}).(time.Time)
	if !ok {
		return ""
	}
	ms := float64(time.Since(start).Microseconds()) / 1000
	if ms < 10 {
		return fmt.Sprintf("%.1f ms", ms)
	}
	return fmt.Sprintf("%.0f ms", ms)
}

// clock gives the time of day, with the date when it is not today.
func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if y, m, d := t.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
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
	linked   linkIndex   // the relative links of each doc, for ?backlinks
	at       *commit     // for a version, the commit it shows
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

// lookup returns the tree that serves path. A tree inside another wins, so
// it keeps its own label and lock.
func (s *server) lookup(path string) *tree {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *tree
	for _, t := range s.trees {
		if strings.HasPrefix(path, t.prefix) && (best == nil || len(t.prefix) > len(best.prefix)) {
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
		w.Header().Set("Content-Type", "font/otf")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(b)
	})
	// A shared server has no buttons: no visitor may stop it. The web
	// address has none either; its server stops with the default address.
	if !s.shared && s.web == nil {
		mux.HandleFunc("POST "+ownPath+"stop", s.handleStop)
	}
	mux.HandleFunc("GET "+ownPath+"live", s.handleLive)
	mux.HandleFunc("GET /", s.handlePath)
	return s.logged(guard(mux, s.shared))
}

// logged records each request but those that come with every page: live
// reload streams, the stylesheet, fonts and the favicon. A shared server
// adds the visitor's address. A secret tree's token stays out of the log.
func (s *server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/favicon.ico" || strings.HasPrefix(p, ownPath) && p != ownPath+"stop" {
			next.ServeHTTP(w, r)
			return
		}
		uri := r.URL.RequestURI()
		// The token is cut from the decoded path, which the lookup matched;
		// the raw one may spell it in escapes, eg /%733cret/.
		if t := s.lookup(p); t != nil && t.secret {
			uri = "/<token>" + hideToken(r.URL, strings.TrimPrefix(p, t.prefix))
		} else if t := s.byPrefix(p + "/"); t != nil && t.secret {
			// The secret path without its slash, which redirects.
			uri = "/<token>" + hideToken(r.URL, "")
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		msg := fmt.Sprintf("%s %s %d %dms", r.Method, uri, sw.status, time.Since(start).Milliseconds())
		if s.shared {
			msg += " from " + r.RemoteAddr
		}
		if sw.status >= 500 {
			logErr("%s", msg)
		} else {
			logInfo("%s", msg)
		}
	})
}

// hideToken is the request URI of u for the log, with rest, the decoded
// path below a secret tree's prefix, in place of its path.
func hideToken(u *url.URL, rest string) string {
	uri := (&url.URL{Path: "/" + rest}).EscapedPath()
	if rest == "" && !strings.HasSuffix(u.Path, "/") {
		uri = ""
	}
	if u.RawQuery != "" {
		uri += "?" + u.RawQuery
	}
	return uri
}

// statusWriter keeps the status a handler sent, for the log.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// guard rejects requests for any host but this machine, which defeats DNS
// rebinding, and sets the headers every response needs. A shared server
// takes any host, since visitors use whatever name reaches it; a secret
// path is its guard against rebinding.
func guard(next http.Handler, anyHost bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if !anyHost && !localHost(host) {
			http.Error(w, progName+" answers only to this machine", http.StatusMisdirectedRequest)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		// same-origin, not no-referrer: under no-referrer browsers send
		// Origin: null even on this server's own form posts.
		h.Set("Referrer-Policy", "same-origin")
		// Pages render from files that change while being read, so the
		// browser must ask again before reusing anything.
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// hostsFile maps names to addresses; tests point it elsewhere.
var hostsFile = "/etc/hosts"

// localHost accepts a loopback address, localhost, or a name that the hosts
// file maps to loopback, eg an www instance named there. DNS is never
// asked: a DNS name that resolves to loopback is the rebinding attack itself.
func localHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	for _, e := range hostsEntries() {
		if e.ip.IsLoopback() && e.has(host) {
			return true
		}
	}
	return false
}

// hostsEntry is one line of the hosts file: an address and its names.
type hostsEntry struct {
	ip    netip.Addr
	names []string
}

func (e hostsEntry) has(host string) bool {
	return slices.ContainsFunc(e.names, func(name string) bool { return strings.EqualFold(name, host) })
}

func hostsEntries() []hostsEntry {
	b, err := os.ReadFile(hostsFile)
	if err != nil {
		return nil
	}
	var out []hostsEntry
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		ip, err := netip.ParseAddr(f[0])
		if err != nil {
			continue
		}
		out = append(out, hostsEntry{ip.Unmap(), f[1:]})
	}
	return out
}

// handleStop serves the stop buttons: a tree's button withdraws that tree,
// the index's withdraws every tree. Any site can submit a form to
// localhost, so the request must come from a page of this server.
func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "stop must come from an "+progName+" page", http.StatusForbidden)
		return
	}
	trees, crumbs, what, why := s.list(), []crumb{{Label: progName}}, "Stopped "+progName, "Kill server button"
	if prefix := r.PostFormValue("tree"); prefix != "" {
		t := s.byPrefix(prefix)
		if t == nil {
			s.serveIndex(w, r, http.StatusNotFound, "Not served: "+prefix)
			return
		}
		trees, crumbs, what, why = []*tree{t}, []crumb{t.rootCrumb()}, "Stopped serving "+t.label, "Delete lock button"
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
	what += " and removed its lockfile"
	if len(trees) > 1 {
		what += "s"
	}
	render(w, r, http.StatusOK, page{
		Title:  progName + " stopped",
		Crumbs: crumbs,
		Kind:   "stopped",
		// The server may close its listener before the browser asks for
		// the stylesheet, so the page carries it.
		Style:   template.CSS(styleCSS),
		Body:    template.HTML("<p class=\"own\">" + template.HTMLEscapeString(what) + ". Close this tab.</p>"),
		Stopped: true,
	})
	// stop only withdraws; the hub shuts down on its own goroutine, since
	// Shutdown waits for this response to finish.
	s.stop(trees, why)
}

// sameOrigin trusts Sec-Fetch-Site when present: browsers set it and pages
// cannot forge it. Origin is the fallback for clients that lack it; a
// client sending neither, such as curl, is not a browser a site can drive.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
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
		s.serveIndex(w, r, http.StatusNotFound, "Not found: "+r.URL.Path)
	}
}

// serveIndex lists the locks this server holds, each with its tree and a
// button that deletes it, then the trees on other addresses, which only
// their own server may stop. It answers at / and for paths outside every
// tree.
func (s *server) serveIndex(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if s.shared {
		s.serveSharedIndex(w, r, status, msg)
		return
	}
	esc := template.HTMLEscapeString
	var body strings.Builder
	if msg != "" {
		body.WriteString("<pre class=\"error\">" + esc(msg) + "</pre>\n")
	}
	if s.about.stale() {
		body.WriteString("<p class=\"own warn\">The binary changed after this server started. Run <code>" + esc(s.about.restartCmd()) + "</code>.</p>\n")
	}
	trees := s.list()
	if len(trees) == 0 {
		body.WriteString("<p class=\"own\">No tree is served.</p>\n")
	} else {
		body.WriteString("<table class=\"locks own\">\n")
		for _, t := range trees {
			lockPath, since := "", ""
			if t.lock != nil {
				lockPath, since = t.lock.path, "since "+clock(t.lock.info.ModTime())
			}
			fmt.Fprintf(&body, "<tr><td><a href=\"%s\" title=\"%s\">%s</a> <span class=\"since\">%s</span></td>"+
				"<td><form method=\"post\" action=\""+ownPath+"stop\"><input type=\"hidden\" name=\"tree\" value=\"%s\">"+
				"<input type=\"hidden\" name=\"from\" value=\"index\"><button>Delete lock</button></form></td></tr>\n",
				esc(t.prefixURL()), esc(lockPath), segments(t.label), since, esc(t.prefix))
		}
		body.WriteString("</table>\n")
	}
	if s.webURL != "" {
		fmt.Fprintf(&body, "<p class=\"own\">Online repos: <a href=\"%s/\">%s/</a></p>\n", esc(s.webURL), esc(strings.TrimPrefix(s.webURL, "http://")))
	}
	if others := s.otherTrees(); len(others) > 0 {
		body.WriteString("<h2 class=\"own\">Other addresses</h2>\n<table class=\"locks own\">\n")
		for _, t := range others {
			fmt.Fprintf(&body, "<tr><td><a href=\"%s\" title=\"%s\">%s</a> <span class=\"since\">since %s</span></td></tr>\n",
				esc(t.url), esc(t.lockPath), segments(t.url), clock(t.since))
		}
		body.WriteString("</table>\n")
	}
	render(w, r, status, page{Title: progName, Crumbs: []crumb{{Label: "index"}}, Kind: "index",
		Body: template.HTML(body.String()), Status: s.about.status()})
}

// segments writes a path or URL as one box per name, each ending in its
// slash, so a line breaks between names and not inside one. The // of a
// URL stays with its scheme.
func segments(p string) string {
	var parts []string
	for _, s := range strings.SplitAfter(p, "/") {
		switch {
		case s == "":
		case s == "/" && len(parts) > 0:
			parts[len(parts)-1] += s
		default:
			parts = append(parts, s)
		}
	}
	for i, s := range parts {
		parts[i] = `<span class="seg">` + template.HTMLEscapeString(s) + `</span>`
	}
	return strings.Join(parts, "<wbr>")
}

// serveSharedIndex lists the trees shared without a secret, by name alone:
// no lock paths, buttons or other addresses.
func (s *server) serveSharedIndex(w http.ResponseWriter, r *http.Request, status int, msg string) {
	esc := template.HTMLEscapeString
	var body strings.Builder
	if msg != "" {
		body.WriteString("<pre class=\"error\">" + esc(msg) + "</pre>\n")
	}
	trees := slices.DeleteFunc(s.list(), func(t *tree) bool { return t.secret })
	if len(trees) == 0 {
		body.WriteString("<p class=\"own\">No tree is listed here.</p>\n")
	} else {
		body.WriteString("<ul class=\"listing\">\n")
		for _, t := range trees {
			fmt.Fprintf(&body, "<li><a href=\"%s\">%s/</a></li>\n", esc(t.prefixURL()), esc(t.label))
		}
		body.WriteString("</ul>\n")
	}
	render(w, r, status, page{Title: progName, Crumbs: []crumb{{Label: "index"}}, Kind: "index",
		Body: template.HTML(body.String()), Shared: true})
}

// otherTrees reads the registries of every other address, sorted by URL.
func (s *server) otherTrees() []servedTree {
	if !s.about.ep.IsValid() {
		return nil
	}
	eps, _ := endpoints()
	var out []servedTree
	for _, ep := range eps {
		if ep != s.about.ep {
			out = append(out, peekTrees(ep)...)
		}
	}
	slices.SortFunc(out, func(a, b servedTree) int { return strings.Compare(a.url, b.url) })
	return out
}

// name is the file below the tree that urlPath names, or "." for its root.
func (t *tree) name(urlPath string) string {
	rest := strings.TrimPrefix(urlPath, t.prefix)
	name := strings.TrimPrefix(path.Clean("/"+rest), "/")
	if name == "" {
		return "."
	}
	return name
}

func (t *tree) serve(w http.ResponseWriter, r *http.Request) {
	name := t.name(r.URL.Path)
	// A first segment such as @5430ac3 names a version, unless the tree
	// holds an entry by that name.
	if first, _, _ := strings.Cut(name, "/"); t.hasVersions() && atSegment.MatchString(first) {
		if _, err := fs.Stat(t.fsys, first); err != nil {
			t.serveVersion(w, r, first)
			return
		}
	}
	info, err := fs.Stat(t.fsys, name)
	if ref := (*refusal)(nil); errors.As(err, &ref) {
		t.fail(w, r, name, http.StatusForbidden, t.why(ref))
		return
	}
	if err != nil {
		t.fail(w, r, name, http.StatusNotFound, "Not found: "+name)
		return
	}
	if info.IsDir() && !strings.HasSuffix(r.URL.Path, "/") {
		// Without the slash, relative links in the listing resolve one level up.
		u := r.URL.EscapedPath() + "/"
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, u, http.StatusMovedPermanently)
		return
	}
	if q, ok := r.URL.Query()["search"]; ok && info.IsDir() {
		t.serveSearch(w, r, name, q[0])
		return
	}
	if _, ok := r.URL.Query()["backlinks"]; ok && !info.IsDir() {
		t.serveBacklinks(w, r, name)
		return
	}
	if _, log := r.URL.Query()["log"]; log {
		t.serveLog(w, r, name, info.IsDir())
		return
	}
	if hash, ok := r.URL.Query()["commit"]; ok {
		t.serveCommit(w, r, name, info.IsDir(), hash[0])
		return
	}
	if info.IsDir() {
		t.serveDir(w, r, name)
		return
	}
	if _, raw := r.URL.Query()["raw"]; raw {
		t.serveRaw(w, r, name, info)
		return
	}
	switch {
	case isMarkdown(name):
		t.serveMarkdown(w, r, name)
	case info.Size() <= maxCode:
		t.serveCode(w, r, name, info)
	default:
		t.serveRaw(w, r, name, info)
	}
}

func isMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

func isText(b []byte) bool {
	return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0
}

func (t *tree) serveMarkdown(w http.ResponseWriter, r *http.Request, name string) {
	b, err := fs.ReadFile(t.fsys, name)
	if err != nil {
		t.fail(w, r, name, http.StatusInternalServerError, err.Error())
		return
	}
	body, err := renderMarkdown(b, t.missing(name))
	if err != nil {
		t.fail(w, r, name, http.StatusInternalServerError, err.Error())
		return
	}
	body = template.HTML(t.logLine(r.Context(), name)) + body
	t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "doc", Body: body, Raw: true})
}

// missing reports, for the doc name, whether a relative link names
// nothing the reader could open. A link that climbs out of the tree may
// land in another tree, so it counts as found.
func (t *tree) missing(name string) func(string) bool {
	dir := path.Dir(name)
	return func(dest string) bool {
		target := path.Join(dir, dest)
		if target == ".." || strings.HasPrefix(target, "../") {
			return false
		}
		_, err := fs.Stat(t.fsys, target)
		return errors.Is(err, fs.ErrNotExist)
	}
}

// serveCode shows a text file as a highlighted code page with numbered,
// linkable lines; ?scopes draws each block of code as a box. Binary files
// go raw.
func (t *tree) serveCode(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo) {
	b, err := fs.ReadFile(t.fsys, name)
	if err != nil {
		t.fail(w, r, name, http.StatusInternalServerError, err.Error())
		return
	}
	if !isText(b) {
		t.serveRaw(w, r, name, info)
		return
	}
	var body strings.Builder
	body.WriteString(t.logLine(r.Context(), name))
	// The links to the other view and to the raw file float over the
	// code's top right corner; div.code bounds how far they follow the
	// page down.
	_, scoped := r.URL.Query()["scopes"]
	if scoped {
		body.WriteString(`<div class="code"><div class="view"><a href="?">flat</a><a href="?raw">raw</a></div>`)
	} else {
		body.WriteString(`<div class="code"><div class="view"><a href="?scopes">scopes</a><a href="?raw">raw</a></div>`)
	}
	body.WriteString(highlightPage(codeLexer(name), b, scoped))
	body.WriteString(`</div>`)
	t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "code", Body: template.HTML(body.String())})
}

// serveRaw sends the file itself. Text goes as plain text, so a stray .html
// or .ts file shows as source instead of rendering or downloading.
func (t *tree) serveRaw(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo) {
	f, err := t.fsys.Open(name)
	if err != nil {
		t.fail(w, r, name, http.StatusNotFound, "Not found: "+name)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		t.fail(w, r, name, http.StatusInternalServerError, "file is not seekable")
		return
	}
	if strings.ToLower(path.Ext(name)) != ".svg" {
		head := make([]byte, 512)
		n, _ := io.ReadFull(rs, head)
		if isText(head[:n]) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		rs.Seek(0, io.SeekStart)
	}
	http.ServeContent(w, r, name, info.ModTime(), rs)
}

func (t *tree) serveDir(w http.ResponseWriter, r *http.Request, name string) {
	entries, err := fs.ReadDir(t.fsys, name)
	if err != nil {
		t.fail(w, r, name, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		name string
		dir  bool
		ref  *refusal
	}
	// Entries reached through a link out of the tree are checked one by
	// one, since each may be refused. So is every entry of a shared tree.
	// A version holds only what git stored, so nothing leads out of it.
	outside := false
	if t.at == nil {
		_, err = t.root.Stat(name)
		outside = err != nil || t.shared
	}
	var items []item
	for _, e := range entries {
		n := e.Name()
		if t.unlisted(name, n) {
			continue
		}
		dir := e.IsDir()
		var ref *refusal
		if e.Type()&fs.ModeSymlink != 0 || outside {
			ref = t.refused(path.Join(name, n))
			if ref != nil {
				if ref.link == path.Join(name, n) {
					dir = ref.dir
				}
			} else if info, err := fs.Stat(t.fsys, path.Join(name, n)); err == nil {
				dir = info.IsDir()
			}
		}
		items = append(items, item{n, dir, ref})
	}
	slices.SortStableFunc(items, func(a, b item) int {
		if a.dir != b.dir {
			if a.dir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})

	var body strings.Builder
	if t.at != nil {
		body.WriteString(t.versionLine(r.Context(), name, true))
	}
	body.WriteString("<ul class=\"listing\">\n")
	if t.lastCommit(r.Context(), name) != nil {
		body.WriteString("<li class=\"log\"><a href=\"?log\">" + template.HTMLEscapeString(logLabel) + "</a></li>\n")
	}
	if name != "." {
		body.WriteString("<li><a href=\"../\">../</a></li>\n")
	}
	for _, it := range items {
		label := it.name
		if it.dir {
			label += "/"
		}
		href := (&url.URL{Path: label}).String()
		if it.ref != nil && t.shared {
			fmt.Fprintf(&body, "<li class=\"refused\">%s <span class=\"own\">refused</span></li>\n", template.HTMLEscapeString(label))
			continue
		}
		if it.ref != nil {
			fmt.Fprintf(&body, "<li class=\"refused\"><a href=\"%s\" title=\"%s\">%s</a> <span class=\"own\">refused</span></li>\n",
				template.HTMLEscapeString(href), template.HTMLEscapeString(it.ref.Error()), template.HTMLEscapeString(label))
			continue
		}
		fmt.Fprintf(&body, "<li><a href=\"%s\">%s</a></li>\n",
			template.HTMLEscapeString(href), template.HTMLEscapeString(label))
	}
	body.WriteString("</ul>\n")
	t.render(w, r, http.StatusOK, page{
		Title:  t.title(name),
		Crumbs: t.crumbs(name),
		Kind:   "dir",
		Body:   template.HTML(body.String()),
	})
}

type crumb struct {
	Label string
	Href  string
	Title string // shown on hover, eg the tree's full path
}

// indexCrumb leads every tree page's breadcrumbs to the index, the parent
// of all trees. Its label is not www, which a tree may be called.
var indexCrumb = crumb{Label: "index", Href: "/"}

type page struct {
	Title   string
	Crumbs  []crumb
	Kind    string
	Body    template.HTML
	Raw     bool
	Tree    string       // prefix the stop button withdraws, or "" for every tree
	Search  string       // the tree's URL, which answers ?search=, or "" off a tree
	Path    string       // the page's file or directory on disk, which the copy button copies; "" on a share or a failed page
	Status  string       // the server's age, on the index
	Style   template.CSS // the stylesheet inline, for a page the server may not outlive
	Stopped bool
	Shared  bool   // a shared server's page, which has no button
	NoStop  bool   // a page on the web address, which has no button
	Live    string // the URL of the page's live reload stream, or ""
	Built   *stamp // when the binary was built, above the header, with --stamp; none on a share
}

func (t *tree) title(name string) string {
	at := ""
	if t.at != nil {
		at = "@" + t.at.short
	}
	if name == "." {
		return path.Base(t.label) + at
	}
	if at != "" {
		return name + " " + at
	}
	return name
}

// rootCrumb is the tree's first breadcrumb: its base name, with the full
// path on hover. The address bar already shows the path.
func (t *tree) rootCrumb() crumb {
	if t.at != nil {
		return crumb{Label: path.Base(t.label) + "@" + t.at.short, Title: t.label + " as of " + t.at.hash}
	}
	return crumb{Label: path.Base(t.label), Title: t.label}
}

// crumbs links the index, the root and every parent directory; the last
// part is plain.
func (t *tree) crumbs(name string) []crumb {
	root := t.rootCrumb()
	out := []crumb{indexCrumb}
	// A repo on the web address links its host and account, which list
	// their repos.
	if t.web != nil {
		segs := strings.Split(t.web.key, "/")
		for i := range segs[:len(segs)-1] {
			out = append(out, crumb{Label: segs[i], Href: (&url.URL{Path: "/" + strings.Join(segs[:i+1], "/") + "/"}).String()})
		}
	}
	if name == "." {
		return append(out, root)
	}
	root.Href = t.prefixURL()
	out = append(out, root)
	parts := strings.Split(name, "/")
	for i, p := range parts {
		c := crumb{Label: p}
		if i < len(parts)-1 {
			c.Href = (&url.URL{Path: t.prefix + strings.Join(parts[:i+1], "/") + "/"}).String()
		}
		out = append(out, c)
	}
	return out
}

func render(w http.ResponseWriter, r *http.Request, status int, p page) {
	p.Live = liveURL(r)
	if showBuilt && !p.Shared && !built.IsZero() {
		p.Built = &stamp{Clock: clock(built), Millis: built.UnixMilli(), Ago: int(time.Since(built).Seconds()),
			Commit: buildCommit, Rendered: rendered(r)}
	}
	var b bytes.Buffer
	if err := layout.ExecuteTemplate(&b, "page", p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// no-store keeps rendered pages out of the back/forward cache, so Back
	// after Stop shows the server is gone instead of a dead snapshot.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(b.Bytes())
}

// render gives the page this tree's Delete lock button, unless the tree is
// shared, and a search box for the directory the page is in, with a button
// that copies the page's path on disk. A failed page searches the whole
// tree, since its directory may not exist, and has no path to copy.
func (t *tree) render(w http.ResponseWriter, r *http.Request, status int, p page) {
	p.Tree, p.Shared, p.Search = t.prefix, t.shared, t.prefixURL()
	if t.web != nil {
		p.NoStop, p.Status = true, t.web.status()
	}
	// A version never changes and has no lock or path on disk: no live
	// reload, no stop button, nothing to copy.
	if t.at != nil {
		p.NoStop = true
		r = r.WithContext(context.WithValue(r.Context(), liveKey{}, ""))
	}
	if status == http.StatusOK {
		p.Search = searchURL(r.URL.Path)
		if !t.shared && t.at == nil {
			p.Path = filepath.Join(t.dir, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, t.prefix)))
		}
	}
	render(w, r, status, p)
}

// searchURL is the directory a page at urlPath is in: a listing itself,
// or a file's directory.
func searchURL(urlPath string) string {
	return (&url.URL{Path: urlPath[:strings.LastIndex(urlPath, "/")+1]}).String()
}

// why is what a refusal page says: the reason in full, or on a shared tree
// only that the name is refused, since the reason names paths outside it.
func (t *tree) why(ref *refusal) string {
	if t.shared {
		return "Refused: " + ref.link
	}
	return ref.Error()
}

func (t *tree) fail(w http.ResponseWriter, r *http.Request, name string, status int, msg string) {
	// The request's own entry says not found; a refusal or fault says why
	// only here.
	switch {
	case status >= 500:
		logErr("%s: %s", t.dir, msg)
	case status != http.StatusNotFound:
		logInfo("%s: %s", t.dir, msg)
	}
	t.render(w, r, status, page{
		Title:  t.title(name),
		Crumbs: t.crumbs(name),
		Kind:   "error",
		Body:   template.HTML("<pre class=\"error\">" + template.HTMLEscapeString(msg) + "</pre>"),
	})
}
