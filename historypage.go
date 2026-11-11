package www

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// commitLine is label, a commit's stardate as a history or a commit page
// shows it, with its hash and exact time on hover.
func commitLine(label string, c *commit) string {
	return fmt.Sprintf("<span class=\"date\" title=\"%s %s\">%s</span>", template.HTMLEscapeString(c.short), exact(c.date), template.HTMLEscapeString(label))
}

// starsFor are the stardates of the repo name lies in. A version reads
// its tree's own repo.
func (t *tree) starsFor(ctx context.Context, name string) *stardates {
	if t.at != nil {
		return stardatesIn(ctx, t.dir)
	}
	dir, _, err := t.gitPlace(name)
	if err != nil {
		return nil
	}
	return stardatesIn(ctx, dir)
}

// logLine is the line at the top of a doc or code page in a repo, or ""
// when no commit touched the page. b is the page's file as read.
func (t *tree) logLine(ctx context.Context, name string, b []byte) string {
	if t.at != nil {
		return t.versionLine(ctx, name, false)
	}
	c := t.lastCommit(ctx, name)
	if c == nil {
		return ""
	}
	state := "current"
	if t.pageChanged(ctx, name, b) {
		state = "changed"
	}
	return topLine(c, t.starsFor(ctx, name).label(c), state, "", "", "", "")
}

// topLine is the line at the top of a page in a repo, and the whole line
// is one target: it opens a menu of the page's versions, which loads from
// base?timeline. On the page as it is, the line says only state, current
// or changed, since where the reader is matters more than which commit;
// the commit, by star, its stardate or hash, and subject, is on hover.
// On a version, the line names the commit by star and subject, and now
// and show, links outside the line, lead to the page as it is and to the
// commit's page, which names its full hash. here, when not "", is the URL
// of the version shown, which the menu marks. Without the script the
// menu holds a link to the history.
func topLine(c *commit, star, state, base, here, now, show string) string {
	esc := template.HTMLEscapeString
	what := star + " · " + subjectOf(c)
	// The stardate leads, as it does in histories and the version menu.
	line := fmt.Sprintf("<span class=\"date\" title=\"%s %s\">%s</span> · <span class=\"subject\" title=\"%s %s\">%s</span>",
		esc(c.short), exact(c.date), esc(star), esc(c.short), exact(c.date), esc(subjectOf(c)))
	if state != "" {
		line = fmt.Sprintf("<span class=\"state\" title=\"%s · %s %s\">%s</span>", esc(what), esc(c.short), exact(c.date), state)
	}
	if here != "" {
		here = " data-here=\"" + esc(here) + "\""
	}
	// The links to the page as it is and to the commit float right,
	// before the line, so they stay out of the line's one target.
	out := ""
	if now != "" {
		out = fmt.Sprintf("<a class=\"own now\" href=\"%s\">current</a>\n", esc(now))
	}
	if show != "" {
		out += fmt.Sprintf("<a class=\"own now\" href=\"%s\">commit</a>\n", esc(show))
	}
	// The whole line opens the timeline, so no word labels it; the name
	// shows on hover and is read out first.
	label := "timeline, " + state + ", " + what
	if state == "" {
		label = "timeline, " + what
	}
	return out + fmt.Sprintf("<details class=\"own latest\" data-timeline=\"%s?timeline\"%s>\n<summary title=\"timeline\" aria-label=\"%s\">%s</summary>\n"+
		"<div class=\"versions\"><a href=\"%s?log\">%s</a></div>\n</details>\n",
		esc(base), here, esc(label), line, esc(base), esc(logLabel))
}

// subjectOf is what a link to a commit says: its subject, or its hash
// when the message is empty, so the link stays visible.
func subjectOf(c *commit) string {
	if c.subject == "" {
		return c.short
	}
	return c.subject
}

// exact is a commit's time to the minute, as hover shows it.
func exact(t time.Time) string {
	return t.Local().Format("2006-01-02 15:04")
}

