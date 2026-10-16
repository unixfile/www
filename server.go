package main

import (
	"bytes"
	"context"
	"embed"
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
	"os/exec"
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

//go:embed assets/layout.html
var layoutHTML string

//go:embed assets/lines.lua
var linesLua []byte

// fontFS holds STIX Two Text, which the stylesheet takes from the machine
// when installed and from here otherwise. A changed font needs a new file
// name, since browsers keep these for a year.
//
//go:embed assets/fonts/*.otf
var fontFS embed.FS

var layout = template.Must(template.New("layout").Funcs(template.FuncMap{
	"liveJS": func() template.JS { return template.JS(liveJS) },
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
	stop   func([]*tree) // withdraws trees; set by the hub
	about  about
	shared bool  // on an address beyond loopback: it shows the trees and nothing of the machine
	live   *live // nil in tests that do without live reload
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
	return "mdserve --restart"
}

// status is the index header's account of the server's age.
func (a about) status() string {
	if a.started.IsZero() {
		return ""
	}
	s := "up since " + clock(a.started)
	if a.exeInfo != nil {
		s += ", built " + clock(a.exeInfo.ModTime())
	}
	return s
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
	return &server{stop: func([]*tree) {}}
}

// tree is one served directory, exposed with everything below it.
type tree struct {
	dir      string
	realDir  string // dir with its links resolved
	prefix   string // URL path of the tree, eg /scratch/notes/
	label    string // the tree as the first breadcrumb, eg ~/scratch/notes
	fsys     fs.FS
	root     *os.Root
	filter   string      // path of lines.lua, or "" to join lines as standard Markdown does
	lock     *lock       // nil only in tests
	link     string      // registry link
	linkInfo os.FileInfo // the link this server knows, to tell it from a new join
	wd       int         // inotify watch on dir, or -1
	shared   bool        // served beyond loopback: only what other users could read
	secret   bool        // mounted under a token, and left off the index
}

func newTree(root *os.Root, dir, prefix, label, filter string) *tree {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		real = dir
	}
	t := &tree{dir: dir, realDir: real, prefix: prefix, label: label, root: root, filter: filter, wd: -1}
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

// writeFilter puts lines.lua where pandoc can read it; pandoc takes a
// filter only as a path. The caller removes the file.
func writeFilter() (string, error) {
	f, err := os.CreateTemp("", "mdserve-*.lua")
	if err != nil {
		return "", err
	}
	_, err = f.Write(linesLua)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
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
	mux.HandleFunc("GET /_mdserve/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(styleCSS)
	})
	mux.HandleFunc("GET /_mdserve/fonts/{name}", func(w http.ResponseWriter, r *http.Request) {
		b, err := fontFS.ReadFile("assets/fonts/" + path.Base(r.PathValue("name")))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "font/otf")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Write(b)
	})
	// A shared server has no buttons: no visitor may stop it.
	if !s.shared {
		mux.HandleFunc("POST /_mdserve/stop", s.handleStop)
	}
	mux.HandleFunc("GET /_mdserve/live", s.handleLive)
	mux.HandleFunc("GET /", s.handlePath)
	return guard(mux, s.shared)
}

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
			http.Error(w, "mdserve answers only to this machine", http.StatusMisdirectedRequest)
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
// file maps to loopback, eg an mdserve instance named there. DNS is never
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
		http.Error(w, "stop must come from an mdserve page", http.StatusForbidden)
		return
	}
	trees, crumbs, what := s.list(), []crumb{{Label: "mdserve"}}, "Stopped mdserve"
	if prefix := r.PostFormValue("tree"); prefix != "" {
		t := s.byPrefix(prefix)
		if t == nil {
			s.serveIndex(w, r, http.StatusNotFound, "Not served: "+prefix)
			return
		}
		trees, crumbs, what = []*tree{t}, []crumb{t.rootCrumb()}, "Stopped serving "+t.label
		// While other trees remain the server lives on: a button on the
		// index returns there, and a tree's page links it.
		if len(s.list()) > 1 {
			if r.PostFormValue("from") == "index" {
				s.stop(trees)
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
		Title:  "mdserve stopped",
		Crumbs: crumbs,
		Kind:   "stopped",
		// The server may close its listener before the browser asks for
		// the stylesheet, so the page carries it.
		Style:   template.CSS(styleCSS),
		Body:    template.HTML("<p class=\"mdserve\">" + template.HTMLEscapeString(what) + ". Close this tab.</p>"),
		Stopped: true,
	})
	// stop only withdraws; the hub shuts down on its own goroutine, since
	// Shutdown waits for this response to finish.
	s.stop(trees)
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
	if _, raw := r.URL.Query()["raw"]; !raw {
		r = s.withLive(r)
	}
	t := s.lookup(r.URL.Path)
	switch {
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
		body.WriteString("<p class=\"mdserve warn\">The binary changed after this server started. Run <code>" + esc(s.about.restartCmd()) + "</code>.</p>\n")
	}
	trees := s.list()
	if len(trees) == 0 {
		body.WriteString("<p class=\"mdserve\">No tree is served.</p>\n")
	} else {
		body.WriteString("<table class=\"locks mdserve\">\n")
		for _, t := range trees {
			lockPath, since := "", ""
			if t.lock != nil {
				lockPath, since = t.lock.path, "since "+clock(t.lock.info.ModTime())
			}
			fmt.Fprintf(&body, "<tr><td><a href=\"%s\" title=\"%s\">%s</a> <span class=\"since\">%s</span></td>"+
				"<td><form method=\"post\" action=\"/_mdserve/stop\"><input type=\"hidden\" name=\"tree\" value=\"%s\">"+
				"<input type=\"hidden\" name=\"from\" value=\"index\"><button>Delete lock</button></form></td></tr>\n",
				esc(t.prefixURL()), esc(lockPath), segments(t.label), since, esc(t.prefix))
		}
		body.WriteString("</table>\n")
	}
	if others := s.otherTrees(); len(others) > 0 {
		body.WriteString("<h2 class=\"mdserve\">Other addresses</h2>\n<table class=\"locks mdserve\">\n")
		for _, t := range others {
			fmt.Fprintf(&body, "<tr><td><a href=\"%s\" title=\"%s\">%s</a> <span class=\"since\">since %s</span></td></tr>\n",
				esc(t.url), esc(filepath.Join(t.dir, lockName(t.ep))), segments(t.url), clock(t.since))
		}
		body.WriteString("</table>\n")
	}
	render(w, r, status, page{Title: "mdserve", Crumbs: []crumb{{Label: "index"}}, Kind: "index",
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
		body.WriteString("<p class=\"mdserve\">No tree is listed here.</p>\n")
	} else {
		body.WriteString("<ul class=\"listing\">\n")
		for _, t := range trees {
			fmt.Fprintf(&body, "<li><a href=\"%s\">%s/</a></li>\n", esc(t.prefixURL()), esc(t.label))
		}
		body.WriteString("</ul>\n")
	}
	render(w, r, status, page{Title: "mdserve", Crumbs: []crumb{{Label: "index"}}, Kind: "index",
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
	info, err := fs.Stat(t.fsys, name)
	if ref := (*refusal)(nil); errors.As(err, &ref) {
		t.fail(w, r, name, http.StatusForbidden, t.why(ref))
		return
	}
	if err != nil {
		t.fail(w, r, name, http.StatusNotFound, "Not found: "+name)
		return
	}
	if info.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			// Without the slash, relative links in the README resolve one level up.
			http.Redirect(w, r, r.URL.EscapedPath()+"/", http.StatusMovedPermanently)
			return
		}
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

func isReadme(name string) bool {
	return strings.EqualFold(strings.TrimSuffix(name, path.Ext(name)), "readme") && isMarkdown(name)
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
	body, err := t.pandoc(r.Context(), b, true)
	if err != nil {
		t.fail(w, r, name, http.StatusInternalServerError, err.Error())
		return
	}
	t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "doc", Body: body, Raw: true})
}

// serveCode shows a text file as a highlighted code page with numbered,
// linkable lines. Binary files go raw.
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
	fence := strings.Repeat("`", max(3, longestRun(b, '`')+1))
	var md bytes.Buffer
	fmt.Fprintf(&md, "%s{%s .numberLines}\n", fence, langClass(name))
	md.Write(b)
	if len(b) > 0 && b[len(b)-1] != '\n' {
		md.WriteByte('\n')
	}
	md.WriteString(fence + "\n")
	body, err := t.pandoc(r.Context(), md.Bytes(), false)
	if err != nil {
		t.fail(w, r, name, http.StatusInternalServerError, err.Error())
		return
	}
	body = template.HTML(lineAnchors.Replace(string(body)))
	t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "code", Body: body, Raw: true})
}

