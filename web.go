package main

import (
	"context"
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
	// public hides the cache from strangers: no listing of it, and no
	// path of the machine on a page.
	public bool

	// loadMu makes taking a repo from the disk one step, so two visits,
	// or a visit and a clone, never serve one repo twice.
	loadMu sync.Mutex
	mu     sync.Mutex
	jobs   map[string]*webJob  // by URL path: what a visit set going
	repos  map[string]*webRepo // by key, eg github.com/junegunn/fzf
	lists  map[string]*listing // by the URL path of a host or account
	busy   map[string]bool     // keys being cloned or fetched, which trim leaves
	kicked time.Time

	// A public server limits what one visitor sets going, so a bot cannot
	// make it clone without end; a cached repo is never limited. visitors
	// holds the jobs each address started within visitorWindow, and
	// clones wait for a place in slots, which is nil elsewhere.
	visitors map[netip.Prefix][]*webJob
	swept    time.Time
	slots    chan struct{}

	// wg counts the goroutines web starts, so a test can wait for them
	// before it changes the globals they read.
	wg sync.WaitGroup
}

// A public server lets one address run one job at a time and start at
// most visitorJobs within visitorWindow, and runs at most cloneSlots
// clones at once. A job is a probe, a clone or a listing: whatever a visit
// to an uncached path sets going.
const (
	visitorJobs   = 20
	visitorWindow = time.Hour
	cloneSlots    = 4
)

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
		lists: map[string]*listing{}, busy: map[string]bool{}, visitors: map[netip.Prefix][]*webJob{}}
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
	named    shownCommit // the worktree's commit as the header names it, read once per worktree
}

// shownCommit is how a repo's header names the commit its worktree holds.
type shownCommit struct {
	dir, label, addr, title string
}

// status is what a page of the repo says in its header: the commit shown,
// and a fetch under way or failed. The commit is named as the crumbs of a
// version name it, by its stardate with its hash and time on hover, and
// links this page in that version, so the address keeps naming it after
// the next fetch. When the copy was fetched shows only once a fetch has
// failed: a visit fetches a copy over five minutes old, so a page without
// a failure always shows a current copy.
func (r *webRepo) status(ctx context.Context, prefix, urlPath string) template.HTML {
	r.mu.Lock()
	dir, named, fetched, tried, fetching, failed := r.tree.dir, r.named, r.fetched, r.tried, r.fetching, r.failed
	r.mu.Unlock()
	if named.dir != dir {
		hash := strings.TrimPrefix(filepath.Base(dir), ".wt-")
		named = shownCommit{dir: dir, label: hash[:7], addr: hash, title: hash[:7]}
		if c, stars, err := findCommit(ctx, dir, hash); err == nil {
			named.label, named.addr, named.title = stars.label(&c), stars.addr(&c), c.short+" "+exact(c.date)
			r.mu.Lock()
			r.named = named
			r.mu.Unlock()
		}
	}
	esc := template.HTMLEscapeString
	href := (&url.URL{Path: versionPrefix(prefix, "@"+named.addr) + strings.TrimPrefix(urlPath, prefix)}).String()
	s := "@ <a href=\"" + esc(href) + "\" title=\"" + esc(named.title) + "\">" + esc(named.label) + "</a>"
	switch {
	case fetching:
		s += " · fetching"
	case failed != "":
		s += " · fetched " + clock(fetched) + " · fetch at " + clock(tried) + " failed"
	}
	return template.HTML(s)
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
	// A URL pasted whole after the address, eg
	// /https://github.com/junegunn/fzf/blob/master/README.md, goes where
	// it is read: the scheme drops, and a forge's view of a file becomes
	// the file. path.Clean has made its // one /.
	if s := strings.ToLower(segs[0]); (s == "https:" || s == "http:") && len(segs) > 1 || forgeViews[strings.ToLower(segs[0])] != nil || rawHosts[strings.ToLower(segs[0])] != "" {
		rest := segs
		if strings.HasSuffix(segs[0], ":") {
			rest = segs[1:]
		}
		to := "/" + strings.Join(rest, "/")
		if u, err := url.Parse("https:/" + to); err == nil {
			if _, wp := webPath(u); wp != "" {
				to = wp
			}
		}
		if strings.TrimSuffix(to, "/") != strings.TrimSuffix(p, "/") {
			http.Redirect(rw, r, (&url.URL{Path: to}).String(), http.StatusMovedPermanently)
			return
		}
	}
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
		j, refused, wait := w.start(key, segs, visitor(r))
		if j == nil {
			logInfo("refused %s to %s: %s", key, visitor(r), refused)
			rw.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			w.render(rw, r, http.StatusTooManyRequests, "too many reads", webCrumbs(key), "error",
				"<pre class=\"error\">"+template.HTMLEscapeString(refused)+"</pre>")
			return
		}
		w.serveJob(rw, w.srv.withLive(r), j)
	}
}