// serveTimeline lists every version of name, newest first, for the menu
// on its top line: each commit that touched it, by subject and stardate, then
// its history. The newest commit's row leads to the current page and says
// so, unless the page has changes not yet committed; then a row of its
// own leads there and says that. A page whose history the tree's
// versions do not hold, eg in a share or a nested repo, links each
// commit's page instead.
func (t *tree) serveTimeline(w http.ResponseWriter, r *http.Request, name string, dir bool) {
	crumbs := append(t.pageCrumbs(name, dir), crumb{Label: "timeline"})
	title := "timeline " + t.title(name)
	log, err := t.gitLog(r.Context(), name, maxLog+1)
	if err != nil {
		t.render(w, r, http.StatusNotFound, page{Title: title, Crumbs: crumbs, Kind: "error",
			Body: template.HTML("<pre class=\"error\">" + template.HTMLEscapeString(err.Error()) + "</pre>")})
		return
	}
	if len(log) > maxLog {
		log = log[:maxLog]
	}
	rest := below(name, dir)
	versions := t.historyInVersions(r.Context(), name)
	stars := t.starsFor(r.Context(), name)
	esc := template.HTMLEscapeString
	var body strings.Builder
	// Each row leads with its stardate, so they line up down the menu.
	labels := make([]string, len(log))
	for i := range log {
		labels[i] = stars.rowLabel(&log[i])
	}
	body.WriteString("<ol class=\"versions\"" + leadStyle(labels) + ">\n")
	current := (&url.URL{Path: t.prefix + rest}).String()
	clean := versions && !t.changed(r.Context(), name)
	if versions && !clean {
		fmt.Fprintf(&body, "<li><span class=\"date lead\"></span> <a href=\"%s\">current</a> <span class=\"date\">uncommitted changes</span></li>\n", esc(current))
	}
	for i := range log {
		c := &log[i]
		href, also, note := "?commit="+c.hash, "", ""
		if versions {
			href = t.versionURL(stars, c, rest)
		}
		// The current page is the newest commit's version, so its row
		// leads there and is marked on either page.
		if clean && i == 0 {
			href, also, note = current, fmt.Sprintf(" data-also=\"%s\"", esc(href)), " · current"
		}
		if note != "" {
			note = " <span class=\"date\">current</span>"
		}
		fmt.Fprintf(&body, "<li%s><span class=\"date lead\">%s</span> <a href=\"%s\" title=\"%s %s\">%s</a>%s</li>\n",
			also, esc(labels[i]), esc(href), esc(c.short), exact(c.date), esc(subjectOf(c)), note)
	}
	fmt.Fprintf(&body, "<li class=\"log\"><a href=\"?log\">%s</a></li>\n</ol>\n", esc(logLabel))
	t.render(w, r, http.StatusOK, page{Title: title, Crumbs: crumbs, Kind: "timeline", Body: template.HTML(body.String())})
}

// serveLog lists the commits that touched name, newest first, each with
// its hash, date, subject and body.
func (t *tree) serveLog(w http.ResponseWriter, r *http.Request, name string, dir bool) {
	crumbs := append(t.pageCrumbs(name, dir), crumb{Label: logLabel})
	title := "git log " + t.title(name)
	start := time.Now()
	log, err := t.gitLog(r.Context(), name, maxLog+1)
	took := time.Since(start)
	if err != nil {
		t.render(w, r, http.StatusNotFound, page{Title: title, Crumbs: crumbs, Kind: "error",
			Body: template.HTML("<pre class=\"error\">" + template.HTMLEscapeString(err.Error()) + "</pre>")})
		return
	}
	stars := t.starsFor(r.Context(), name)
	esc := template.HTMLEscapeString
	var body strings.Builder
	// A directory's history keeps the listing's commands row, with the
	// history as the view on screen, so the three views stay siblings
	// and either listing is one click away.
	if dir {
		body.WriteString("<ul class=\"listing\">\n<li class=\"cmds\"><a href=\"./\">" + esc(namesLabel) + "</a> <a href=\"?details\">" +
			esc(detailsLabel) + "</a> <span class=\"history\">" + esc(logLabel) + "</span></li>\n</ul>\n")
	}
	if len(log) == 0 {
		shown := t.title(name)
		if dir && name != "." {
			shown += "/"
		}
		body.WriteString("<p class=\"own\">No commit touches " + esc(shown) + ".</p>\n")
	}
	if len(log) > maxLog {
		log = log[:maxLog]
		fmt.Fprintf(&body, "<p class=\"own\">The %d newest commits. <code>git log</code> lists the rest.</p>\n", maxLog)
	}
	// git walks every commit back to the ones that touched name, unless
	// the repo's commit-graph tells it which paths each commit changed.
	// www leaves the repo alone and names the command instead. A share
	// says nothing, since its viewer cannot run it.
	if took > slowGit && !t.shared && t.lacksPathFilters(r.Context(), name) {
		fmt.Fprintf(&body, "<p class=\"own\">git took %d ms for this history. "+
			"<code>git commit-graph write --reachable --changed-paths</code> in the repo makes it several times faster.</p>\n",
			took.Milliseconds())
	}
	// Each commit folds out to its message and its diff. The diff comes
	// from the commit page when the fold opens, through the one script;
	// without it, the link in its place opens that page. A row is one
	// target, the fold, so the links to the commit and its version wait
	// inside it. A local tree links each commit's version of the page:
	// the file or directory as it was then.
	browse := t.historyInVersions(r.Context(), name)
	// Each row leads with its stardate, so they line up down the history.
	labels := make([]string, len(log))
	for i := range log {
		labels[i] = stars.rowLabel(&log[i])
	}
	body.WriteString("<ol class=\"log\"" + leadStyle(labels) + ">\n")
	for i := range log {
		c := &log[i]
		show := esc("?commit=" + c.hash)
		then := ""
		if browse {
			then = fmt.Sprintf(" · <a class=\"then\" href=\"%s\">browse</a>", esc(t.versionURL(stars, c, below(name, dir))))
		}
		fmt.Fprintf(&body, "<li><details data-diff=\"%s\">\n<summary><span class=\"own lead\">%s</span> <span class=\"subject\">%s</span></summary>\n"+
			"<p class=\"own links\">commit <a href=\"%s\">%s</a>%s</p>\n",
			show, commitLine(labels[i], c), esc(c.subject), show, esc(c.hash), then)
		for _, para := range paragraphs(c.body) {
			body.WriteString("<p>" + para + "</p>\n")
		}
		fmt.Fprintf(&body, "<div class=\"diffs\"><p class=\"own\"><a href=\"%s\">diff</a></p></div>\n</details></li>\n", show)
	}
	body.WriteString("</ol>\n")
	t.render(w, r, http.StatusOK, page{Title: title, Crumbs: crumbs, Kind: "log", Body: template.HTML(body.String())})
}

