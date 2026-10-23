package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// web serves the second address, 127.31.N.2. A URL there names a repo on
// a forge by host and path, eg /github.com/junegunn/fzf/README.md, which
// web fetches into its cache on the first visit and serves as a tree
// from then on. A path that names no repo, eg /github.com/junegunn, lists
// the repos it holds.
//
// Each repo in the cache is a bare clone without blobs, in <key>/.git, and
// a detached worktree of the commit served, in <key>/.wt-<commit>, which
// <key>/.tree names. A fetch that moves HEAD builds the next worktree
// beside the old one and swaps the link, so no page sees a tree half
// updated. A worktree has its own HEAD, so history pages show the commit
// served. git fetches a commit's blobs for a worktree in one request,
// where a checkout of paths would fetch them one by one.
type web struct {
	root  string // the cache, its links resolved
	limit int64  // the cache's disk limit
	srv   *server
	ctx   context.Context // ends with the server, and every fetch with it

	// loadMu makes taking a repo from the disk one step, so two visits,
	// or a visit and a clone, never serve one repo twice.
	loadMu sync.Mutex
	mu     sync.Mutex
	jobs   map[string]*webJob  // by URL path: what a visit set going
	repos  map[string]*webRepo // by key, eg github.com/junegunn/fzf
	lists  map[string]*listing // by the URL path of a host or account
	busy   map[string]bool     // keys being cloned or fetched, which trim leaves
	kicked time.Time
}

// webScheme is how web reaches forges. Tests serve repos over http.
var webScheme = "https"

// webRefresh is how old a fetch may be before a visit fetches again.
const webRefresh = 5 * time.Minute

// defaultDisk is the cache's disk limit without --disk.
const defaultDisk = 2 << 30

// probeMax caps the prefixes probed for one path.
const probeMax = 8

// listFresh is how long a listing is kept. GitHub allows 60 calls an hour
// without a token.
const listFresh = time.Hour

// missFresh is how long a path that names nothing is kept as such.
const missFresh = 5 * time.Minute

// cacheRoot is the cache directory once web runs, its links resolved.
// git lets calls below it fetch from the repo's forge.
var cacheRoot string

// inCache reports whether dir lies in web's cache.
func inCache(dir string) bool {
	return cacheRoot != "" && (dir == cacheRoot || strings.HasPrefix(dir, cacheRoot+"/"))
}

func cacheDir() (string, error) {
	if d := os.Getenv("XDG_CACHE_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, progName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", progName), nil
}

// webEndpoint is where a server on ep serves the web: the .2 beside a
// user's default address, on the same port.
func webEndpoint(ep netip.AddrPort, uid int) (netip.AddrPort, bool) {
	def, err := endpointFor("", uid)
	if err != nil || ep.Addr() != def.Addr() {
		return netip.AddrPort{}, false
	}
	a := ep.Addr().As4()
	a[3] = 2
	return netip.AddrPortFrom(netip.AddrFrom4(a), ep.Port()), true
}

func newWeb(ctx context.Context, root string, limit int64) (*web, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	cacheRoot = real
	w := &web{root: real, limit: limit, ctx: ctx, jobs: map[string]*webJob{}, repos: map[string]*webRepo{},
		lists: map[string]*listing{}, busy: map[string]bool{}}
	w.srv = newServer()
	w.srv.web = w
	return w, nil
}

// webRepo is one repo in the cache.
type webRepo struct {
	key, dir string
	mu       sync.Mutex
	tree     *tree
	fetched  time.Time // the last fetch that worked
	tried    time.Time // the last fetch begun
	fetching bool
	failed   string // why the last fetch failed, or ""
	used     time.Time
}

// status is what a page of the repo says in its header: the commit shown
// and when it was fetched.
func (r *webRepo) status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := "at " + strings.TrimPrefix(filepath.Base(r.tree.dir), ".wt-")[:7] + ", fetched " + clock(r.fetched)
	switch {
	case r.fetching:
		s += ", fetching"
	case r.failed != "":
		s += ", fetch at " + clock(r.tried) + " failed"
	}
	return s
}

// webJob is what one visit set going: a probe for the repo the path lies
// in, then its clone or the listing of the path. Its steps are what the
// reading page shows.
type webJob struct {
	urlPath string
	started time.Time
	steps   []string
	line    string // git's latest progress line
	err     string
	ended   time.Time
}