// visitor is who asks, for a public server's limits: the peer's address
// or, from a proxy on loopback such as Caddy, the last address in
// X-Forwarded-For, which the proxy wrote. An IPv6 address counts by its
// /64, since one host gets a /64 whole.
func visitor(r *http.Request) netip.Prefix {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Prefix{}
	}
	a := ap.Addr().Unmap()
	if xs := r.Header.Values("X-Forwarded-For"); a.IsLoopback() && len(xs) > 0 {
		all := strings.Split(xs[len(xs)-1], ",")
		if fa, err := netip.ParseAddr(strings.TrimSpace(all[len(all)-1])); err == nil {
			a = fa.Unmap()
		}
	}
	bits := 32
	if a.Is6() {
		bits = 64
	}
	p, _ := a.Prefix(bits)
	return p
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
		w.wg.Go(func() { w.refresh(r) })
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
	t.web, t.public = r, w.public
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
	w.wg.Go(t.warm)
	w.wg.Go(t.warmLinks)
	return nil
}

// dropTree removes a worktree no page shows.
func (w *web) dropTree(r *webRepo, wt string) {
	if _, err := git(w.ctx, filepath.Join(r.dir, ".git"), "worktree", "remove", "--force", wt); err != nil {
		os.RemoveAll(wt)
		git(w.ctx, filepath.Join(r.dir, ".git"), "worktree", "prune")
	}
}

// start begins the job for the URL path key, whose segments are segs, for
// the visitor who. On a public server over who's limits it returns nil,
// why, and how long until who may start one.
func (w *web) start(key string, segs []string, who netip.Prefix) (*webJob, string, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if j := w.jobs[key]; j != nil {
		return j, "", 0
	}
	now := time.Now()
	if w.public {
		if now.Sub(w.swept) > time.Minute {
			for v := range w.visitors {
				w.recent(v, now)
			}
			w.swept = now
		}
		mine := w.recent(who, now)
		for _, o := range mine {
			if o.ended.IsZero() {
				return nil, "One read at a time from one address, and " + o.urlPath + " is still being read. Its page shows how far it is; this one can start when it ends.", 5 * time.Second
			}
		}
		if len(mine) >= visitorJobs {
			wait := mine[0].started.Add(visitorWindow).Sub(now)
			return nil, fmt.Sprintf("This address has started %d reads within %d minutes, the most a public server allows. The next can start in %d minutes. A repo already read opens at any time.",
				len(mine), int(visitorWindow.Minutes()), int(wait.Minutes())+1), wait
		}
	}
	j := &webJob{urlPath: "/" + key, started: now}
	w.jobs[key] = j
	if w.public {
		w.visitors[who] = append(w.visitors[who], j)
	}
	w.wg.Go(func() { w.run(j, key, segs) })
	return j, "", 0
}

// recent drops the jobs who started before visitorWindow that have ended,
// and returns those left, oldest first. w.mu is held.
func (w *web) recent(who netip.Prefix, now time.Time) []*webJob {
	mine := slices.DeleteFunc(w.visitors[who], func(j *webJob) bool {
		return now.Sub(j.started) > visitorWindow && !j.ended.IsZero()
	})
	if len(mine) == 0 {
		delete(w.visitors, who)
		return nil
	}
	w.visitors[who] = mine
	return mine
}

// step records what the job does now and wakes its page.
func (w *web) step(j *webJob, s string) {
	s = w.hide(s)
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
		line = w.hide(line)
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
	j.err, j.ended = w.hide(failed), time.Now()
	if err == nil {
		delete(w.jobs, strings.TrimPrefix(j.urlPath, "/"))
	}
	w.mu.Unlock()
	w.kick(true)
}

// run finds the repo the path lies in and clones it, or lists the path
// when it names no repo.
func (w *web) run(j *webJob, key string, segs []string) {
	if publicNet.Load() {
		if _, err := reach(w.ctx, segs[0]); err != nil {
			w.end(j, err)
			return
		}
	}
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

// where writes a path in the cache for a page: with ~, or on a public
// server from the cache's root, which shows nothing of the machine.
func (w *web) where(p string) string {
	if w.public {
		return w.hide(p)
	}
	return tildePath(p)
}

// hide writes the cache as "cache" in text a public server's page shows,
// eg git's error, which names the paths it was given.
func (w *web) hide(s string) string {
	if !w.public {
		return s
	}
	return strings.ReplaceAll(s, w.root, "cache")
}

// tildePath writes a path below home with ~.
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
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
