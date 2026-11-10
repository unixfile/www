package www

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// leadStyle sets a list's --lead to the width of its longest label, so
// the labels that lead its rows line up: in monospace a width in ch fits
// every label exactly.
func leadStyle(labels []string) string {
	n := 0
	for _, l := range labels {
		n = max(n, utf8.RuneCountInString(l))
	}
	return fmt.Sprintf(" style=\"--lead:%dch\"", n)
}

// errorLine is a failure's message as a page shows it.
func errorLine(msg string) string {
	return "<pre class=\"error\">" + template.HTMLEscapeString(msg) + "</pre>\n"
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
	Tree    string        // prefix the stop button withdraws, or "" for every tree
	Search  string        // the tree's URL, which answers ?search=, or "" off a tree
	Recents string        // the tree whose pages a search lists as recent; a version's is its tree's
	Status  template.HTML // the server's age, on the index, or a repo's commit on the web address
	Style   template.CSS  // the stylesheet inline, for a page the server may not outlive
	Stopped bool
	Bug     bool   // the bug button at the page's foot
	Shared  bool   // a shared server's page, which has no button
	NoStop  bool   // a page on the web address, which has no button
	Live    string // the URL of the page's live reload stream, or ""
	Fixed   bool   // a version, which never changes, or a page that must not reload: the script runs but holds no stream
	// Forge is, for a page of a repo on the web address, the forge's own
	// permanent URL for it, which the share button copies; ForgeLines is
	// how that forge marks lines.
	Forge, ForgeLines string
	Built             *stamp // when the binary was built, above the header, with --debug; none on a share
}

func (t *tree) title(name string) string {
	at := ""
	if t.at != nil {
		at = " @ " + t.atLabel
	}
	if name == "." {
		return path.Base(t.label) + at
	}
	if at != "" {
		return name + at
	}
	return name
}

// rootCrumb is the tree's first breadcrumb: its base name, with the full
// path on hover. The address bar already shows the path.
func (t *tree) rootCrumb() crumb {
	if t.at != nil {
		return crumb{Label: path.Base(t.label) + " @ " + t.atLabel, Title: t.label + " as of " + t.at.hash}
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
			out = append(out, crumb{Label: segs[i], Href: (&url.URL{Path: rooted("/" + strings.Join(segs[:i+1], "/") + "/")}).String()})
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
	if p.Live == "" {
		p.Live = liveURL(r)
	}
	p.Bug = !bugOff && !p.Shared && !p.Stopped
	if debugOn && !p.Shared && !built.IsZero() {
		p.Built = &stamp{Clock: clock(built), Millis: built.UnixMilli(), Ago: int(time.Since(built).Seconds()),
			Commit: buildCommit, Rendered: rendered(r)}
	}
	var b bytes.Buffer
	// The body is most of the page, and can be megabytes.
	b.Grow(len(p.Body) + 16<<10)
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
// Recent pages stay within the tree, so one repo's reading never shows in
// another's search.
func (t *tree) render(w http.ResponseWriter, r *http.Request, status int, p page) {
	p.Tree, p.Shared, p.Search = t.prefix, t.shared, t.prefixURL()
	p.Recents = t.prefixURL()
	if t.base != nil {
		p.Recents = t.base.prefixURL()
	}
	if t.web != nil {
		p.NoStop, p.Status = true, t.web.status(r.Context(), t.prefix, r.URL.Path)
	}
	// A version never changes and has no lock: no live reload and no stop
	// button. Its page still runs the script, for the keys, the search box,
	// line marks and the copy button.
	if t.at != nil {
		p.NoStop, p.Fixed = true, true
	}
	if status == http.StatusOK {
		p.Search = searchURL(r.URL.Path)
		p.Forge, p.ForgeLines = t.forgeLink(r, p.Kind)
	}
	render(w, r, status, p)
}

// searchURL is the directory a page at urlPath is in: a listing itself,
// or a file's directory.
func searchURL(urlPath string) string {
	return (&url.URL{Path: urlPath[:strings.LastIndex(urlPath, "/")+1]}).String()
}

// why is what a refusal page says: the reason in full, or on a shared or
// public tree only that the name is refused, since the reason names paths
// outside it.
func (t *tree) why(ref *refusal) string {
	if t.shared || t.public {
		return "Refused: " + ref.link
	}
	return ref.Error()
}

// coin is a status code that turns like a coin above a 404's table, or a
// stop's 410, with its reason on the line below, since the digits alone
// do not say what happened.
func coin(status int) string {
	code := strconv.Itoa(status)
	return `<p class="coin" aria-hidden="true"><span><span>` + code + `</span><span>` + code + `</span></span></p>` + "\n" +
		`<p class="reason">` + http.StatusText(status) + `</p>` + "\n"
}

// fullURL is the URL r asked for, without its query, so "not found"
// names it whole when copied away from the address bar.
func fullURL(r *http.Request) string {
	return (&url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path}).String()
}

// notFound is the page for a path the tree lacks: 404 turns like a coin
// above a table of labelled rows, the URL first, then where git says the
// path went and the names close to it. Other failures say why in the
// plain line alone.
func (t *tree) notFound(w http.ResponseWriter, r *http.Request, name string) {
	t.render(w, r, http.StatusNotFound, page{
		Title:  t.title(name),
		Crumbs: t.crumbs(name),
		Kind:   "error",
		Body:   template.HTML(coin(http.StatusNotFound) + `<table class="gone">` + "\n" + notFoundRow(r) + t.goneRows(r.Context(), name) + "</table>\n"),
	})
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