// pageCrumbs are the crumbs of the page at name, the last a link back to
// it, for a page that hangs below it.
func (t *tree) pageCrumbs(name string, dir bool) []crumb {
	crumbs := t.crumbs(name)
	href := name
	switch {
	case name == ".":
		href = ""
	case dir:
		href += "/"
	}
	crumbs[len(crumbs)-1].Href = (&url.URL{Path: t.prefix + href}).String()
	return crumbs
}

// maxDiff caps the bytes of diff a commit page shows.
const maxDiff = 1 << 20

var hexHash = regexp.MustCompile(`^[0-9a-f]{4,64}$`)

// fileDiff is one file's part of a commit's diff: its name in the tree
// and the diff's lines below git's header. A file whose type changed has
// two.
type fileDiff struct {
	name  string // relative to the tree
	rel   string // relative to where git runs
	lines []string
}

// gitShow reads the commit that hash names and its diff, limited to name.
// A share leaves out files whose names begin with a dot. cut reports a
// diff cut at maxDiff.
func (t *tree) gitShow(ctx context.Context, name, hash string) (c *commit, diffs []fileDiff, cut bool, err error) {
	dir, spec, err := t.gitPlace(name)
	if err != nil {
		return nil, nil, false, t.gitErr(err)
	}
	if t.shared && !openRepo(ctx, dir) {
		return nil, nil, false, errNoHistory
	}
	// A commit never changes, so its diff is kept under its full hash.
	var key string
	if r := repoAt(ctx, dir); r.complaint == "" && fullHash(hash) {
		key = fmt.Sprintf("show\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t", r.gitDir, dir, spec, name, hash, t.shared)
		if v, ok := gitCache.get(key); ok {
			s := v.(shown)
			return &s.c, s.diffs, s.cut, nil
		}
	}
	// ^{commit} refuses a blob or a tree, which would show a file's text.
	rev := hash + "^{commit}"
	show := func(args ...string) ([]byte, error) {
		args = append([]string{"show", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--no-color",
			"--no-renames", "--diff-merges=first-parent", "--relative", "--src-prefix=a/", "--dst-prefix=b/"}, args...)
		return git(ctx, dir, args...)
	}
	out, err := show("-s", "-z", "--format="+commitFormat, rev, "--")
	if err != nil {
		return nil, nil, false, t.gitErr(err)
	}
	log := t.commits(out)
	if len(log) == 0 {
		return nil, nil, false, t.gitErr(errors.New("no commit " + hash))
	}
	out, err = show("--format=", "--patch", rev, "--", spec)
	if err != nil {
		return nil, nil, false, t.gitErr(err)
	}
	if len(out) > maxDiff {
		out, cut = out[:bytes.LastIndexByte(out[:maxDiff], '\n')+1], true
	}
	for _, b := range strings.Split("\n"+string(out), "\ndiff --git ")[1:] {
		head, _, _ := strings.Cut(b, "\n")
		f, ok := diffName(head)
		if !ok || t.shared && hasDotName(f) {
			continue
		}
		rel := f
		switch {
		case spec != ".":
			f = name
		case name != ".":
			f = path.Join(name, f)
		}
		diffs = append(diffs, fileDiff{name: f, rel: rel, lines: diffLines(b)})
	}
	if key != "" {
		c := log[0]
		size := int64(len(c.hash)+len(c.short)+len(c.subject)+len(c.body)) + 64
		for _, d := range diffs {
			size += int64(len(d.name)) + 16
			for _, l := range d.lines {
				size += int64(len(l)) + 16
			}
		}
		gitCache.put(key, shown{c, diffs, cut}, size)
	}
	return &log[0], diffs, cut, nil
}

