package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// logLabel names the history in a listing, at a page's top and in the
// crumbs: the command that makes it.
const logLabel = "<(git log)"

// maxLog caps the commits a history page lists.
const maxLog = 500

// gitWait bounds a git run that no request waits on.
const gitWait = 5 * time.Second

// slowGit is how long git may take for a history before its page names
// the command that speeds it up.
var slowGit = 100 * time.Millisecond

// errNoHistory is what a share says in place of git's errors, which name
// paths on the machine.
var errNoHistory = errors.New("No git history here.")

// commit is one entry of a history.
type commit struct {
	hash, short string
	date        time.Time
	subject     string
	body        string
}

// git runs git in dir. A repo's .git/config can name programs for git to
// run, so the options that would start one are off, fsmonitor among them
// whichever command runs, and git may not fetch a partial clone's missing
// objects. Pathspecs are literal, and no GIT_
// variable from www's own environment leaks in. git's safe.directory
// check refuses repos that other users own.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	args = append([]string{"-c", "protocol.allow=never", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--no-pager", "--literal-pathspecs"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out.Bytes(), errors.New(msg)
		}
		return out.Bytes(), fmt.Errorf("git: %w", err)
	}
	return out.Bytes(), nil
}

// gitPlace is where git runs for the page at name, and the path it logs.
// Links resolve first, so the history is that of what the page shows.
func (t *tree) gitPlace(name string) (dir, spec string, err error) {
	real, err := filepath.EvalSymlinks(filepath.Join(t.dir, filepath.FromSlash(name)))
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		return real, ".", nil
	}
	return filepath.Dir(real), filepath.Base(real), nil
}

// gitLog lists up to n commits that touched name, newest first. A
// directory's history covers everything below it. A share shows it only
// when other users could read the repo, and drops trailers.
func (t *tree) gitLog(ctx context.Context, name string, n int) ([]commit, error) {
	dir, spec, err := t.gitPlace(name)
	if err != nil {
		return nil, t.gitErr(err)
	}
	if t.shared && !openRepo(ctx, dir) {
		return nil, errNoHistory
	}
	// A history is fixed by the commit HEAD names, so it is kept under
	// that commit and a new one misses the cache.
	var key string
	if r := repoAt(ctx, dir); r.complaint == "" {
		key = fmt.Sprintf("log\x00%s\x00%s\x00%s\x00%s\x00%d\x00%t", r.gitDir, r.head(ctx), dir, spec, n, t.shared)
		if v, ok := gitCache.get(key); ok {
			return v.([]commit), nil
		}
	}
	out, err := git(ctx, dir, "log", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--no-color",
		"-z", "--format="+commitFormat, "-n", fmt.Sprint(n), "--", spec)
	if err != nil {
		return nil, t.gitErr(err)
	}
	log := t.commits(out)
	if key != "" {
		size := int64(0)
		for _, c := range log {
			size += int64(len(c.hash)+len(c.short)+len(c.subject)+len(c.body)) + 64
		}
		gitCache.put(key, log, size)
	}
	return log, nil
}

// commitFormat is how git prints a commit for commits to read.
const commitFormat = "%H%n%h%n%aI%n%s%n%b"

// commits reads git's records in commitFormat, each ended by a NUL.
func (t *tree) commits(out []byte) []commit {
	var log []commit
	for _, rec := range strings.Split(string(out), "\x00") {
		f := strings.SplitN(rec, "\n", 5)
		if len(f) < 4 {
			continue
		}
		c := commit{hash: f[0], short: f[1], subject: f[3]}
		c.date, _ = time.Parse(time.RFC3339, f[2])
		if len(f) == 5 {
			c.body = strings.TrimSpace(f[4])
		}
		if t.shared {
			c.body = dropTrailers(c.body)
		}
		log = append(log, c)
	}
	return log
}

// lacksPathFilters reports whether the repo name lies in has no
// commit-graph with changed-path filters, the index git reads to skip
// commits that left a path alone. A split graph counts by its newest
// layer, which holds the commits a history reads first.
func (t *tree) lacksPathFilters(ctx context.Context, name string) bool {
	dir, _, err := t.gitPlace(name)
	if err != nil {
		return false
	}
	r := repoAt(ctx, dir)
	if r.complaint != "" {
		return false
	}
	info := filepath.Join(r.common, "objects", "info")
	graph := filepath.Join(info, "commit-graph")
	if chain, err := os.ReadFile(filepath.Join(info, "commit-graphs", "commit-graph-chain")); err == nil {
		lines := strings.Fields(string(chain))
		if len(lines) > 0 {
			graph = filepath.Join(info, "commit-graphs", "graph-"+lines[len(lines)-1]+".graph")
		}
	}
	return !hasChunk(graph, "BIDX")
}

// hasChunk reports whether the commit-graph file at p holds the chunk id.
// Its header is the signature CGPH, three bytes and the chunk count; the
// table of contents that follows has a 12-byte row per chunk: the id and
// its offset.
func hasChunk(p, id string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil || string(head[:4]) != "CGPH" {
		return false
	}
	toc := make([]byte, 12*int(head[6]))
	if _, err := io.ReadFull(f, toc); err != nil {
		return false
	}
	for i := 0; i < len(toc); i += 12 {
		if string(toc[i:i+4]) == id {
			return true
		}
	}
	return false
}