// serve answers every request on the web address.
func (w *web) serve(rw http.ResponseWriter, r *http.Request, t *tree) {
	// The user may delete the cache, or a repo in it, under the server.
	if t != nil && !exists(t.dir) {
		w.forget(t.web)
		t = nil
	}
	if t != nil {
		w.visit(t.web)
		t.serve(rw, r)
		return
	}
	p := path.Clean(r.URL.Path)
	if p == "/" {
		w.serveRoot(rw, r)
		return
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	host := strings.ToLower(segs[0])
	if !validHost(host) {
		w.fail(rw, r, http.StatusNotFound, "Not a forge: "+segs[0]+". Name a host and a path, eg /github.com/junegunn/fzf")
		return
	}
	// A repo has one URL: the host in lower case, without .git.
	last := segs[len(segs)-1]
	if trimmed := strings.TrimSuffix(last, ".git"); host != segs[0] || len(segs) > 1 && trimmed != last && trimmed != "" {
		segs[0], segs[len(segs)-1] = host, trimmed
		http.Redirect(rw, r, (&url.URL{Path: "/" + strings.Join(segs, "/")}).String(), http.StatusMovedPermanently)
		return
	}
	if w.cached(segs) != "" {
		if t := w.srv.byPrefix(r.URL.Path + "/"); t != nil {
			http.Redirect(rw, r, t.prefixURL(), http.StatusFound)
			return
		}
		if t := w.srv.lookup(r.URL.Path); t != nil {
			w.visit(t.web)
			t.serve(rw, r)
			return
		}
	}
	key := strings.Join(segs, "/")
	w.mu.Lock()
	l := w.lists[key]
	j := w.jobs[key]
	if j != nil && !j.ended.IsZero() && time.Since(j.ended) > 30*time.Second {
		delete(w.jobs, key)
		j = nil
	}
	if l != nil && time.Since(l.at) > l.fresh() {
		delete(w.lists, key)
		l = nil
	}
	w.mu.Unlock()
	switch {
	case l != nil:
		w.serveListing(rw, r, l)
	case j != nil:
		w.serveJob(rw, r, j)
	case r.Header.Get("Sec-Fetch-Site") == "cross-site":
		w.serveConfirm(rw, r, key)
	default:
		w.serveJob(rw, w.srv.withLive(r), w.start(key, segs))
	}
}

// validHost accepts a host name or address, with a port.
func validHost(h string) bool {
	if h == "" || !strings.ContainsAny(h, ".:") || strings.HasPrefix(h, "_") {
		return false
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune(".-:[]", c)) {
			return false
		}
	}
	return true
}

// visit notes a request for repo r, which keeps it in the cache, and
// fetches it again in the background when its last fetch is old.
func (w *web) visit(r *webRepo) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.used) > time.Minute {
		r.used = time.Now()
		touch(filepath.Join(r.dir, ".used"))
	}
	if !r.fetching && time.Since(r.fetched) > webRefresh && time.Since(r.tried) > webRefresh {
		r.fetching, r.tried = true, time.Now()
		go w.refresh(r)
	}
}

func touch(p string) {
	now := time.Now()
	if os.Chtimes(p, now, now) != nil {
		os.WriteFile(p, nil, 0o600)
	}
}

// cached returns the key of the cached repo that the path segs lies in,
// serving it if it is not yet served, or "".
func (w *web) cached(segs []string) string {
	for i := len(segs); i >= 2; i-- {
		key := strings.Join(segs[:i], "/")
		r, err := w.load(key, false)
		if err != nil {
			logErr("E%d: cannot serve %s from the cache: %v", errnoOf(err), key, err)
			return ""
		}
		if r != nil {
			return key
		}
	}
	return ""
}

