package www

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// maxCode caps the size of a file shown as a code page; larger files go raw.
const maxCode = 1 << 20

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
	if first, rest, ok := t.versionIn(r.URL.Path); ok {
		t.serveVersion(w, r, first, rest)
		return
	}
	name := t.name(r.URL.Path)
	// A first segment such as @5430ac3 named a version before versions
	// moved beside the tree's name, unless the tree holds an entry by that
	// name. It moves there for good, so old links still lead to it.
	if first, rest, _ := strings.Cut(name, "/"); t.hasVersions() && atSegment.MatchString(first) {
		if _, err := fs.Stat(t.fsys, first); err != nil {
			if t.prefix == rooted("/") {
				t.serveVersion(w, r, first, rest)
				return
			}
			u := (&url.URL{Path: versionPrefix(t.prefix, first) + rest}).String()
			if strings.HasSuffix(r.URL.Path, "/") && rest != "" {
				u += "/"
			}
			if r.URL.RawQuery != "" {
				u += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, u, http.StatusMovedPermanently)
			return
		}
	}
	info, err := fs.Stat(t.fsys, name)
	if ref := (*refusal)(nil); errors.As(err, &ref) {
		t.fail(w, r, name, http.StatusForbidden, t.why(ref))
		return
	}
	if err != nil {
		t.notFound(w, r, name)
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
	if _, ok := r.URL.Query()["timeline"]; ok {
		t.serveTimeline(w, r, name, info.IsDir())
		return
	}
	if hash, ok := r.URL.Query()["commit"]; ok {
		if q := r.URL.Query(); q.Has("decl") {
			t.serveDecl(w, r, name, info.IsDir(), hash[0], q.Get("file"), q.Get("decl"))
			return
		}
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
	body, err := renderMarkdown(b, t.missing(name), t.webBase())
	if err != nil {
		t.fail(w, r, name, http.StatusInternalServerError, err.Error())
		return
	}
	body = template.HTML(t.logLine(r.Context(), name, b)) + body
	t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "doc", Body: body, Raw: true})
}

// webBase is where t's docs send a link to a forge: the web address, the
// base path alone on the web address itself, or nil where no server here serves it, eg on
// a share.
func (t *tree) webBase() *string {
	if t.base != nil {
		return t.base.webBase()
	}
	switch s := t.srv; {
	case s == nil:
		return nil
	case s.web != nil:
		return &basePath
	case s.webURL != "":
		return &s.webURL
	}
	return nil
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
// linkable lines; ?scopes draws each block of code as a box, ?tree
// shows the syntax tree, ?gron shows a JSON file as one line per node, and
// ?json, ?yaml and ?keyval show a file in one of those formats in another.
// Binary files go raw.
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
	body.WriteString(t.logLine(r.Context(), name, b))
	// The links to the other views and to the raw file float over the
	// code's top right corner; div.code bounds how far they follow the
	// page down. A tree that cannot be had says why above the flat code.
	q := r.URL.Query()
	_, scoped := q["scopes"]
	if _, ok := q["gron"]; ok && isJSON(name) {
		out, err := gron(b)
		if err == nil {
			body.WriteString(`<div class="code">` + codeViews(name, "gron") + highlightPage(gronLexer, out, false) + `</div>`)
			t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "code", Body: template.HTML(body.String())})
			return
		}
		body.WriteString("<p class=\"own warn\">No gron view, since this is not valid JSON: " + template.HTMLEscapeString(err.Error()) + "</p>\n")
	}
	if from := formatOf(name); from != nil {
		if to := formatView(from, q); to != nil {
			code, err := formatPage(b, from, to)
			if err == nil {
				body.WriteString(formatNote(name, from, to))
				body.WriteString(`<div class="code">` + codeViews(name, to.name) + code + `</div>`)
				t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "code", Body: template.HTML(body.String())})
				return
			}
			body.WriteString(formatFail(from, to, err))
		}
	}
	if _, ok := q["tree"]; ok {
		tree, err := syntaxPage(r.Context(), name, pageHref(r.URL.Path), b)
		if err == nil {
			// A large file's tree is megabytes, so it is written once,
			// into room made for it.
			body.Grow(len(tree) + 1024)
			body.WriteString(`<div class="code">` + codeViews(name, "tree"))
			body.WriteString(tree)
			body.WriteString(`</div>`)
			t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "tree", Body: template.HTML(body.String())})
			return
		}
		logErr("no syntax tree for %s: %v", name, err)
		body.WriteString(treeNote(name, err))
	}
	view := "flat"
	if scoped {
		view = "scopes"
	}
	body.WriteString(`<div class="code">` + codeViews(name, view))
	body.WriteString(highlightPage(codeLexer(name), b, scoped))
	body.WriteString(`</div>`)
	t.render(w, r, http.StatusOK, page{Title: name, Crumbs: t.crumbs(name), Kind: "code", Body: template.HTML(body.String())})
}

