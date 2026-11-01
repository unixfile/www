package main

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// depths gives, for forges whose repos sit at one depth, the number of
// segments in a repo's key, host included.
var depths = map[string]int{"github.com": 3, "bitbucket.org": 3, "codeberg.org": 3}

// minDepths gives the fewest segments of a repo's key on forges with
// groups, host included.
var minDepths = map[string]int{"gitlab.com": 3}

// forgeViews gives, for each forge a doc's links are read from, the path
// segments after a repo's key that lead into a file or directory at a
// ref, eg blob and tree on GitHub, and raw for a file's bare bytes. The
// ref follows them, then the path.
var forgeViews = map[string][][]string{
	"github.com":    {{"blob"}, {"tree"}, {"raw"}},
	"codeberg.org":  {{"src", "branch"}, {"src", "tag"}, {"src", "commit"}, {"raw", "branch"}, {"raw", "tag"}, {"raw", "commit"}},
	"bitbucket.org": {{"src"}, {"raw"}},
	"gitlab.com":    {{"-", "blob"}, {"-", "tree"}, {"-", "raw"}},
}

// rawHosts serve a forge's files bare, at owner/name, then the ref, then
// the path, eg raw.githubusercontent.com/junegunn/fzf/master/README.md.
// Each maps to its forge.
var rawHosts = map[string]string{"raw.githubusercontent.com": "github.com"}

// pinnedRef matches a ref that is a commit's hash, which the web address
// serves as a version.
var pinnedRef = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// forgePages are GitHub's first segments that name a page of the site,
// not an account, eg github.com/sponsors/junegunn.
var forgePages = map[string]bool{
	"about": true, "apps": true, "collections": true, "customer-stories": true, "enterprise": true,
	"features": true, "login": true, "marketplace": true, "orgs": true, "settings": true,
	"site": true, "sponsors": true, "topics": true, "trending": true, "users": true,
}

// webPath is the path on the web address that reads what the forge URL u
// shows, eg /github.com/junegunn/fzf/README.md for
// https://github.com/junegunn/fzf/blob/master/README.md, and the repo's
// key, or "" when u is not a repo or a file in one on a forge in
// forgeViews or rawHosts. A ref that is a commit's hash leads to that
// version, eg /github.com/junegunn/fzf@26ff936/README.md; any other ref
// drops, since the web address serves the commit it fetched.
func webPath(u *url.URL) (key, p string) {
	if u.Scheme != "https" && u.Scheme != "http" || u.User != nil || u.Port() != "" {
		return "", ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	views, ok := forgeViews[host]
	raw := rawHosts[host]
	if raw != "" {
		host, views, ok = raw, [][]string{{}}, true
	}
	if !ok {
		return "", ""
	}
	segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	if slices.ContainsFunc(segs, func(s string) bool { return s == "." || s == ".." }) {
		return "", ""
	}
	// A repo's key ends where a view begins, or after owner/name.
	n := len(segs)
	if host == "gitlab.com" {
		if i := slices.Index(segs, "-"); i >= 0 {
			n = i
		}
	} else {
		n = min(n, 2)
	}
	if n < 2 || host == "github.com" && forgePages[segs[0]] {
		return "", ""
	}
	k := append([]string{host}, segs[:n]...)
	k[n] = strings.TrimSuffix(k[n], ".git")
	key = strings.Join(k, "/")
	p = "/" + key + "/"
	rest := segs[n:]
	if len(rest) == 0 && raw == "" {
		return key, p
	}
	for _, v := range views {
		if len(rest) <= len(v) || !slices.Equal(rest[:len(v)], v) {
			continue
		}
		ref, after := rest[len(v)], rest[len(v)+1:]
		// raw.githubusercontent.com also names a ref in full, eg
		// refs/heads/main.
		if raw != "" && ref == "refs" && len(after) > 2 {
			after = after[2:]
		}
		file := strings.Join(after, "/")
		if raw != "" && file == "" {
			break
		}
		if file != "" && len(v) > 0 && v[len(v)-1] == "tree" {
			file += "/"
		}
		if pinnedRef.MatchString(ref) {
			p = "/" + key + "@" + ref + "/"
		}
		return key, p + file
	}
	return "", ""
}

// forgeStyle is how a forge links a file, a directory and a commit, each
// followed by a commit's hash, and how it marks lines in a fragment:
// github as #L10-L12, gitlab as #L10-12, bitbucket as #lines-10:12.
type forgeStyle struct{ file, dir, commit, lines string }

var forgeStyles = map[string]forgeStyle{
	"github.com":    {"/blob/", "/tree/", "/commit/", "github"},
	"codeberg.org":  {"/src/commit/", "/src/commit/", "/commit/", "github"},
	"gitlab.com":    {"/-/blob/", "/-/tree/", "/-/commit/", "gitlab"},
	"bitbucket.org": {"/src/", "/src/", "/commits/", "bitbucket"},
}

// forgeLink is the forge's own permanent URL for the page of kind that r
// asks of t, a repo on the web address or a version of one, and how the
// forge marks lines; "" where the forge or the page has none. A file or
// directory is linked at the commit the page shows, so the link keeps
// naming the same text after the forge moves on.
func (t *tree) forgeLink(r *http.Request, kind string) (link, lines string) {
	repo := t.web
	if t.base != nil {
		repo = t.base.web
	}
	if repo == nil {
		return "", ""
	}
	host, _, _ := strings.Cut(repo.key, "/")
	st, ok := forgeStyles[host]
	if !ok {
		return "", ""
	}
	base := "https://" + repo.key
	var commit, prefix string
	switch kind {
	case "commit":
		if h := r.URL.Query().Get("commit"); hexHash.MatchString(h) {
			return base + st.commit + h, st.lines
		}
		return "", ""
	case "doc", "code", "tree":
		prefix = st.file
	case "dir":
		prefix = st.dir
	default:
		return "", ""
	}
	if t.at != nil {
		commit = t.at.hash
	} else if commit = repoAt(r.Context(), t.dir).head(r.Context()); !isHash(commit) {
		return "", ""
	}
	link = base + prefix + commit
	if name := t.name(r.URL.Path); name != "." {
		link += (&url.URL{Path: "/" + name}).EscapedPath()
	}
	return link, st.lines
}