// load returns the repo key, served at the commit its .tree names when it
// was on disk but not yet served, or nil when the cache lacks it. A repo
// being cloned is lacking until its clone, with mine set, loads it. load
// removes worktrees a crash left behind.
func (w *web) load(key string, mine bool) (*webRepo, error) {
	w.loadMu.Lock()
	defer w.loadMu.Unlock()
	w.mu.Lock()
	r, busy := w.repos[key], w.busy[key]
	w.mu.Unlock()
	if r != nil || busy && !mine {
		return r, nil
	}
	dir := filepath.Join(w.root, filepath.FromSlash(key))
	wt, err := os.Readlink(filepath.Join(dir, ".tree"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r = &webRepo{key: key, dir: dir}
	if info, err := os.Stat(filepath.Join(dir, ".fetched")); err == nil {
		r.fetched = info.ModTime()
	}
	if info, err := os.Stat(filepath.Join(dir, ".used")); err == nil {
		r.used = info.ModTime()
	}
	if err := w.show(r, filepath.Join(dir, wt)); err != nil {
		return nil, err
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".wt-") && e.Name() != wt {
			w.dropTree(r, filepath.Join(dir, e.Name()))
		}
	}
	return r, nil
}

// show serves r from the worktree wt, in place of the one it served.
func (w *web) show(r *webRepo, wt string) error {
	root, err := os.OpenRoot(wt)
	if err != nil {
		return err
	}
	t := newTree(root, wt, "/"+r.key+"/", r.key)
	t.web = r
	r.mu.Lock()
	old := r.tree
	r.tree = t
	r.mu.Unlock()
	w.mu.Lock()
	w.repos[r.key] = r
	w.mu.Unlock()
	w.srv.add(t)
	if old != nil {
		w.srv.remove(old)
		old.root.Close()
	}
	logInfo("serves %s at %s from %s", r.key, filepath.Base(wt), wt)
	go t.warm()
	go t.warmLinks()
	return nil
}

// dropTree removes a worktree no page shows.
func (w *web) dropTree(r *webRepo, wt string) {
	if _, err := git(w.ctx, filepath.Join(r.dir, ".git"), "worktree", "remove", "--force", wt); err != nil {
		os.RemoveAll(wt)
		git(w.ctx, filepath.Join(r.dir, ".git"), "worktree", "prune")
	}
}

// start begins the job for the URL path key, whose segments are segs.
func (w *web) start(key string, segs []string) *webJob {
	w.mu.Lock()
	defer w.mu.Unlock()
	if j := w.jobs[key]; j != nil {
		return j
	}
	j := &webJob{urlPath: "/" + key, started: time.Now()}
	w.jobs[key] = j
	go w.run(j, key, segs)
	return j
}

// step records what the job does now and wakes its page.
func (w *web) step(j *webJob, s string) {
	w.mu.Lock()
	j.steps = append(j.steps, s)
	j.line = ""
	w.mu.Unlock()
	w.kick(true)
}

// progress records git's latest progress line, and wakes the page at most
// once a second.
func (w *web) progress(j *webJob) func(string) {
	return func(line string) {
		w.mu.Lock()
		j.line = line
		w.mu.Unlock()
		w.kick(false)
	}
}

func (w *web) kick(now bool) {
	w.mu.Lock()
	due := now || time.Since(w.kicked) >= time.Second
	if due {
		w.kicked = time.Now()
	}
	w.mu.Unlock()
	if due {
		w.srv.live.kick()
	}
}

// end closes the job, with why it failed or nil.
func (w *web) end(j *webJob, err error) {
	failed := ""
	if err != nil {
		failed = err.Error()
		logErr("E%d: %s: %s", errnoOf(err), strings.TrimPrefix(j.urlPath, "/"), failed)
	}
	w.mu.Lock()
	j.err, j.ended = failed, time.Now()
	if err == nil {
		delete(w.jobs, strings.TrimPrefix(j.urlPath, "/"))
	}
	w.mu.Unlock()
	w.kick(true)
}

// run finds the repo the path lies in and clones it, or lists the path
// when it names no repo.
func (w *web) run(j *webJob, key string, segs []string) {
	repoKey, cloneURL, probed := w.probe(j, segs)
	if repoKey == "" {
		w.list(j, key, probed)
		return
	}
	start := time.Now()
	if err := w.clone(j, repoKey, cloneURL); err != nil {
		// A clone that failed leaves nothing behind.
		if !exists(filepath.Join(w.root, filepath.FromSlash(repoKey), ".git", "HEAD")) {
			w.drop(repoKey)
		}
		// A forge asks for a login where no public repo is.
		if strings.Contains(err.Error(), "could not read Username") {
			err = &errno{syscall.ENOENT, "no repo answers at " + cloneURL + ": the forge asked for a login, which only a private or missing repo needs"}
		}
		w.end(j, err)
		return
	}
	logInfo("read %s from %s in %d ms", repoKey, cloneURL, time.Since(start).Milliseconds())
	w.end(j, nil)
}

// depths gives, for forges whose repos sit at one depth, the number of
// segments in a repo's key, host included.
var depths = map[string]int{"github.com": 3, "bitbucket.org": 3, "codeberg.org": 3}

// minDepths gives the fewest segments of a repo's key on forges with
// groups, host included.
var minDepths = map[string]int{"gitlab.com": 3}

// probe asks every prefix of the path at once whether a repo answers
// there, each with and without .git, and takes the longest that does. It
// returns the repo's key and URL, or "" and whether it probed at all.
func (w *web) probe(j *webJob, segs []string) (key, cloneURL string, probed bool) {
	lo, hi := 2, min(len(segs), probeMax+1)
	if d, ok := depths[segs[0]]; ok {
		// The repo is the first d segments, so a probe would tell
		// nothing the clone does not, and costs a round trip.
		if len(segs) >= d {
			key = strings.Join(segs[:d], "/")
			w.step(j, segs[0]+" keeps every repo at owner/name, so "+webScheme+"://"+key+" needs no probe")
			return key, webScheme + "://" + key, true
		}
		lo, hi = d, min(d, len(segs))
	} else if d, ok := minDepths[segs[0]]; ok {
		lo = d
	}
	if lo > hi {
		return "", "", false
	}
	type answer struct {
		n   int
		url string
	}
	var urls []string
	for n := hi; n >= lo; n-- {
		u := webScheme + "://" + strings.Join(segs[:n], "/")
		urls = append(urls, u)
	}
	w.step(j, "git ls-remote, at once, on "+strings.Join(urls, " "))
	start := time.Now()
	ctx, cancel := context.WithTimeout(w.ctx, 30*time.Second)
	defer cancel()
	ch := make(chan answer)
	for n := hi; n >= lo; n-- {
		for _, u := range []string{webScheme + "://" + strings.Join(segs[:n], "/"), webScheme + "://" + strings.Join(segs[:n], "/") + ".git"} {
			go func() {
				if _, err := git(ctx, w.root, "ls-remote", u, "HEAD"); err == nil {
					ch <- answer{n, u}
				} else {
					ch <- answer{0, ""}
				}
			}()
		}
	}
	var best answer
	for range 2 * (hi - lo + 1) {
		if a := <-ch; a.n > best.n || a.n == best.n && a.n > 0 && len(a.url) < len(best.url) {
			best = a
		}
	}
	ms := time.Since(start).Milliseconds()
	if best.n == 0 {
		w.step(j, fmt.Sprintf("no repo answered, in %d ms", ms))
		return "", "", true
	}
	w.step(j, fmt.Sprintf("%s answered, in %d ms", best.url, ms))
	return strings.Join(segs[:best.n], "/"), best.url, true
}

// clone fetches the repo key from cloneURL into the cache and serves it.
func (w *web) clone(j *webJob, key, cloneURL string) error {
	w.mu.Lock()
	r := w.repos[key]
	w.mu.Unlock()
	if r != nil {
		return nil
	}
	dir := filepath.Join(w.root, filepath.FromSlash(key))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := w.lockRepo(key, dir)
	if err != nil {
		return err
	}
	defer unlock()
	// Another visit or server may have cloned it meanwhile.
	if r, err := w.load(key, true); r != nil || err != nil {
		return err
	}
	bare := filepath.Join(dir, ".git")
	if !exists(filepath.Join(bare, "HEAD")) {
		tmp := bare + ".tmp"
		os.RemoveAll(tmp)
		w.step(j, "git clone --bare --filter=blob:none "+cloneURL+" "+tildePath(bare))
		start := time.Now()
		if _, err := gitProgress(w.ctx, dir, w.progress(j), "clone", "--bare", "--filter=blob:none", "--progress", "--", cloneURL, tmp); err != nil {
			os.RemoveAll(tmp)
			return err
		}
		if err := os.Rename(tmp, bare); err != nil {
			return err
		}
		w.step(j, fmt.Sprintf("cloned in %s", seconds(time.Since(start))))
	}
	r = &webRepo{key: key, dir: dir}
	touch(filepath.Join(dir, ".fetched"))
	touch(filepath.Join(dir, ".used"))
	if _, err := w.checkout(r, w.progress(j), func(s string) { w.step(j, s) }); err != nil {
		return err
	}
	// The cache must hold the new repo before a page shows it.
	if err := w.trim(key); err != nil {
		return err
	}
	_, err = w.load(key, true)
	return err
}

// checkout builds the worktree of the commit HEAD names, unless it is
// there, and points .tree at it.
func (w *web) checkout(r *webRepo, progress func(string), step func(string)) (string, error) {
	bare := filepath.Join(r.dir, ".git")
	out, err := git(w.ctx, bare, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(out))
	name := ".wt-" + head[:12]
	wt := filepath.Join(r.dir, name)
	if !exists(wt) {
		step("git worktree add --detach " + tildePath(wt) + " " + head[:12] + ", which fetches its files")
		start := time.Now()
		if _, err := gitProgress(w.ctx, bare, progress, "worktree", "add", "--detach", "--", wt, head); err != nil {
			os.RemoveAll(wt)
			git(w.ctx, bare, "worktree", "prune")
			return "", err
		}
		step(fmt.Sprintf("checked out in %s", seconds(time.Since(start))))
	}
	link := filepath.Join(r.dir, ".tree")
	os.Remove(link + ".new")
	if err := os.Symlink(name, link+".new"); err != nil {
		return "", err
	}
	return wt, os.Rename(link+".new", link)
}

// refresh fetches r again, and serves the new commit when HEAD moved. A
// failed fetch leaves the commit served, and its pages say so.
func (w *web) refresh(r *webRepo) {
	failed := ""
	defer func() {
		r.mu.Lock()
		r.fetching, r.failed = false, failed
		if failed == "" {
			r.fetched = time.Now()
		}
		r.mu.Unlock()
		w.kick(true)
	}()
	w.kick(true)
	unlock, err := w.lockRepo(r.key, r.dir)
	if err != nil {
		failed = err.Error()
		return
	}
	defer unlock()
	bare := filepath.Join(r.dir, ".git")
	start := time.Now()
	if _, err := git(w.ctx, bare, "fetch", "--prune", "--no-write-fetch-head", "origin",
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		failed = err.Error()
		logErr("E%d: cannot fetch %s: %s", syscall.EHOSTUNREACH, r.key, failed)
		return
	}
	touch(filepath.Join(r.dir, ".fetched"))
	r.mu.Lock()
	old := r.tree.dir
	r.mu.Unlock()
	wt, err := w.checkout(r, nil, func(string) {})
	if err != nil {
		failed = err.Error()
		return
	}
	logInfo("fetched %s in %d ms", r.key, time.Since(start).Milliseconds())
	if wt == old {
		return
	}
	if err := w.show(r, wt); err != nil {
		failed = err.Error()
		return
	}
	w.dropTree(r, old)
	if err := w.trim(r.key); err != nil {
		failed = err.Error()
	}
}

// lockRepo marks the repo key busy and takes its lock.
func (w *web) lockRepo(key, dir string) (func(), error) {
	w.mu.Lock()
	w.busy[key] = true
	w.mu.Unlock()
	unlock, err := flockDir(dir)
	if err != nil {
		unlock = func() {}
	}
	return func() {
		unlock()
		w.mu.Lock()
		delete(w.busy, key)
		w.mu.Unlock()
	}, err
}

// flockDir takes the repo directory's lock, which keeps two servers from
// fetching one repo at once.
func flockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// cachedRepo is a repo found on disk in the cache.
type cachedRepo struct {
	key  string
	size int64
	used time.Time
}

// onDisk lists the repos in the cache, with their size and last request.
func (w *web) onDisk() []cachedRepo {
	var out []cachedRepo
	filepath.WalkDir(w.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		name := d.Name()
		if p != w.root && (strings.HasPrefix(name, ".wt-") || name == ".git" || name == ".git.tmp") {
			return fs.SkipDir
		}
		if !exists(filepath.Join(p, ".git", "HEAD")) {
			return nil
		}
		key, _ := filepath.Rel(w.root, p)
		c := cachedRepo{key: filepath.ToSlash(key), size: diskUse(filepath.Join(p, ".git"))}
		if wt, err := os.Readlink(filepath.Join(p, ".tree")); err == nil {
			c.size += diskUse(filepath.Join(p, wt))
		}
		for _, f := range []string{".used", ".fetched"} {
			if info, err := os.Stat(filepath.Join(p, f)); err == nil {
				c.used = info.ModTime()
				break
			}
		}
		out = append(out, c)
		return nil
	})
	return out
}

