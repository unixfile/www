package www

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

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
// path is its guard against rebinding. So does a public server, which
// holds nothing a stranger may not read.
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
