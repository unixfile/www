package www

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// cachedRepo is a repo found on disk in the cache.
type cachedRepo struct {
	key  string
	size int64
	used time.Time
}

// eachRepo calls fn for every repo in the cache, with its key and its
// directory. It reads directory names alone, never a repo's files.
func (w *web) eachRepo(fn func(key, dir string)) {
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
		fn(filepath.ToSlash(key), p)
		return nil
	})
}

// onDiskKeys names every repo in the cache. It costs a walk of the
// cache's directories, where onDisk reads every file's size.
func (w *web) onDiskKeys() []string {
	var out []string
	w.eachRepo(func(key, _ string) { out = append(out, key) })
	return out
}

// onDisk is every repo in the cache with its disk use and its last
// request. Measuring reads every file of every repo, which takes a minute
// on a full cache of some GiB, so only a caller that shows or acts on
// sizes asks for it.
func (w *web) onDisk() []cachedRepo {
	var out []cachedRepo
	w.eachRepo(func(key, p string) {
		c := cachedRepo{key: key, size: diskUse(filepath.Join(p, ".git"))}
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
	})
	return out
}

// diskUse is the disk space below dir. It is a variable so a test can
// tell when a page measures the cache.
var diskUse = func(dir string) int64 {
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
// limit. A repo in use, fetched here or locked by another server, stays.
// keep, just fetched, goes last, and only when it alone is over the limit;
// trim then says so.
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
		if c.key == keep && c.size <= w.limit {
			// Repos in use kept the cache over its limit; a later trim
			// drops them.
			logInfo("the cache stays over %s while repos in use fill it", sizeText(w.limit))
			return nil
		}
		if c.key == keep {
			// The caller holds keep's lock.
			w.remove(c.key)
			return &errno{syscall.EFBIG, fmt.Sprintf("%s takes %s, over the cache's limit of %s, so it was dropped; --disk sets the limit",
				c.key, sizeText(c.size), sizeText(w.limit))}
		}
		if !w.drop(c.key) {
			logInfo("keeps %s, %s, though the cache is over %s, since another server is fetching it", c.key, sizeText(c.size), sizeText(w.limit))
			continue
		}
		logInfo("dropped %s, %s, last requested %s, since the cache is over %s", c.key, sizeText(c.size), c.used.Format(time.DateTime), sizeText(w.limit))
		total -= c.size
	}
	return nil
}

// drop stops serving the repo key and deletes it from the cache, unless
// another server holds its lock, and reports whether it did. The servers
// on every address share the cache, and each knows only its own busy
// repos.
func (w *web) drop(key string) bool {
	dir := filepath.Join(w.root, filepath.FromSlash(key))
	unlock, err := flockDir(dir, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false
	}
	if err == nil {
		defer unlock()
	}
	w.remove(key)
	return true
}

// remove stops serving the repo key and deletes it from the cache. The
// caller holds its lock.
func (w *web) remove(key string) {
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