// diskUse is the disk space below dir.
func diskUse(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil {
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				n += st.Blocks * 512
			}
		}
		return nil
	})
	return n
}

// trim drops the repos requested longest ago until the cache fits its
// limit. keep, just fetched, goes last; alone over the limit it goes too,
// and trim says so.
func (w *web) trim(keep string) error {
	repos := w.onDisk()
	var total int64
	for _, c := range repos {
		total += c.size
	}
	slices.SortFunc(repos, func(a, b cachedRepo) int {
		if (a.key == keep) != (b.key == keep) {
			if a.key == keep {
				return 1
			}
			return -1
		}
		return a.used.Compare(b.used)
	})
	for _, c := range repos {
		if total <= w.limit {
			return nil
		}
		w.mu.Lock()
		busy := w.busy[c.key] && c.key != keep
		w.mu.Unlock()
		if busy {
			continue
		}
		if c.key == keep {
			w.drop(c.key)
			return &errno{syscall.EFBIG, fmt.Sprintf("%s takes %s, over the cache's limit of %s, so it was dropped; --disk sets the limit",
				c.key, sizeText(c.size), sizeText(w.limit))}
		} else {
			logInfo("drops %s, %s, last requested %s, since the cache is over %s", c.key, sizeText(c.size), c.used.Format(time.DateTime), sizeText(w.limit))
		}
		w.drop(c.key)
		total -= c.size
	}
	return nil
}

