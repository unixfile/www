package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// logLabel names the history in a listing, at a page's foot and in the
// crumbs: the command that makes it.
const logLabel = "<(git log)"

// maxLog caps the commits a history page lists.
const maxLog = 500

// gitWait bounds a git run that no request waits on.
const gitWait = 5 * time.Second

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
// run, so the options that would start one are off, and git may not fetch
// a partial clone's missing objects. Pathspecs are literal, and no GIT_
// variable from mdserve's own environment leaks in. git's safe.directory
// check refuses repos that other users own.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	args = append([]string{"-c", "protocol.allow=never", "--no-pager", "--literal-pathspecs"}, args...)
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
	out, err := git(ctx, dir, "log", "--no-ext-diff", "--no-textconv", "--no-show-signature", "--no-color",
		"-z", "--format=%H%n%h%n%aI%n%s%n%b", "-n", fmt.Sprint(n), "--", spec)
	if err != nil {
		return nil, t.gitErr(err)
	}
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
	return log, nil
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

// dropTrailers cuts a body's trailer block: a last paragraph made only of
// Key: value lines, such as Signed-off-by or Claude-Session.
func dropTrailers(body string) string {
	paras := strings.Split(body, "\n\n")
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		if !trailer.MatchString(line) {
			return body
		}
	}
	return strings.TrimSpace(strings.Join(paras[:len(paras)-1], "\n\n"))
}

var trailer = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*: \S`)

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
// complaint, which changes only if a repo appears.
func (t *tree) gitHead(name string) (gitDir, state string) {
	dir, _, err := t.gitPlace(name)
	if err != nil {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitWait)
	defer cancel()
	out, err := git(ctx, dir, "rev-parse", "--absolute-git-dir", "HEAD")
	if err != nil {
		return "", string(out) + err.Error()
	}
	gitDir, _, _ = strings.Cut(string(out), "\n")
	return gitDir, string(out)
}

// commitLine is a commit's hash and date, as a history and a page's foot
// show them.
func commitLine(c *commit) string {
	esc := template.HTMLEscapeString
	return fmt.Sprintf("<span class=\"hash\" title=\"%s\">%s</span> <span class=\"date\">%s</span>",
		esc(c.hash), esc(c.short), c.date.Local().Format("2006-01-02 15:04"))
}

// logFoot is the line at the foot of a doc or code page that leads to its
// history and names its last commit, or "" when it has none.
func (t *tree) logFoot(ctx context.Context, name string) string {
	c := t.lastCommit(ctx, name)
	if c == nil {
		return ""
	}
	return fmt.Sprintf("<p class=\"mdserve foot\"><a href=\"?log\">%s</a> %s %s</p>\n",
		template.HTMLEscapeString(logLabel), commitLine(c), template.HTMLEscapeString(c.subject))
}

// serveLog lists the commits that touched name, newest first, each with
// its hash, date, subject and body.
func (t *tree) serveLog(w http.ResponseWriter, r *http.Request, name string, dir bool) {
	crumbs := t.crumbs(name)
	if dir && name != "." {
		name += "/"
	}
	crumbs[len(crumbs)-1].Href = (&url.URL{Path: t.prefix + strings.TrimPrefix(name, ".")}).String()
	crumbs = append(crumbs, crumb{Label: logLabel})
	title := "git log " + t.title(strings.TrimSuffix(name, "/"))
	log, err := t.gitLog(r.Context(), strings.TrimSuffix(name, "/"), maxLog+1)
	if err != nil {
		t.render(w, r, http.StatusNotFound, page{Title: title, Crumbs: crumbs, Kind: "error",
			Body: template.HTML("<pre class=\"error\">" + template.HTMLEscapeString(err.Error()) + "</pre>")})
		return
	}
	esc := template.HTMLEscapeString
	var body strings.Builder
	if len(log) == 0 {
		body.WriteString("<p class=\"mdserve\">No commit touches " + esc(name) + ".</p>\n")
	}
	if len(log) > maxLog {
		log = log[:maxLog]
		fmt.Fprintf(&body, "<p class=\"mdserve\">The %d newest commits. <code>git log</code> lists the rest.</p>\n", maxLog)
	}
	body.WriteString("<ol class=\"log\">\n")
	for i := range log {
		c := &log[i]
		fmt.Fprintf(&body, "<li>\n<p class=\"mdserve\">%s</p>\n<p class=\"subject\">%s</p>\n", commitLine(c), esc(c.subject))
		for _, para := range paragraphs(c.body) {
			body.WriteString("<p>" + para + "</p>\n")
		}
		body.WriteString("</li>\n")
	}
	body.WriteString("</ol>\n")
	t.render(w, r, http.StatusOK, page{Title: title, Crumbs: crumbs, Kind: "log", Body: template.HTML(body.String())})
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
