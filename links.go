package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// followMarker marks a directory that links out of a tree may reach. lord
// keeps csi and .claude in a store outside the repo; one marker in the
// store root opens them all.
const followMarker = ".mdserve-follow"

// treeFS opens a tree's files through its os.Root, which follows a link
// only while it stays inside the tree and never an absolute one. What the
// root refuses is resolved again: an absolute link back into the tree is
// served from the tree, and a link out of it is served when a marker lies
// at or above its target and other users could read the target. A shared
// tree also refuses whatever other users could not read inside it.
type treeFS struct{ t *tree }

func (f treeFS) Open(name string) (fs.File, error) {
	// A shared tree hides its locks, which name the tree's path.
	if f.t.shared && isLockName(path.Base(name)) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	file, err := f.open(name)
	if err != nil || !f.t.shared {
		return file, err
	}
	if ref := f.t.private(name); ref != nil {
		file.Close()
		return nil, ref
	}
	return file, nil
}

func (f treeFS) open(name string) (fs.File, error) {
	file, err := f.t.root.FS().Open(name)
	if err == nil || !fs.ValidPath(name) {
		return file, err
	}
	real, rerr := filepath.EvalSymlinks(filepath.Join(f.t.dir, filepath.FromSlash(name)))
	if rerr != nil {
		return nil, err
	}
	if within(real, f.t.realDir) {
		rel, rerr := filepath.Rel(f.t.realDir, real)
		if rerr != nil || filepath.ToSlash(rel) == name {
			return nil, err
		}
		return f.t.root.FS().Open(filepath.ToSlash(rel))
	}
	marker, ref := f.t.follow(name, real)
	if ref != nil {
		return nil, ref
	}
	root, err := os.OpenRoot(marker)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel, err := filepath.Rel(marker, real)
	if err != nil {
		return nil, err
	}
	return root.Open(rel)
}

// refusal says why a link out of the tree is not followed: no marker lies
// at or above the target, or other users cannot read path.
type refusal struct {
	link    string // the link in the tree, relative to its root
	target  string // where the link leads
	dir     bool   // the target is a directory
	private bool
	path    string      // what other users cannot read
	mode    fs.FileMode // its permission bits
}

func (r *refusal) Error() string {
	msg := r.link + " links outside the tree to\n  " + r.target + "\n"
	if !r.private {
		where := "the target or a directory above it"
		if !r.dir {
			where = "the target's directory or one above it"
		}
		return msg + "mdserve follows such links only into a directory that holds " +
			followMarker + ", or below one. Create it in " + where + "."
	}
	what := "it"
	if r.path != r.target {
		what = r.path
	}
	return msg + fmt.Sprintf("Other users cannot read %s (mode %03o), so mdserve does not serve it.", what, r.mode.Perm())
}

// follow decides whether name, which resolves to real outside the tree,
// may be served. It returns the directory holding the marker, or why not.
func (t *tree) follow(name, real string) (string, *refusal) {
	ref := &refusal{}
	ref.link, ref.target = t.outLink(name)
	if info, err := os.Stat(ref.target); err == nil {
		ref.dir = info.IsDir()
	}
	marker := findMarker(real)
	if marker == "" {
		return "", ref
	}
	if p, mode, ok := othersRead(marker, real); !ok {
		ref.private, ref.path, ref.mode = true, p, mode
		return "", ref
	}
	return marker, nil
}

// outLink finds the first link on the way to name that leads out of the
// tree, and where it leads.
func (t *tree) outLink(name string) (link, target string) {
	parts := strings.Split(name, "/")
	for i := range parts {
		p := path.Join(parts[:i+1]...)
		real, err := filepath.EvalSymlinks(filepath.Join(t.dir, filepath.FromSlash(p)))
		if err == nil && !within(real, t.realDir) {
			return p, real
		}
	}
	return name, ""
}

// findMarker returns the nearest directory at or above real that holds the
// marker, or "".
func findMarker(real string) string {
	dir := real
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		dir = filepath.Dir(real)
	}
	for {
		if info, err := os.Lstat(filepath.Join(dir, followMarker)); err == nil && info.Mode().IsRegular() {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
}

// othersRead reports whether other users could read real, and every
// directory from the marker down to it: a file needs the others-read bit, a
// directory others-read and others-execute. Otherwise it returns the first
// path from the marker down that fails, with its mode.
func othersRead(marker, real string) (string, fs.FileMode, bool) {
	rel, err := filepath.Rel(marker, real)
	if err != nil {
		return real, 0, false
	}
	p := marker
	steps := []string{p}
	if rel != "." {
		for _, s := range strings.Split(rel, string(filepath.Separator)) {
			p = filepath.Join(p, s)
			steps = append(steps, p)
		}
	}
	for _, p := range steps {
		info, err := os.Stat(p)
		if err != nil {
			return p, 0, false
		}
		need := fs.FileMode(0o004)
		if info.IsDir() {
			need = 0o005
		}
		if info.Mode().Perm()&need != need {
			return p, info.Mode(), false
		}
	}
	return "", 0, true
}

// private refuses name in a shared tree when other users could not read
// it, or could not pass a directory on the way. Each step of name is
// checked where it resolves, from the tree's root or from the marker
// above a link's target.
func (t *tree) private(name string) *refusal {
	parts := []string{"."}
	if name != "." {
		parts = strings.Split(name, "/")
	}
	for i := range parts {
		p := path.Join(parts[:i+1]...)
		real, err := filepath.EvalSymlinks(filepath.Join(t.dir, filepath.FromSlash(p)))
		if err != nil {
			return &refusal{link: name, private: true}
		}
		base := t.realDir
		if !within(real, t.realDir) {
			if base = findMarker(real); base == "" {
				return &refusal{link: name, target: real}
			}
		}
		if bad, mode, ok := othersRead(base, real); !ok {
			ref := &refusal{link: name, target: real, private: true, path: bad, mode: mode}
			if info, err := os.Stat(real); err == nil && p == name {
				ref.dir = info.IsDir()
			}
			return ref
		}
	}
	return nil
}

// refused reports why name cannot be opened, or nil when it can or fails
// for another reason.
func (t *tree) refused(name string) *refusal {
	f, err := t.fsys.Open(name)
	if err == nil {
		f.Close()
		return nil
	}
	var ref *refusal
	if errors.As(err, &ref) {
		return ref
	}
	return nil
}