// drop stops serving the repo key and deletes it from the cache.
func (w *web) drop(key string) {
	w.mu.Lock()
	r := w.repos[key]
	w.mu.Unlock()
	w.forget(r)
	dir := filepath.Join(w.root, filepath.FromSlash(key))
	os.RemoveAll(dir)
	for d := filepath.Dir(dir); d != w.root && strings.HasPrefix(d, w.root); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			break
		}
	}
}

// forget stops serving r and leaves the disk as it is.
func (w *web) forget(r *webRepo) {
	if r == nil {
		return
	}
	w.mu.Lock()
	if w.repos[r.key] == r {
		delete(w.repos, r.key)
	}
	w.mu.Unlock()
	r.mu.Lock()
	t := r.tree
	r.mu.Unlock()
	w.srv.remove(t)
	t.root.Close()
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d kB", n>>10)
	}
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1f s", d.Seconds())
}

// tildePath writes a path below home with ~.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// listing is the repos a host or account holds, and what told.
type listing struct {
	at     time.Time
	source string   // what answered, eg "the GitHub API"
	asked  string   // the URL asked
	failed []string // the sources that failed before it, and why
	repos  []listed
}

type listed struct{ key, desc string }

func (l *listing) fresh() time.Duration {
	if len(l.repos) == 0 {
		return missFresh
	}
	return listFresh
}

