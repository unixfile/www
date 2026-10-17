package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
)

// repo is the git repo a directory lies in, as git found it, or git's
// complaint when it lies in none.
type repo struct {
	gitDir, common, top string
	complaint           string
}

// repoAt asks git once which repo dir lies in, so git's own checks, such as
// safe.directory, decide. The answer is kept until a .git appears or goes
// where it would change: between dir and the repo's top, or above dir when
// dir lies in no repo.
func repoAt(ctx context.Context, dir string) repo {
	key := "repo\x00" + dir
	if v, ok := gitCache.get(key); ok {
		if r := v.(repo); r.holds(dir) {
			return r
		}
		gitCache.drop(key)
	}
	var r repo
	out, err := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--show-toplevel")
	if err != nil {
		// Inside a git directory there is no work tree to name.
		out, err = git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	}
	if err != nil {
		r.complaint = string(out) + err.Error()
	} else {
		f := strings.Split(strings.TrimSpace(string(out)), "\n")
		r.gitDir, r.common = f[0], f[1]
		if len(f) > 2 {
			r.top = f[2]
		}
	}
	gitCache.put(key, r, int64(len(r.gitDir)+len(r.common)+len(r.top)+len(r.complaint)))
	return r
}

// holds reports whether r still answers for dir: its git directory is
// there, and no .git has appeared or gone in the levels that decide.
func (r repo) holds(dir string) bool {
	if r.complaint != "" {
		for d := dir; ; d = filepath.Dir(d) {
			if exists(filepath.Join(d, ".git")) {
				return false
			}
			if d == filepath.Dir(d) {
				return true
			}
		}
	}
	if !exists(filepath.Join(r.gitDir, "HEAD")) {
		return false
	}
	if r.top == "" {
		return true
	}
	for d := dir; d != r.top; d = filepath.Dir(d) {
		if exists(filepath.Join(d, ".git")) || d == filepath.Dir(d) {
			return false
		}
	}
	return exists(filepath.Join(r.top, ".git"))
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// head names the commit HEAD points at. It reads the repo's files, which
// takes microseconds where git takes a millisecond, and asks git only for
// what it cannot read: a reftable store or a ref it does not find.
func (r repo) head(ctx context.Context) string {
	if h, ok := r.readHead(); ok {
		return h
	}
	out, err := git(ctx, r.gitDir, "rev-parse", "HEAD")
	if err != nil {
		return "no HEAD: " + err.Error()
	}
	return strings.TrimSpace(string(out))
}

func (r repo) readHead() (string, bool) {
	if exists(filepath.Join(r.common, "reftable")) {
		return "", false
	}
	ref := "HEAD"
	for range 5 {
		val, ok := r.readRef(ref)
		if !ok {
			return "", false
		}
		target, symbolic := strings.CutPrefix(val, "ref: ")
		if !symbolic {
			return val, isHash(val)
		}
		ref = target
	}
	return "", false
}

// readRef reads a ref from the worktree's git directory, then the common
// one, then packed-refs.
func (r repo) readRef(ref string) (string, bool) {
	for _, d := range []string{r.gitDir, r.common} {
		if b, err := os.ReadFile(filepath.Join(d, filepath.FromSlash(ref))); err == nil {
			return strings.TrimSpace(string(b)), true
		}
	}
	f, err := os.Open(filepath.Join(r.common, "packed-refs"))
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		hash, name, ok := strings.Cut(sc.Text(), " ")
		if ok && name == ref {
			return hash, true
		}
	}
	return "", false
}

func isHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