// warm asks git, in the background, what a new tree's first pages need:
// the top lines of its listing and of its README, the page most often
// opened next. Later visits find the answers cached.
func (t *tree) warm() {
	ctx, cancel := context.WithTimeout(context.Background(), gitWait)
	defer cancel()
	start := time.Now()
	names := []string{"."}
	if entries, err := fs.ReadDir(t.fsys, "."); err == nil {
		for _, e := range entries {
			if n := strings.ToLower(e.Name()); !e.IsDir() && (n == "readme.md" || n == "readme.markdown") {
				names = append(names, e.Name())
				break
			}
		}
	}
	for _, name := range names {
		t.lastCommit(ctx, name)
	}
	logInfo("warmed git answers for %s in %d ms: %s", t.dir, time.Since(start).Milliseconds(), strings.Join(names, ", "))
}

// gitErr is err, or on a share errNoHistory.
func (t *tree) gitErr(err error) error {
	if t.shared {
		return errNoHistory
	}
	return err
}

// openRepo reports whether other users could read the repo that dir lies
// in: its git directory and HEAD, and the objects and refs it shares with
// its worktrees. A share serves nothing that git run as another user
// could not show.
func openRepo(ctx context.Context, dir string) bool {
	out, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return false
	}
	gitDir, common, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	for _, p := range [][2]string{{gitDir, "HEAD"}, {common, "objects"}, {common, "refs"}} {
		if _, _, ok := othersRead(p[0], filepath.Join(p[0], p[1])); !ok {
			return false
		}
	}
	return true
}

// dropTrailers cuts a body's trailer block, such as Signed-off-by or
// Claude-Session. As git does, it takes the last paragraph for one when a
// quarter of its lines are trailers, so a cherry-pick note left inside
// does not save the rest. A line that starts with blank space continues
// the trailer above it.
func dropTrailers(body string) string {
	paras := strings.Split(body, "\n\n")
	trailers, others := 0, 0
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		switch {
		case trailer.MatchString(line):
			trailers++
		case trailers > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
		default:
			others++
		}
	}
	if trailers == 0 || 3*trailers < others {
		return body
	}
	return strings.TrimSpace(strings.Join(paras[:len(paras)-1], "\n\n"))
}

var trailer = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*[ \t]*:[ \t]*\S`)

// lastCommit is the newest commit that touched name, or nil when none
// did or name lies outside a repo.
func (t *tree) lastCommit(ctx context.Context, name string) *commit {
	log, err := t.gitLog(ctx, name, 1)
	if err != nil || len(log) == 0 {
		return nil
	}
	return &log[0]
}

// gitHead names the repo name lies in and its HEAD, so a page can watch
// the repo and reload when a commit lands. Outside a repo it returns git's
// complaint, which changes only if a repo appears. A share watches only
// a repo whose history it shows, or a reload would tell when a commit
// lands in one it calls private.
func (t *tree) gitHead(name string) (gitDir, state string) {
	dir, _, err := t.gitPlace(name)
	if err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitWait)
	defer cancel()
	if t.shared && !openRepo(ctx, dir) {
		return "", ""
	}
	r := repoAt(ctx, dir)
	if r.complaint != "" {
		return "", r.complaint
	}
	return r.gitDir, r.gitDir + "\n" + r.head(ctx)
}

// commitLine is a commit's hash and date, as a history and a page's top
// show them.
func commitLine(c *commit) string {
	esc := template.HTMLEscapeString
	return fmt.Sprintf("<a class=\"hash\" href=\"?commit=%s\" title=\"%s\">%s</a> <span class=\"date\">%s</span>",
		esc(c.hash), esc(c.hash), esc(c.short), c.date.Local().Format("2006-01-02 15:04"))
}

// logLine is the line at the top of a doc or code page that leads to its
// history and names its last commit, or "" when it has none.
func (t *tree) logLine(ctx context.Context, name string) string {
	c := t.lastCommit(ctx, name)
	if c == nil {
		return ""
	}
	return fmt.Sprintf("<p class=\"own latest\"><a href=\"?log\">%s</a> %s %s</p>\n",
		template.HTMLEscapeString(logLabel), commitLine(c), template.HTMLEscapeString(c.subject))
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
	esc := template.HTMLEscapeString
	var body strings.Builder
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
	// without it, the link in its place opens that page.
	body.WriteString("<ol class=\"log\">\n")
	for i := range log {
		c := &log[i]
		show := esc("?commit=" + c.hash)
		fmt.Fprintf(&body, "<li><details data-diff=\"%s\">\n<summary><span class=\"own\">%s</span> <span class=\"subject\">%s</span></summary>\n",
			show, commitLine(c), esc(c.subject))
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
	name  string
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
		switch {
		case spec != ".":
			f = name
		case name != ".":
			f = path.Join(name, f)
		}
		diffs = append(diffs, fileDiff{name: f, lines: diffLines(b)})
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
	fmt.Fprintf(&body, "<div class=\"commit\">\n<p class=\"own\">%s</p>\n<p class=\"subject\">%s</p>\n", commitLine(c), esc(c.subject))
	for _, para := range paragraphs(c.body) {
		body.WriteString("<p>" + para + "</p>\n")
	}
	body.WriteString("</div>\n<div class=\"diffs\">\n")
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
		fmt.Fprintf(&body, "<p class=\"own file\">%s</p>\n<pre class=\"diff\">", label)
		for _, line := range d.lines {
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
			fmt.Fprintf(&body, "<span class=\"%s\">%s</span>", class, esc(line))
		}
		body.WriteString("</pre>\n")
	}
	if cut {
		fmt.Fprintf(&body, "<p class=\"own\">The diff stops at %d KiB. <code>git show %s</code> shows the rest.</p>\n", maxDiff>>10, esc(c.short))
	}
	body.WriteString("</div>\n")
	t.render(w, r, http.StatusOK, page{Title: title, Crumbs: crumbs, Kind: "commit", Body: template.HTML(body.String())})
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