// shown is a commit and its diff as gitShow keeps them.
type shown struct {
	c     commit
	diffs []fileDiff
	cut   bool
}

// fullHash reports whether hash is a whole SHA-1 or SHA-256 name in hex,
// which no later commit can make ambiguous.
func fullHash(hash string) bool {
	if len(hash) != 40 && len(hash) != 64 {
		return false
	}
	return !strings.ContainsFunc(hash, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') })
}

// diffName reads the file's name from the header of its diff, a/name
// b/name, which git quotes as C does when the name holds a quote, a
// backslash or a control character. Without renames both names are one.
func diffName(head string) (string, bool) {
	if !strings.HasPrefix(head, `"`) {
		n := (len(head) - len("a/ b/")) / 2
		if n < 1 || !strings.HasPrefix(head, "a/") {
			return "", false
		}
		return head[2 : 2+n], true
	}
	for i := 1; i < len(head); i++ {
		switch head[i] {
		case '\\':
			i++
		case '"':
			f, err := strconv.Unquote(head[:i+1])
			return strings.TrimPrefix(f, "a/"), err == nil && strings.HasPrefix(f, "a/")
		}
	}
	return "", false
}

// diffLines drops the header git puts above a file's diff, which names
// the file again, and keeps the lines that say what kind of change it is.
func diffLines(block string) []string {
	var out []string
	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")[1:]
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			return append(out, lines[i:]...)
		}
		switch {
		case strings.HasPrefix(line, "index "), strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
		case strings.HasPrefix(line, "Binary files "):
			out = append(out, "Binary file changed")
		default:
			out = append(out, line)
		}
	}
	return out
}

