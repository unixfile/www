package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	return gitRun(ctx, dir, nil, progress, args...)
}

// gitIn is git with stdin as its standard input.
func gitIn(ctx context.Context, dir string, stdin io.Reader, args ...string) ([]byte, error) {
	return gitRun(ctx, dir, stdin, nil, args...)
}

func gitRun(ctx context.Context, dir string, stdin io.Reader, progress func(string), args ...string) ([]byte, error) {
	remote := inCache(dir)
	pre := []string{"-c", "protocol.allow=never"}
	if remote {
		pre = append(pre, "-c", "protocol."+webScheme+".allow=always", "-c", "credential.helper=", "-c", "core.askPass=",
			"-c", "core.hooksPath="+os.DevNull)
		// git may fetch here, even for a page's blob, so a public server
		// checks where it would connect first.
		if publicNet.Load() {
			if host := gitHost(dir, args); host != "" {
				pin, err := reach(ctx, host)
				if err != nil {
					return nil, err
				}
				pre = append(pre, pin...)
			}
		}
	}
	args = append(append(pre, "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--no-pager", "--literal-pathspecs"), args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
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

// commits reads git's records in commitFormat, each ended by a NUL; a
// share drops their trailers.
func (t *tree) commits(out []byte) []commit {
	log := parseCommits(out)
	if t.shared {
		for i := range log {
			log[i].body = dropTrailers(log[i].body)
		}
	}
	return log
}

// parseCommits reads git's records in commitFormat, each ended by a NUL.
func parseCommits(out []byte) []commit {
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

// gitErr is err, or on a share errNoHistory. On a public tree it names
// the cache as "cache", since git's errors name the paths it was given.
func (t *tree) gitErr(err error) error {
	if t.shared {
		return errNoHistory
	}
	if t.public && err != nil && cacheRoot != "" && strings.Contains(err.Error(), cacheRoot) {
		return errors.New(strings.ReplaceAll(err.Error(), cacheRoot, "cache"))
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

// pageChanged reports whether b, the file of the page at name, differs
// from its last commit. Git's id for b, against the id HEAD keeps for
// the path, answers in microseconds; git status, which took 16 ms on
// node, runs only when the ids differ, since a filter such as autocrlf
// can make equal files differ in bytes.
func (t *tree) pageChanged(ctx context.Context, name string, b []byte) bool {
	if id := t.committedBlob(ctx, name); id != "" && blobID(b, len(id)) == id {
		return false
	}
	return t.changed(ctx, name)
}

// committedBlob is the id of the blob HEAD holds for the file at name, or
// "" when git cannot say. It is kept under HEAD's commit.
func (t *tree) committedBlob(ctx context.Context, name string) string {
	dir, spec, err := t.gitPlace(name)
	if err != nil {
		return ""
	}
	r := repoAt(ctx, dir)
	if r.complaint != "" {
		return ""
	}
	key := "blob\x00" + r.gitDir + "\x00" + r.head(ctx) + "\x00" + dir + "\x00" + spec
	if v, ok := gitCache.get(key); ok {
		return v.(string)
	}
	out, err := git(ctx, dir, "rev-parse", "--verify", "-q", "HEAD:./"+spec)
	id := strings.TrimSpace(string(out))
	if err != nil {
		id = ""
	}
	gitCache.put(key, id, int64(len(key)+len(id)+32))
	return id
}

// blobID is git's id for a blob holding b: SHA-1 for an id of n = 40 hex
// digits, else SHA-256.
func blobID(b []byte, n int) string {
	h := sha256.New()
	if n == 40 {
		h = sha1.New()
	}
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
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
