package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

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
		if w.slots != nil {
			select {
			case w.slots <- struct{}{}:
			default:
				w.step(j, fmt.Sprintf("waiting: %d clones run at once", cap(w.slots)))
				select {
				case w.slots <- struct{}{}:
				case <-w.ctx.Done():
					return w.ctx.Err()
				}
			}
			// The slot covers the worktree's blobs too, the larger fetch.
			defer func() { <-w.slots }()
		}
		tmp := bare + ".tmp"
		os.RemoveAll(tmp)
		w.step(j, "git clone --bare --filter=blob:none "+cloneURL+" "+w.where(bare))
		start := time.Now()
		ctx, stop := w.bounded(dir)
		_, err := gitProgress(ctx, dir, w.progress(j), "clone", "--bare", "--filter=blob:none", "--progress", "--", cloneURL, tmp)
		err = boundErr(ctx, err)
		stop()
		if err != nil {
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
		step("git worktree add --detach " + w.where(wt) + " " + head[:12] + ", which fetches its files")
		start := time.Now()
		ctx, stop := w.bounded(r.dir)
		_, err := gitProgress(ctx, bare, progress, "worktree", "add", "--detach", "--", wt, head)
		err = boundErr(ctx, err)
		stop()
		if err != nil {
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
	ctx, stop := w.bounded(r.dir)
	_, err = git(ctx, bare, "fetch", "--prune", "--no-write-fetch-head", "origin",
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	err = boundErr(ctx, err)
	stop()
	if err != nil {
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

// fetchWait bounds a public server's clone, fetch or checkout, so a forge
// that sends slowly cannot hold a clone's place for good.
const fetchWait = 10 * time.Minute

// bounded is the context for git fetching into the repo directory dir.
// On a public server it ends after fetchWait, or once dir outgrows the
// cache's limit: trim runs only after a fetch, and a stranger's forge may
// send without end.
func (w *web) bounded(dir string) (context.Context, func()) {
	if !w.public {
		return w.ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(w.ctx)
	ctx, cancelWait := context.WithTimeoutCause(ctx, fetchWait,
		&errno{syscall.ETIMEDOUT, fmt.Sprintf("the forge took over %d minutes, the most a public server waits", int(fetchWait.Minutes()))})
	done := make(chan struct{})
	w.wg.Go(func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if n := diskUse(dir); n > w.limit {
					cancel(&errno{syscall.EFBIG, fmt.Sprintf("the repo grew past %s, the cache's limit, so the fetch stopped; --disk sets the limit", sizeText(w.limit))})
					return
				}
			}
		}
	})
	return ctx, func() {
		close(done)
		cancelWait()
		cancel(nil)
	}
}

// boundErr is why git failed in a context from bounded: the limit it hit,
// or err.
func boundErr(ctx context.Context, err error) error {
	if err != nil && ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil {
			var e *errno
			if errors.As(cause, &e) {
				return cause
			}
		}
	}
	return err
}

// lockRepo marks the repo key busy and takes its lock.
func (w *web) lockRepo(key, dir string) (func(), error) {
	w.mu.Lock()
	w.busy[key] = true
	w.mu.Unlock()
	unlock, err := flockDir(dir, syscall.LOCK_EX)
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
// fetching one repo at once, and one from deleting a repo another fetches.
// how is LOCK_EX, or LOCK_EX|LOCK_NB to fail with EWOULDBLOCK when held.
func flockDir(dir string, how int) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := flock(f, how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