// list lists the path key, by the first source that answers: the forge's
// API, a web frontend's index, then the cache.
func (w *web) list(j *webJob, key string, probed bool) {
	segs := strings.Split(key, "/")
	l := &listing{at: time.Now()}
	for _, src := range listSources(segs) {
		w.step(j, "GET "+src.url+", "+src.name)
		repos, err := w.ask(src)
		if err == nil && len(repos) > 0 {
			l.source, l.asked, l.repos = src.name, src.url, repos
			break
		}
		if err == nil {
			err = errors.New("no repos")
		}
		l.failed = append(l.failed, src.name+": "+err.Error())
		w.step(j, src.name+": "+err.Error())
	}
	if l.source == "" {
		l.source = "the cache"
		for _, c := range w.onDisk() {
			if strings.HasPrefix(c.key, key+"/") {
				l.repos = append(l.repos, listed{key: c.key})
			}
		}
		slices.SortFunc(l.repos, func(a, b listed) int { return strings.Compare(a.key, b.key) })
	}
	if len(l.repos) == 0 && probed && len(l.failed) == 0 {
		l.failed = append(l.failed, "no source lists "+key)
	}
	logInfo("listed %d repos at %s by %s", len(l.repos), key, l.source)
	w.mu.Lock()
	w.lists[key] = l
	w.mu.Unlock()
	w.end(j, nil)
}

// listSource is one place that may list a path's repos.
type listSource struct {
	name, url string
	parse     func(body []byte, next *string) ([]listed, error)
}

var (
	cgitRepo   = regexp.MustCompile(`<td class='(?:top|sub)level-repo'><a (?:title='[^']*' )?href='([^']+)'`)
	stagitRepo = regexp.MustCompile(`<td><a href="([^"/]+)/log\.html">`)
)

