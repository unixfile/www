package main

import (
	"fmt"
	"html/template"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// serveIndex lists every server on the user's addresses in the order of
// their addresses, so the page reads the same whichever server answers.
// Each server leads with its address, binary, build and start, then its
// trees. Only this server's trees carry a Delete lock button, since a stop
// must come from the server's own origin; another server's address links
// to its own index. Installs that run no server follow, so the package
// and a make install build both show. It answers at / and for paths
// outside every tree.
func (s *server) serveIndex(w http.ResponseWriter, r *http.Request, status int, msg string) {
	// msg is HTML: an errorLine, or a 404's table.
	if s.shared {
		s.serveSharedIndex(w, r, status, msg)
		return
	}
	esc := template.HTMLEscapeString
	var body strings.Builder
	body.WriteString(msg)
	servers := s.servers()
	trees := s.list()
	// One lead width for every table, so the times line up down the page.
	var sinces []string
	ownSince := make([]string, len(trees))
	for i, t := range trees {
		if t.lock != nil && debugOn {
			ownSince[i] = "since " + clock(t.lock.info.ModTime())
		}
	}
	sinces = append(sinces, ownSince...)
	for _, srv := range servers {
		for _, t := range srv.trees {
			if debugOn {
				sinces = append(sinces, "since "+clock(t.since))
			}
		}
	}
	lead := leadStyle(sinces)
	for _, srv := range servers {
		if srv.ep.IsValid() {
			addr := esc(srv.ep.String())
			if !srv.own {
				addr = "<a href=\"" + esc(origin(srv.ep)) + "/\">" + addr + "</a>"
			} else {
				addr += ", this page"
			}
			body.WriteString("<h2 class=\"own\">" + addr + "</h2>\n")
			body.WriteString("<p class=\"own\">" + srv.account() + "</p>\n")
		}
		if srv.stale {
			fmt.Fprintf(&body, "<p class=\"own warn\">The binary changed after this server started. Run <code>%s</code>.</p>\n", esc(srv.restartCmd))
		}
		if !srv.own {
			body.WriteString("<table class=\"locks own\"" + lead + ">\n")
			for _, t := range srv.trees {
				since := ""
				if debugOn {
					since = "since " + clock(t.since)
				}
				fmt.Fprintf(&body, "<tr><td>%s<a href=\"%s\" title=\"%s\">%s</a></td></tr>\n",
					sinceLead(since), esc(t.url), esc(t.lockPath), segments(tildePath(t.dir)))
			}
			body.WriteString("</table>\n")
			continue
		}
		if len(trees) == 0 {
			body.WriteString("<p class=\"own\">No tree is served.</p>\n")
			continue
		}
		body.WriteString("<table class=\"locks own\"" + lead + ">\n")
		for i, t := range trees {
			lockPath := ""
			if t.lock != nil {
				lockPath = t.lock.path
			}
			fmt.Fprintf(&body, "<tr><td>%s<a href=\"%s\" title=\"%s\">%s</a></td>"+
				"<td><form method=\"post\" action=\""+ownPath+"stop\"><input type=\"hidden\" name=\"tree\" value=\"%s\">"+
				"<input type=\"hidden\" name=\"from\" value=\"index\"><button>Delete lock</button></form></td></tr>\n",
				sinceLead(ownSince[i]), esc(t.prefixURL()), esc(lockPath), segments(t.label), esc(t.prefix))
		}
		body.WriteString("</table>\n")
	}
	if idle := s.idleInstalls(servers); len(idle) > 0 {
		body.WriteString("<h2 class=\"own\">No server</h2>\n")
		for _, b := range idle {
			body.WriteString("<p class=\"own\">" + b.account() + "</p>\n")
		}
	}
	if s.webURL != "" {
		fmt.Fprintf(&body, "<p class=\"own\">Online repos: <a href=\"%s/\">%s/</a></p>\n", esc(s.webURL), esc(strings.TrimPrefix(s.webURL, "http://")))
	}
	render(w, r, status, page{Title: progName, Crumbs: []crumb{{Label: "index"}}, Kind: "index",
		Body: template.HTML(body.String())})
}

// sinceLead is a tree's lock time leading its row on the index, or ""
// when the index shows none.
func sinceLead(since string) string {
	if since == "" {
		return ""
	}
	return "<span class=\"since lead\">" + template.HTMLEscapeString(since) + "</span> "
}

// indexServer is a server, or an install with none, as the index shows
// it.
type indexServer struct {
	ep         netip.AddrPort // invalid for an install with no server
	own        bool           // the server that renders the page
	binary     string         // "" when its registry has no record
	since      time.Time      // when the server started
	built      time.Time      // when its binary was written
	commit     string
	stale      bool // the binary on disk is newer than the server
	restartCmd string
	trees      []servedTree // another server's trees
}

// account is the line under a server's address: its binary, and with
// --debug the build it runs and since when. A server whose binary changed
// on disk names no build, since the file on disk is not the code it runs.
func (b indexServer) account() string {
	esc := template.HTMLEscapeString
	var parts []string
	if b.binary == "" {
		parts = append(parts, "binary not recorded")
	} else {
		parts = append(parts, "<code>"+segments(tildePath(b.binary))+"</code>")
	}
	if debugOn && !b.stale && !b.built.IsZero() {
		parts = append(parts, "built "+esc(clock(b.built))+" from "+esc(b.commit))
	}
	if debugOn && !b.since.IsZero() {
		parts = append(parts, "up since "+esc(clock(b.since)))
	}
	return strings.Join(parts, ", ")
}

// servers lists this server and every other live one on the user's
// addresses, in the order of their addresses.
func (s *server) servers() []indexServer {
	own := indexServer{ep: s.about.ep, own: true, binary: s.about.exe, since: s.about.started,
		built: built, commit: buildCommit, stale: s.about.stale(), restartCmd: s.about.restartCmd()}
	out := []indexServer{own}
	if !s.about.ep.IsValid() {
		return out
	}
	eps, _ := endpoints()
	for _, ep := range eps {
		if ep == s.about.ep {
			continue
		}
		trees := peekTrees(ep)
		if len(trees) == 0 {
			continue
		}
		slices.SortFunc(trees, func(a, b servedTree) int { return strings.Compare(a.url, b.url) })
		srv := indexServer{ep: ep, binary: binaryOf(ep), trees: trees}
		if l, ok := serverOf(ep); ok {
			srv.since = l.since
		}
		if srv.binary != "" {
			srv.built, srv.commit = binInfo(srv.binary)
			srv.stale = !srv.since.IsZero() && srv.built.After(srv.since)
			srv.restartCmd = tildePath(srv.binary) + " --restart"
		}
		out = append(out, srv)
	}
	slices.SortFunc(out, func(a, b indexServer) int { return a.ep.Compare(b.ep) })
	return out
}

// idleInstalls are the installs that run no server: the package's binary
// and the one make install links, so the index shows both whether or not
// they serve.
func (s *server) idleInstalls(servers []indexServer) []indexServer {
	if !s.about.ep.IsValid() {
		return nil
	}
	paths := []string{"/usr/bin/" + progName}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".local", "bin", progName))
	}
	var out []indexServer
	for _, p := range paths {
		p, err := filepath.EvalSymlinks(p)
		if err != nil || slices.ContainsFunc(servers, func(b indexServer) bool { return b.binary == p }) ||
			slices.ContainsFunc(out, func(b indexServer) bool { return b.binary == p }) {
			continue
		}
		b := indexServer{binary: p}
		b.built, b.commit = binInfo(p)
		out = append(out, b)
	}
	return out
}

// binInfo reads when a binary on disk was written and the commit it came
// from.
func binInfo(path string) (time.Time, string) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, ""
	}
	return info.ModTime(), fileRev(path)
}

// serveSharedIndex lists the trees shared without a secret, by name alone:
// no lock paths, buttons or other addresses.
func (s *server) serveSharedIndex(w http.ResponseWriter, r *http.Request, status int, msg string) {
	esc := template.HTMLEscapeString
	var body strings.Builder
	body.WriteString(msg)
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