// serveCommit shows the commit that hash names: its message and its diff
// limited to name, with a link to the diff across the whole tree. A
// history's fold takes the diff from div.diffs.
func (t *tree) serveCommit(w http.ResponseWriter, r *http.Request, name string, dir bool, hash string) {
	crumbs := t.pageCrumbs(name, dir)
	crumbs = append(crumbs, crumb{Label: logLabel, Href: crumbs[len(crumbs)-1].Href + "?log"}, crumb{Label: hash})
	title := "git show " + hash + " " + t.title(name)
	fail := func(err error) {
		t.render(w, r, http.StatusNotFound, page{Title: title, Crumbs: crumbs, Kind: "error",
			Body: template.HTML("<pre class=\"error\">" + template.HTMLEscapeString(err.Error()) + "</pre>")})
	}
	if !hexHash.MatchString(hash) {
		fail(errors.New("Not a commit hash: " + hash))
		return
	}
	c, diffs, cut, err := t.gitShow(r.Context(), name, hash)
	if err != nil {
		fail(err)
		return
	}
	crumbs[len(crumbs)-1].Label = c.short
	esc := template.HTMLEscapeString
	var body strings.Builder
	// The commit's full hash follows its stardate as plain text, so it
	// can be selected and given to git.
	fmt.Fprintf(&body, "<div class=\"commit\">\n<p class=\"own\">%s · <span class=\"hash\">%s</span></p>\n<p class=\"subject\">%s</p>\n", commitLine(t.starsFor(r.Context(), name).label(c), c), esc(c.hash), esc(c.subject))
	for _, para := range paragraphs(c.body) {
		body.WriteString("<p>" + para + "</p>\n")
	}
	body.WriteString("</div>\n")
	// The semantic diff comes inline when it is kept or asked for with
	// &semantic; otherwise the page asks for it once it shows the plain
	// diff.
	var sem *semantic
	_, asked := r.URL.Query()["semantic"]
	if key := t.semanticKey(name, c.hash); key != "" {
		if v, ok := semanticCache.get(key); ok {
			sem = v.(*semantic)
		}
	}
	if sem == nil && asked {
		sem = t.semanticDiff(r.Context(), name, c.hash, diffs)
	}
	switch {
	case sem != nil:
		body.WriteString(t.semanticHTML(r.URL.Path, c, sem))
	case len(diffs) > 0:
		q := r.URL.Query()
		q.Set("commit", c.hash)
		q.Set("semantic", "")
		fmt.Fprintf(&body, "<div class=\"semantic\" data-src=\"%s\"><p class=\"own\">reading declarations</p></div>\n", esc((&url.URL{Path: r.URL.Path, RawQuery: q.Encode()}).String()))
	}
	body.WriteString("<div class=\"diffs\">\n")
	all := (&url.URL{Path: t.prefix, RawQuery: "commit=" + c.hash}).String()
	shown := name
	if dir {
		shown += "/"
	}
	switch {
	case name != "." && len(diffs) == 0:
		fmt.Fprintf(&body, "<p class=\"own\">It does not change %s. <a href=\"%s\">All files</a></p>\n", esc(shown), esc(all))
	case name != ".":
		fmt.Fprintf(&body, "<p class=\"own\">Changes to %s. <a href=\"%s\">All files</a></p>\n", esc(shown), esc(all))
	case len(diffs) == 0:
		body.WriteString("<p class=\"own\">It changes no file here.</p>\n")
	}
	for _, d := range diffs {
		label := esc(d.name)
		if _, err := fs.Stat(t.fsys, d.name); err == nil {
			label = fmt.Sprintf("<a href=\"%s\">%s</a>", esc((&url.URL{Path: t.prefix + d.name}).String()), label)
		}
		var marks map[int][][2]int
		if sem != nil {
			marks = sem.marks[d.name]
		}
		fmt.Fprintf(&body, "<p class=\"own file\">%s</p>\n%s", label, diffPre(d.lines, marks))
	}
	if cut {
		fmt.Fprintf(&body, "<p class=\"own\">The diff stops at %d KiB. <code>git show %s</code> shows the rest.</p>\n", maxDiff>>10, esc(c.short))
	}
	body.WriteString("</div>\n")
	t.render(w, r, http.StatusOK, page{Title: title, Crumbs: crumbs, Kind: "commit", Body: template.HTML(body.String())})
}

// diffPre renders a diff's lines, each a block tinted by its kind, with
// the changed byte ranges marks gives by line index.
func diffPre(lines []string, marks map[int][][2]int) string {
	var b strings.Builder
	b.WriteString("<pre class=\"diff\">")
	for i, line := range lines {
		class := "meta"
		switch {
		case strings.HasPrefix(line, "@@"):
			class = "hunk"
		case strings.HasPrefix(line, "+"):
			class = "add"
		case strings.HasPrefix(line, "-"):
			class = "del"
		case strings.HasPrefix(line, " "):
			class = "same"
		}
		fmt.Fprintf(&b, "<span class=\"%s\">%s</span>", class, markLine(line, marks[i]))
	}
	b.WriteString("</pre>\n")
	return b.String()
}

// paragraphs splits a commit body at blank lines and joins the lines of
// each, since most bodies are wrapped by hand to 72 columns. A line that
// starts a list item or is indented keeps its own line.
func paragraphs(body string) []string {
	var out []string
	for _, para := range strings.Split(body, "\n\n") {
		var b strings.Builder
		for i, line := range strings.Split(strings.Trim(para, "\n"), "\n") {
			if i > 0 {
				if listLine(line) {
					b.WriteString("<br>")
				} else {
					b.WriteString(" ")
				}
			}
			b.WriteString(template.HTMLEscapeString(strings.TrimRight(line, " \t")))
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return out
}

func listLine(line string) bool {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "+ ") {
		return true
	}
	num := strings.TrimLeft(line, "0123456789")
	return len(num) < len(line) && (strings.HasPrefix(num, ". ") || strings.HasPrefix(num, ") "))
}