// listSources are the sources for the path segs, in the order to ask them.
func listSources(segs []string) []listSource {
	host, rest := segs[0], strings.Join(segs[1:], "/")
	var out []listSource
	jsonList := func(field string) func([]byte, *string) ([]listed, error) {
		return func(b []byte, _ *string) ([]listed, error) {
			var v []map[string]any
			if err := json.Unmarshal(b, &v); err != nil {
				return nil, errors.New("not a list of repos")
			}
			var out []listed
			for _, m := range v {
				name, _ := m[field].(string)
				desc, _ := m["description"].(string)
				if name != "" {
					out = append(out, listed{key: host + "/" + name, desc: desc})
				}
			}
			return out, nil
		}
	}
	switch {
	case host == "github.com" && len(segs) == 2:
		out = append(out, listSource{"the GitHub API", "https://api.github.com/users/" + rest + "/repos?per_page=100", jsonList("full_name")})
	case host == "gitlab.com" && len(segs) >= 2:
		if len(segs) == 2 {
			out = append(out, listSource{"the GitLab API", "https://gitlab.com/api/v4/users/" + rest + "/projects?per_page=100", jsonList("path_with_namespace")})
		}
		out = append(out, listSource{"the GitLab API", "https://gitlab.com/api/v4/groups/" + url.PathEscape(rest) + "/projects?per_page=100&include_subgroups=true", jsonList("path_with_namespace")})
	case host == "bitbucket.org" && len(segs) == 2:
		out = append(out, listSource{"the Bitbucket API", "https://api.bitbucket.org/2.0/repositories/" + rest + "?pagelen=100",
			func(b []byte, next *string) ([]listed, error) {
				var v struct {
					Values []struct{ Full_name, Description string }
					Next   string
				}
				if err := json.Unmarshal(b, &v); err != nil {
					return nil, errors.New("not a list of repos")
				}
				*next = v.Next
				var out []listed
				for _, r := range v.Values {
					out = append(out, listed{key: host + "/" + r.Full_name, desc: r.Description})
				}
				return out, nil
			}})
	case len(segs) == 2 && !slices.Contains([]string{"github.com", "gitlab.com", "bitbucket.org"}, host):
		out = append(out, listSource{"the Forgejo API", webScheme + "://" + host + "/api/v1/users/" + rest + "/repos?limit=50", jsonList("full_name")})
	}
	if _, forge := depths[host]; forge || host == "gitlab.com" {
		return out
	}
	base := webScheme + "://" + strings.Join(segs, "/") + "/"
	out = append(out, listSource{"gitweb's project index", base + "?a=project_index", func(b []byte, _ *string) ([]listed, error) {
		var out []listed
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 0 || strings.ContainsAny(f[0], "<>\"") {
				return nil, errors.New("not a project index")
			}
			name, err := url.QueryUnescape(f[0])
			if err != nil {
				return nil, errors.New("not a project index")
			}
			out = append(out, listed{key: strings.Join(segs, "/") + "/" + strings.TrimSuffix(name, ".git")})
		}
		return out, nil
	}})
	out = append(out, listSource{"the frontend's front page", base, func(b []byte, _ *string) ([]listed, error) {
		var out []listed
		for _, m := range cgitRepo.FindAllSubmatch(b, -1) {
			p := strings.Trim(string(m[1]), "/")
			out = append(out, listed{key: host + "/" + strings.TrimSuffix(p, ".git")})
		}
		for _, m := range stagitRepo.FindAllSubmatch(b, -1) {
			out = append(out, listed{key: strings.Join(segs, "/") + "/" + string(m[1])})
		}
		return out, nil
	}})
	return out
}

var webClient = &http.Client{Timeout: 20 * time.Second}

// ask reads src, following the forge's pages up to 10.
func (w *web) ask(src listSource) ([]listed, error) {
	var out []listed
	u := src.url
	for page := 1; page <= 10 && u != ""; page++ {
		req, err := http.NewRequestWithContext(w.ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", progName)
		resp, err := webClient.Do(req)
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, errors.New(resp.Status)
		}
		next := ""
		got, err := src.parse(b, &next)
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		switch {
		case next != "":
			u = next
		case strings.Contains(src.url, "api") && len(got) >= 50 && !strings.Contains(src.url, "bitbucket"):
			u = src.url + "&page=" + strconv.Itoa(page+1)
		default:
			u = ""
		}
	}
	return out, nil
}