// serveRaw sends the file itself. Text goes as plain text, so a stray .html
// or .ts file shows as source instead of rendering or downloading.
func (t *tree) serveRaw(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo) {
	f, err := t.fsys.Open(name)
	if err != nil {
		t.notFound(w, r, name)
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
		info fs.FileInfo // nil when unknown or refused
	}
	details := r.URL.Query().Has("details")
	sortKey, sortRev := detailSort(r.URL.Query().Get("details"), t.at != nil)
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
		var info fs.FileInfo
		if e.Type()&fs.ModeSymlink != 0 || outside {
			ref = t.refused(path.Join(name, n))
			if ref != nil {
				if ref.link == path.Join(name, n) {
					dir = ref.dir
				}
			} else if i, err := fs.Stat(t.fsys, path.Join(name, n)); err == nil {
				dir, info = i.IsDir(), i
			}
		} else if details {
			info, _ = e.Info()
		}
		items = append(items, item{n, dir, ref, info})
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
	// A sorted details view orders by size or time as ls does, largest or
	// newest first. An entry without the value goes last and keeps its
	// name order: a directory under -S, a refused entry under either.
	if sortKey != "" {
		val := func(it item) int64 {
			switch {
			case it.info == nil, sortKey == "S" && it.dir:
				return math.MinInt64
			case sortKey == "S":
				return it.info.Size()
			}
			return it.info.ModTime().UnixNano()
		}
		slices.SortStableFunc(items, func(a, b item) int { return cmp.Compare(val(b), val(a)) })
		if sortRev {
			slices.Reverse(items)
		}
	}

	var body strings.Builder
	if t.at != nil {
		body.WriteString(t.versionLine(r.Context(), name, true))
	}
	body.WriteString("<ul class=\"listing\">\n")
	// The first row names the commands behind the directory's views: its
	// names, its details and its history, when it has one. The view on
	// screen shows as text, the others as links. The details view stays on
	// while the reader moves between directories, as a file manager's does.
	view, lead := "", ""
	flags := sortFlags(sortKey, sortRev)
	if details {
		view = detailsQuery(flags)
		lead = detailFields(nil, true, true, time.Time{})
	}
	cmd := func(label, href string, here bool) string {
		if here {
			return "<span>" + template.HTMLEscapeString(label) + "</span>"
		}
		return "<a href=\"" + href + "\">" + template.HTMLEscapeString(label) + "</a>"
	}
	// The history sits at the row's right end, apart from the two
	// listing views.
	body.WriteString("<li class=\"cmds\">" + cmd(namesLabel, "./", !details) + " " + cmd(detailsCmd(flags, details), "?details", details))
	if t.lastCommit(r.Context(), name) != nil {
		body.WriteString(" <a class=\"history\" href=\"?log\">" + template.HTMLEscapeString(logLabel) + "</a>")
	}
	body.WriteString("</li>\n")
	if details {
		body.WriteString(detailHeader(sortKey, sortRev, t.at != nil))
	}
	if name != "." {
		body.WriteString("<li>" + lead + "<a href=\"../" + view + "\">../</a></li>\n")
	}
	now := time.Now()
	for _, it := range items {
		label := it.name
		if it.dir {
			label += "/"
		}
		href := (&url.URL{Path: label}).String()
		if it.dir {
			href += view
		}
		if details {
			lead = detailFields(it.info, it.dir, t.at != nil, now)
		}
		if it.ref != nil && (t.shared || t.public) {
			fmt.Fprintf(&body, "<li class=\"refused\">%s%s <span class=\"own\">refused</span></li>\n", lead, template.HTMLEscapeString(label))
			continue
		}
		if it.ref != nil {
			fmt.Fprintf(&body, "<li class=\"refused\">%s<a href=\"%s\" title=\"%s\">%s</a> <span class=\"own\">refused</span></li>\n",
				lead, template.HTMLEscapeString(href), template.HTMLEscapeString(it.ref.Error()), template.HTMLEscapeString(label))
			continue
		}
		fmt.Fprintf(&body, "<li>%s<a href=\"%s\">%s</a></li>\n",
			lead, template.HTMLEscapeString(href), template.HTMLEscapeString(label))
	}
	body.WriteString("</ul>\n")
	t.render(w, r, http.StatusOK, page{
		Title:  t.title(name),
		Crumbs: t.crumbs(name),
		Kind:   "dir",
		Body:   template.HTML(body.String()),
	})
}