// lineAnchors renames pandoc's line ids from cb1-42 to L42, the form GitHub
// uses, so links such as main.go#L42 written for GitHub land on the line.
// Pandoc escapes quotes in code, so file content cannot match.
var lineAnchors = strings.NewReplacer(`id="cb1-`, `id="L`, `href="#cb1-`, `href="#L`)

// langClass returns the pandoc class that picks a highlighter. Pandoc
// matches a file extension as well as a language name.
func langClass(name string) string {
	base := strings.ToLower(path.Base(name))
	switch base {
	case "makefile", "gnumakefile", "justfile":
		return ".makefile"
	case "dockerfile", "containerfile":
		return ".dockerfile"
	}
	ext := strings.TrimPrefix(path.Ext(base), ".")
	if ext == "" || strings.ContainsFunc(ext, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '_' || c == '-')
	}) {
		return ""
	}
	return "." + ext
}

func longestRun(b []byte, c byte) int {
	best, n := 0, 0
	for _, x := range b {
		if x == c {
			n++
			best = max(best, n)
		} else {
			n = 0
		}
	}
	return best
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
	_, err = t.root.Stat(name)
	outside := err != nil || t.shared
	var items []item
	readme := ""
	for _, e := range entries {
		n := e.Name()
		if isLockName(n) {
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
		if ref == nil && !dir && readme == "" && isReadme(n) {
			readme = n
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
	body.WriteString("<ul class=\"listing\">\n")
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
			fmt.Fprintf(&body, "<li class=\"refused\">%s <span class=\"mdserve\">refused</span></li>\n", template.HTMLEscapeString(label))
			continue
		}
		if it.ref != nil {
			fmt.Fprintf(&body, "<li class=\"refused\"><a href=\"%s\" title=\"%s\">%s</a> <span class=\"mdserve\">refused</span></li>\n",
				template.HTMLEscapeString(href), template.HTMLEscapeString(it.ref.Error()), template.HTMLEscapeString(label))
			continue
		}
		fmt.Fprintf(&body, "<li><a href=\"%s\">%s</a></li>\n",
			template.HTMLEscapeString(href), template.HTMLEscapeString(label))
	}
	body.WriteString("</ul>\n")
	if readme != "" {
		b, err := fs.ReadFile(t.fsys, path.Join(name, readme))
		if err == nil {
			if html, err := t.pandoc(r.Context(), b, true); err == nil {
				body.WriteString("<article class=\"readme\">\n" + string(html) + "</article>\n")
			}
		}
	}
	t.render(w, r, http.StatusOK, page{
		Title:  t.title(name),
		Crumbs: t.crumbs(name),
		Kind:   "dir",
		Body:   template.HTML(body.String()),
	})
}

// pandoc converts on every request; there is no cache yet. The request
// context kills pandoc when the browser goes away. A doc gets lines.lua,
// so each source line becomes a paragraph; a code page does not. Bare URLs
// become links, as on GitHub.
func (t *tree) pandoc(ctx context.Context, input []byte, doc bool) (template.HTML, error) {
	args := []string{"--sandbox", "--preserve-tabs", "-f", "markdown+autolink_bare_uris", "-t", "html5"}
	if doc && t.filter != "" {
		args = append(args, "--lua-filter", t.filter)
	}
	cmd := exec.CommandContext(ctx, "pandoc", args...)
	cmd.Stdin = bytes.NewReader(input)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pandoc: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return template.HTML(out.String()), nil
}

type crumb struct {
	Label string
	Href  string
	Title string // shown on hover, eg the tree's full path
}

// indexCrumb leads every tree page's breadcrumbs to the index, the parent
// of all trees. Its label is not mdserve, which a tree may be called.
var indexCrumb = crumb{Label: "index", Href: "/"}

type page struct {
	Title   string
	Crumbs  []crumb
	Kind    string
	Body    template.HTML
	Raw     bool
	Tree    string       // prefix the stop button withdraws, or "" for every tree
	Status  string       // the server's age, on the index
	Style   template.CSS // the stylesheet inline, for a page the server may not outlive
	Stopped bool
	Shared  bool   // a shared server's page, which has no button
	Live    string // the URL of the page's live reload stream, or ""
}

func (t *tree) title(name string) string {
	if name == "." {
		return path.Base(t.label)
	}
	return name
}

// rootCrumb is the tree's first breadcrumb: its base name, with the full
// path on hover. The address bar already shows the path.
func (t *tree) rootCrumb() crumb {
	return crumb{Label: path.Base(t.label), Title: t.label}
}

// crumbs links the index, the root and every parent directory; the last
// part is plain.
func (t *tree) crumbs(name string) []crumb {
	root := t.rootCrumb()
	if name == "." {
		return []crumb{indexCrumb, root}
	}
	root.Href = t.prefixURL()
	out := []crumb{indexCrumb, root}
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
// shared.
func (t *tree) render(w http.ResponseWriter, r *http.Request, status int, p page) {
	p.Tree, p.Shared = t.prefix, t.shared
	render(w, r, status, p)
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
	t.render(w, r, status, page{
		Title:  t.title(name),
		Crumbs: t.crumbs(name),
		Kind:   "error",
		Body:   template.HTML("<pre class=\"error\">" + template.HTMLEscapeString(msg) + "</pre>"),
	})
}
