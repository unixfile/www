package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

//go:embed assets/style.css
var styleCSS []byte

//go:embed assets/layout.html
var layoutHTML string

//go:embed assets/lines.lua
var linesLua []byte

var layout = template.Must(template.New("layout").Parse(layoutHTML))

// maxCode caps the size of a file shown as a code page; larger files go raw.
const maxCode = 1 << 20

// csp forbids every script. Pandoc passes raw HTML through, and a README
// in a cloned repo must not run code on this origin.
const csp = "default-src 'none'; img-src 'self' data:; media-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

type server struct {
	fsys   fs.FS
	prefix string // URL path of the served directory, eg /scratch/notes/
	label  string // the served directory as the first breadcrumb, eg ~/scratch/notes
	filter string // path of lines.lua, or "" to join lines as standard Markdown does
	stop   func()
}

func newServer(root *os.Root, prefix, label, filter string) *server {
	return &server{fsys: root.FS(), prefix: prefix, label: label, filter: filter, stop: func() {}}
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

func (s *server) prefixURL() string {
	return (&url.URL{Path: s.prefix}).String()
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_mdserve/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Write(styleCSS)
	})
	mux.HandleFunc("POST /_mdserve/stop", s.handleStop)
	mux.HandleFunc("GET /", s.handlePath)
	return guard(mux)
}

// guard rejects requests for any host but this machine, which defeats DNS
// rebinding, and sets the headers every response needs.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		switch host {
		case "127.0.0.1", "localhost", "::1":
		default:
			http.Error(w, "mdserve answers only to localhost", http.StatusMisdirectedRequest)
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

