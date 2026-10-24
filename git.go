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
//
// In web's cache git may reach a forge, since a repo there is a partial
// clone whose pages fetch what they show: by webScheme alone, without
// credentials, a prompt, hooks or the user's and the system's git config.
// GIT_NO_LAZY_FETCH needs git 2.44; before it protocol.allow=never alone
// keeps other repos from fetching.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return gitProgress(ctx, dir, nil, args...)
}

// gitProgress is git, and hands each line of git's progress to progress,
// when set. git rewrites a progress line in place with \r, so each
// rewrite is a line.
func gitProgress(ctx context.Context, dir string, progress func(string), args ...string) ([]byte, error) {
	remote := inCache(dir)
	pre := []string{"-c", "protocol.allow=never"}
	if remote {
		pre = append(pre, "-c", "protocol."+webScheme+".allow=always", "-c", "credential.helper=", "-c", "core.askPass=",
			"-c", "core.hooksPath="+os.DevNull)
	}
	args = append(append(pre, "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--no-pager", "--literal-pathspecs"), args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") && !strings.HasPrefix(kv, "SSH_ASKPASS=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	if remote {
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	} else {
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if progress != nil {
		cmd.Stderr = &progressWriter{buf: &stderr, line: progress}
	}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out.Bytes(), errors.New(msg)
		}
		return out.Bytes(), fmt.Errorf("git: %w", err)
	}
	return out.Bytes(), nil
}

// progressWriter splits git's stderr at \r and \n and hands each line to
// line. buf keeps the lines that are not progress, so an error reads as
// git's complaint alone.
type progressWriter struct {
	buf  *bytes.Buffer
	line func(string)
	part []byte
}

var progressLine = regexp.MustCompile(`^(remote: )?[A-Z][a-z]+( [a-z]+)*: +[0-9]+%`)

func (p *progressWriter) Write(b []byte) (int, error) {
	for _, c := range b {
		if c != '\r' && c != '\n' {
			p.part = append(p.part, c)
			continue
		}
		if s := strings.TrimSpace(string(p.part)); s != "" {
			p.line(s)
			if !progressLine.MatchString(s) {
				p.buf.WriteString(s + "\n")
			}
		}
		p.part = p.part[:0]
	}
	return len(b), nil
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
// opened next, and the stardates of its repo, which a large repo takes
// a second to number. Later visits find the answers cached.
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
	stardatesIn(ctx, t.dir)
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
	if t.at != nil {
		return nil
	}
	// Two commits, as the page's top line asks for, share one cache entry.
	log, err := t.gitLog(ctx, name, 2)
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

// commitLine is a commit's stardate, as a history and a commit page show
// it, with its hash and exact time on hover.
func commitLine(s *stardates, c *commit) string {
	return fmt.Sprintf("<span class=\"date\" title=\"%s %s\">%s</span>", template.HTMLEscapeString(c.short), exact(c.date), template.HTMLEscapeString(s.label(c)))
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
// when no commit touched the page.
func (t *tree) logLine(ctx context.Context, name string) string {
	if t.at != nil {
		return t.versionLine(ctx, name, false)
	}
	c := t.lastCommit(ctx, name)
	if c == nil {
		return ""
	}
	return topLine(c, t.starsFor(ctx, name).label(c), "", "")
}

// topLine is the line at the top of a page in a repo. It names the
// commit shown, by subject and star, its stardate or hash, with hash and
// exact time on hover,
// and the whole line is one target: it opens a menu of the page's
// versions, which loads from base?timeline. here, when not "", is the
// URL of the version shown, which the menu marks. Without the script the
// menu holds a link to the history.
func topLine(c *commit, star, base, here string) string {
	esc := template.HTMLEscapeString
	line := fmt.Sprintf("<span class=\"subject\" title=\"%s %s\">%s</span> · <span class=\"date\" title=\"%s %s\">%s</span>",
		esc(c.short), exact(c.date), esc(subjectOf(c)), esc(c.short), exact(c.date), esc(star))
	if here != "" {
		here = " data-here=\"" + esc(here) + "\""
	}
	return fmt.Sprintf("<details class=\"own latest\" data-timeline=\"%s?timeline\"%s>\n<summary><span class=\"menu\">timeline</span> · %s</summary>\n"+
		"<div class=\"versions\"><a href=\"%s?log\">%s</a></div>\n</details>\n",
		esc(base), here, line, esc(base), esc(logLabel))
}

// changed reports whether name, a file or everything below a directory,
// differs from its last commit: edited, staged, added or untracked.
func (t *tree) changed(ctx context.Context, name string) bool {
	dir, spec, err := t.gitPlace(name)
	if err != nil {
		return false
	}
	out, err := git(ctx, dir, "status", "--porcelain", "-z", "--", spec)
	return err == nil && len(out) > 0
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
		labels[i] = stars.label(&log[i])
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
		labels[i] = stars.label(&log[i])
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
			show, commitLine(stars, c), esc(c.subject), show, esc(c.short), then)
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
	fmt.Fprintf(&body, "<div class=\"commit\">\n<p class=\"own\">%s</p>\n<p class=\"subject\">%s</p>\n", commitLine(t.starsFor(r.Context(), name), c), esc(c.subject))
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
