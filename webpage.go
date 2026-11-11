package www

import (
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

// crumbs link the root and each segment of a path that no tree serves.
func webCrumbs(key string) []crumb {
	out := []crumb{indexCrumb}
	segs := strings.Split(key, "/")
	for i, s := range segs {
		c := crumb{Label: s}
		if i < len(segs)-1 {
			c.Href = (&url.URL{Path: rooted("/" + strings.Join(segs[:i+1], "/") + "/")}).String()
		}
		out = append(out, c)
	}
	return out
}

func (w *web) render(rw http.ResponseWriter, r *http.Request, status int, title string, crumbs []crumb, kind string, body string) {
	render(rw, r, status, page{Title: title, Crumbs: crumbs, Kind: kind, Body: template.HTML(body), NoStop: true})
}

func (w *web) fail(rw http.ResponseWriter, r *http.Request, status int, msg string) {
	w.render(rw, r, status, "not found", []crumb{indexCrumb, {Label: "not found"}}, "error",
		"<pre class=\"error\">"+template.HTMLEscapeString(msg)+"</pre>")
}

// serveRoot lists the cache: each repo, its size and its last request. A
// public server says only how to read a repo.
func (w *web) serveRoot(rw http.ResponseWriter, r *http.Request) {
	esc := template.HTMLEscapeString
	var total int64
	var body strings.Builder
	fmt.Fprintf(&body, "<p class=\"own\">Type a forge's host and a path after this address, eg <a href=\"%[1]s/github.com/junegunn/fzf/\">%[1]s/github.com/junegunn/fzf</a>, to read that repo, or <a href=\"%[1]s/git.suckless.org/\">%[1]s/git.suckless.org</a> to list a host's repos.</p>\n", basePath)
	if w.public {
		w.render(rw, r, http.StatusOK, "web", []crumb{{Label: "index"}}, "index", body.String())
		return
	}
	repos := w.onDisk()
	slices.SortFunc(repos, func(a, b cachedRepo) int { return strings.Compare(a.key, b.key) })
	if len(repos) > 0 {
		var sizes []string
		for _, c := range repos {
			sizes = append(sizes, sizeText(c.size))
		}
		// The size leads, aligned right as numbers are; when it was
		// read follows the name.
		body.WriteString("<table class=\"locks own\"" + leadStyle(sizes) + ">\n")
		for i, c := range repos {
			total += c.size
			fmt.Fprintf(&body, "<tr><td><span class=\"since lead num\">%s</span> <a href=\"%s\">%s</a> <span class=\"since\">last read %s</span></td></tr>\n",
				sizes[i], esc((&url.URL{Path: rooted("/" + c.key + "/")}).String()), segments(c.key), clock(c.used))
		}
		body.WriteString("</table>\n")
	}
	fmt.Fprintf(&body, "<p class=\"own\">The cache in %s holds %d repos in %s of its %s.</p>\n", esc(tildePath(w.root)), len(repos), sizeText(total), sizeText(w.limit))
	if u := w.srv.homeURL; u != "" {
		fmt.Fprintf(&body, "<p class=\"own\">Trees: <a href=\"%s/\">%s/</a></p>\n", esc(u), esc(strings.TrimPrefix(u, "http://")))
	}
	w.render(rw, r, http.StatusOK, "web", []crumb{{Label: "index"}}, "index", body.String())
}

// serveJob shows what a visit set going: the reading page.
func (w *web) serveJob(rw http.ResponseWriter, r *http.Request, j *webJob) {
	esc := template.HTMLEscapeString
	w.mu.Lock()
	steps, line, failed, started := slices.Clone(j.steps), j.line, j.err, j.started
	w.mu.Unlock()
	var body strings.Builder
	status := http.StatusAccepted
	if failed != "" {
		status = http.StatusBadGateway
		body.WriteString("<pre class=\"error\">" + esc(failed) + "</pre>\n")
	} else {
		body.WriteString("<p class=\"own reading\">reading</p>\n")
	}
	body.WriteString("<pre class=\"own steps\">")
	for _, s := range steps {
		body.WriteString(esc(s) + "\n")
	}
	if line != "" {
		body.WriteString(esc(line) + "\n")
	}
	body.WriteString("</pre>\n")
	fmt.Fprintf(&body, "<p class=\"own\">Started %s, %d s ago.", clock(started), int(time.Since(started).Seconds()))
	if failed != "" {
		body.WriteString(" Reload after 30 s to try again.")
	} else {
		body.WriteString(" The page turns into what was asked for when it is read.")
	}
	body.WriteString("</p>\n")
	key := strings.TrimPrefix(j.urlPath, "/")
	w.render(rw, r, status, "reading "+key, webCrumbs(key), "reading", body.String())
}

// serveConfirm asks before a download that another site set going.
func (w *web) serveConfirm(rw http.ResponseWriter, r *http.Request, key string) {
	esc := template.HTMLEscapeString
	u := (&url.URL{Path: rooted("/" + key)}).String()
	body := fmt.Sprintf("<p class=\"own\">Another site sent you here, so nothing is fetched yet. <a href=\"%s\">Read %s</a> fetches it from %s into the cache.</p>\n",
		esc(u), esc(key), esc(webScheme+"://"+key))
	// The page runs the script, which carries the link's fragment, eg
	// #L10-L12, onto the read link, but holds no stream: a reload must
	// not fetch what the user has not asked for.
	render(rw, r, http.StatusOK, page{Title: key, Crumbs: webCrumbs(key), Kind: "confirm", Body: template.HTML(body), NoStop: true, Live: "-", Fixed: true})
}

// serveListing lists a host's or account's repos, and names its source.
func (w *web) serveListing(rw http.ResponseWriter, r *http.Request, l *listing) {
	esc := template.HTMLEscapeString
	p, _ := unrooted(path.Clean(r.URL.Path))
	key := strings.Trim(p, "/")
	var body strings.Builder
	status := http.StatusOK
	if len(l.repos) == 0 {
		status = http.StatusNotFound
		fmt.Fprintf(&body, "<pre class=\"error\">No repo answers at %s, and nothing lists one there.</pre>\n", esc(webScheme+"://"+key))
	} else {
		body.WriteString("<ul class=\"listing\">\n")
		for _, e := range l.repos {
			name := strings.TrimPrefix(e.key, key+"/")
			fmt.Fprintf(&body, "<li><a href=\"%s\">%s/</a>", esc((&url.URL{Path: rooted("/" + e.key + "/")}).String()), esc(name))
			if e.desc != "" {
				fmt.Fprintf(&body, " <span class=\"own desc\">%s</span>", esc(e.desc))
			}
			body.WriteString("</li>\n")
		}
		body.WriteString("</ul>\n")
	}
	failed := "Failed first: "
	if l.source == "" {
		// A public server keeps the cache to itself, so nothing may have
		// answered: the page says when it asked, and names no source.
		failed = "Failed: "
		fmt.Fprintf(&body, "<p class=\"own\">Asked at %s.</p>\n", clock(l.at))
	} else {
		src := l.source
		if l.asked != "" {
			src += ", " + l.asked
		}
		fmt.Fprintf(&body, "<p class=\"own\">%d repos, listed by %s at %s.", len(l.repos), esc(src), clock(l.at))
		if l.source == "the cache" {
			body.WriteString(" The cache holds only what was read here, so the host may hold more.")
		}
		body.WriteString("</p>\n")
	}
	if len(l.failed) > 0 {
		body.WriteString("<p class=\"own\">" + failed + esc(strings.Join(l.failed, "; ")) + ".</p>\n")
	}
	w.render(rw, r, status, key, webCrumbs(key), "index", body.String())
}