// handleStop serves the stop button. Any site can submit a form to
// localhost, so the request must come from a page of this server.
func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "stop must come from an mdserve page", http.StatusForbidden)
		return
	}
	s.render(w, http.StatusOK, page{
		Title:   "mdserve stopped",
		Crumbs:  []crumb{{Label: s.label}},
		Kind:    "stopped",
		Body:    "<p>mdserve stopped and removed its lockfile. This tab can be closed.</p>",
		Stopped: true,
	})
	// Shutdown waits for this response to finish, so it must not run here.
	go s.stop()
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
	rest, ok := strings.CutPrefix(r.URL.Path, s.prefix)
	if !ok {
		if r.URL.Path == "/" || r.URL.Path+"/" == s.prefix {
			// Temporary: the next server on this port may serve another tree.
			http.Redirect(w, r, s.prefixURL(), http.StatusFound)
			return
		}
		s.fail(w, ".", http.StatusNotFound, "Not found: "+r.URL.Path+"\nThis server serves "+s.label+" at "+s.prefix)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+rest), "/")
	if name == "" {
		name = "."
	}
	info, err := fs.Stat(s.fsys, name)
	if err != nil {
		s.fail(w, name, http.StatusNotFound, "Not found: "+name)
		return
	}
	if info.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			// Without the slash, relative links in the README resolve one level up.
			http.Redirect(w, r, r.URL.EscapedPath()+"/", http.StatusMovedPermanently)
			return
		}
		s.serveDir(w, r, name)
		return
	}
	if _, raw := r.URL.Query()["raw"]; raw {
		s.serveRaw(w, r, name, info)
		return
	}
	switch {
	case isMarkdown(name):
		s.serveMarkdown(w, r, name)
	case info.Size() <= maxCode:
		s.serveCode(w, r, name, info)
	default:
		s.serveRaw(w, r, name, info)
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

func (s *server) serveMarkdown(w http.ResponseWriter, r *http.Request, name string) {
	b, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		s.fail(w, name, http.StatusInternalServerError, err.Error())
		return
	}
	body, err := s.pandoc(r.Context(), b, true)
	if err != nil {
		s.fail(w, name, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, http.StatusOK, page{Title: name, Crumbs: s.crumbs(name), Kind: "doc", Body: body, Raw: true})
}

// serveCode shows a text file as a highlighted code page with numbered,
// linkable lines. Binary files go raw.
func (s *server) serveCode(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo) {
	b, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		s.fail(w, name, http.StatusInternalServerError, err.Error())
		return
	}
	if !isText(b) {
		s.serveRaw(w, r, name, info)
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
	body, err := s.pandoc(r.Context(), md.Bytes(), false)
	if err != nil {
		s.fail(w, name, http.StatusInternalServerError, err.Error())
		return
	}
	body = template.HTML(lineAnchors.Replace(string(body)))
	s.render(w, http.StatusOK, page{Title: name, Crumbs: s.crumbs(name), Kind: "code", Body: body, Raw: true})
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
func (s *server) serveRaw(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo) {
	f, err := s.fsys.Open(name)
	if err != nil {
		s.fail(w, name, http.StatusNotFound, "Not found: "+name)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		s.fail(w, name, http.StatusInternalServerError, "file is not seekable")
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

func (s *server) serveDir(w http.ResponseWriter, r *http.Request, name string) {
	entries, err := fs.ReadDir(s.fsys, name)
	if err != nil {
		s.fail(w, name, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		name string
		dir  bool
	}
	var items []item
	readme := ""
	for _, e := range entries {
		n := e.Name()
		if isLockName(n) {
			continue
		}
		dir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			if info, err := fs.Stat(s.fsys, path.Join(name, n)); err == nil {
				dir = info.IsDir()
			}
		}
		if !dir && readme == "" && strings.EqualFold(strings.TrimSuffix(n, path.Ext(n)), "readme") && isMarkdown(n) {
			readme = n
		}
		items = append(items, item{n, dir})
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
		fmt.Fprintf(&body, "<li><a href=\"%s\">%s</a></li>\n",
			template.HTMLEscapeString(href), template.HTMLEscapeString(label))
	}
	body.WriteString("</ul>\n")
	if readme != "" {
		b, err := fs.ReadFile(s.fsys, path.Join(name, readme))
		if err == nil {
			if html, err := s.pandoc(r.Context(), b, true); err == nil {
				body.WriteString("<article class=\"readme\">\n" + string(html) + "</article>\n")
			}
		}
	}
	s.render(w, http.StatusOK, page{
		Title:  s.title(name),
		Crumbs: s.crumbs(name),
		Kind:   "dir",
		Body:   template.HTML(body.String()),
	})
}

// pandoc converts on every request; there is no cache yet. The request
// context kills pandoc when the browser goes away. A doc gets lines.lua,
// so each source line becomes a paragraph; a code page does not. Bare URLs
// become links, as on GitHub.
func (s *server) pandoc(ctx context.Context, input []byte, doc bool) (template.HTML, error) {
	args := []string{"--sandbox", "--preserve-tabs", "-f", "markdown+autolink_bare_uris", "-t", "html5"}
	if doc && s.filter != "" {
		args = append(args, "--lua-filter", s.filter)
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
}

type page struct {
	Title   string
	Crumbs  []crumb
	Kind    string
	Body    template.HTML
	Raw     bool
	Stopped bool
}

func (s *server) title(name string) string {
	if name == "." {
		return s.label
	}
	return name
}

// crumbs links the root and every parent directory; the last part is plain.
func (s *server) crumbs(name string) []crumb {
	if name == "." {
		return []crumb{{Label: s.label}}
	}
	out := []crumb{{Label: s.label, Href: s.prefixURL()}}
	parts := strings.Split(name, "/")
	for i, p := range parts {
		c := crumb{Label: p}
		if i < len(parts)-1 {
			c.Href = (&url.URL{Path: s.prefix + strings.Join(parts[:i+1], "/") + "/"}).String()
		}
		out = append(out, c)
	}
	return out
}

func (s *server) render(w http.ResponseWriter, status int, p page) {
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

func (s *server) fail(w http.ResponseWriter, name string, status int, msg string) {
	s.render(w, status, page{
		Title:  s.title(name),
		Crumbs: s.crumbs(name),
		Kind:   "error",
		Body:   template.HTML("<pre class=\"error\">" + template.HTMLEscapeString(msg) + "</pre>"),
	})
}