// version writes the state of a page at urlPath that no tree serves.
func (w *web) version(h io.Writer, urlPath string) {
	key := strings.Trim(path.Clean(urlPath), "/")
	w.mu.Lock()
	defer w.mu.Unlock()
	if j := w.jobs[key]; j != nil {
		fmt.Fprintf(h, "job %p\n%s\n%s\n%s\n", j, strings.Join(j.steps, "\n"), j.line, j.err)
	}
	if l := w.lists[key]; l != nil {
		fmt.Fprintf(h, "list %p\n", l)
	}
	keys := make([]string, 0, len(w.repos))
	for k := range w.repos {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	io.WriteString(h, strings.Join(keys, "\n"))
}

// crumbs link the root and each segment of a path that no tree serves.
func webCrumbs(key string) []crumb {
	out := []crumb{indexCrumb}
	segs := strings.Split(key, "/")
	for i, s := range segs {
		c := crumb{Label: s}
		if i < len(segs)-1 {
			c.Href = (&url.URL{Path: "/" + strings.Join(segs[:i+1], "/") + "/"}).String()
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

// serveRoot lists the cache: each repo, its size and its last request.
func (w *web) serveRoot(rw http.ResponseWriter, r *http.Request) {
	esc := template.HTMLEscapeString
	repos := w.onDisk()
	slices.SortFunc(repos, func(a, b cachedRepo) int { return strings.Compare(a.key, b.key) })
	var total int64
	var body strings.Builder
	body.WriteString("<p class=\"own\">Type a forge's host and a path after this address, eg <a href=\"/github.com/junegunn/fzf/\">/github.com/junegunn/fzf</a>, to read that repo, or <a href=\"/git.suckless.org/\">/git.suckless.org</a> to list a host's repos.</p>\n")
	if len(repos) > 0 {
		body.WriteString("<table class=\"locks own\">\n")
		for _, c := range repos {
			total += c.size
			fmt.Fprintf(&body, "<tr><td><a href=\"%s\">%s</a> <span class=\"since\">%s, last read %s</span></td></tr>\n",
				esc((&url.URL{Path: "/" + c.key + "/"}).String()), segments(c.key), sizeText(c.size), clock(c.used))
		}
		body.WriteString("</table>\n")
	}
	fmt.Fprintf(&body, "<p class=\"own\">The cache in %s holds %d repos in %s of its %s.</p>\n", esc(tildePath(w.root)), len(repos), sizeText(total), sizeText(w.limit))
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
		body.WriteString(" The page turns into the repo when it is read.")
	}
	body.WriteString("</p>\n")
	key := strings.TrimPrefix(j.urlPath, "/")
	w.render(rw, r, status, "reading "+key, webCrumbs(key), "reading", body.String())
}

// serveConfirm asks before a download that another site set going.
func (w *web) serveConfirm(rw http.ResponseWriter, r *http.Request, key string) {
	esc := template.HTMLEscapeString
	u := (&url.URL{Path: "/" + key}).String()
	body := fmt.Sprintf("<p class=\"own\">Another site sent you here, so nothing is fetched yet. <a href=\"%s\">Read %s</a> fetches it from %s into the cache.</p>\n",
		esc(u), esc(key), esc(webScheme+"://"+key))
	w.render(rw, r, http.StatusOK, key, webCrumbs(key), "confirm", body)
}

// serveListing lists a host's or account's repos, and names its source.
func (w *web) serveListing(rw http.ResponseWriter, r *http.Request, l *listing) {
	esc := template.HTMLEscapeString
	key := strings.Trim(path.Clean(r.URL.Path), "/")
	var body strings.Builder
	status := http.StatusOK
	if len(l.repos) == 0 {
		status = http.StatusNotFound
		fmt.Fprintf(&body, "<pre class=\"error\">No repo answers at %s, and nothing lists one there.</pre>\n", esc(webScheme+"://"+key))
	} else {
		body.WriteString("<ul class=\"listing\">\n")
		for _, e := range l.repos {
			name := strings.TrimPrefix(e.key, key+"/")
			fmt.Fprintf(&body, "<li><a href=\"%s\">%s/</a>", esc((&url.URL{Path: "/" + e.key + "/"}).String()), esc(name))
			if e.desc != "" {
				fmt.Fprintf(&body, " <span class=\"own desc\">%s</span>", esc(e.desc))
			}
			body.WriteString("</li>\n")
		}
		body.WriteString("</ul>\n")
	}
	src := l.source
	if l.asked != "" {
		src += ", " + l.asked
	}
	fmt.Fprintf(&body, "<p class=\"own\">%d repos, listed by %s at %s.", len(l.repos), esc(src), clock(l.at))
	if l.source == "the cache" {
		body.WriteString(" The cache holds only what was read here, so the host may hold more.")
	}
	body.WriteString("</p>\n")
	if len(l.failed) > 0 {
		body.WriteString("<p class=\"own\">Failed first: " + esc(strings.Join(l.failed, "; ")) + ".</p>\n")
	}
	w.render(rw, r, status, key, webCrumbs(key), "index", body.String())
}
