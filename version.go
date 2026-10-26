package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A version is a tree as it was in one commit, served below the tree's
// URL at @ and the commit's stardate, eg /scratch/www/@261024.4/doc/, or
// its hash when it has none. The version is a path segment rather than a
// query, so relative links in its docs, listings and breadcrumbs stay in
// that version. A stardate always holds a dot, which no hash does.
var atSegment = regexp.MustCompile(`^@([0-9a-f]{4,40}|[0-9]{6}(?:\.[0-9]+)+)$`)

// stardateRev matches a stardate, eg 261024.4 or 261024.4.2, or a day and
// x, eg 261026.x, as the name of a commit.
var stardateRev = regexp.MustCompile(`^[0-9]{6}\.(?:[0-9]+|x)(?:\.[0-9]+)*$`)

// isDay reports whether s is a date as a stardate writes it, eg 261024.
func isDay(s string) bool {
	_, err := time.Parse("060102", s)
	return len(s) == 6 && err == nil
}

// findCommit is the commit rev names in the repo dir lies in, and that
// repo's stardates. rev is a stardate, a day and x that one commit alone
// holds while it waits for a push, or anything git rev-parse takes.
func findCommit(ctx context.Context, dir, rev string) (commit, *stardates, error) {
	stars, spec := stardatesIn(ctx, dir), rev
	missing := &errno{syscall.ENOENT, "no commit " + rev + " in this repo"}
	switch {
	case isDay(rev):
		// Six digits are hex too, but a reader who types a date means
		// the day, which holds many commits.
		return commit{}, stars, &errno{syscall.EINVAL, rev + " is a day, not a commit; add its place, eg " + rev + ".1"}
	case strings.HasPrefix(rev, "-"):
		// git would read it as an option.
		return commit{}, stars, missing
	case stardateRev.MatchString(rev) && strings.Contains(rev, ".x"):
		var held []string
		if stars != nil {
			for h, d := range stars.pending {
				if d == rev {
					held = append(held, h)
				}
			}
		}
		switch len(held) {
		case 0:
			return commit{}, stars, &errno{syscall.ENOENT, "no commit " + rev + " waits for a push in this repo"}
		case 1:
			spec = held[0]
		default:
			return commit{}, stars, &errno{syscall.EINVAL, fmt.Sprintf("%s names %d commits until they are pushed; give a hash", rev, len(held))}
		}
	case stardateRev.MatchString(rev):
		if stars == nil || stars.byDate[rev] == "" {
			return commit{}, stars, &errno{syscall.ENOENT, "no commit " + rev + " on this repo's main line"}
		}
		spec = stars.byDate[rev]
	}
	out, err := git(ctx, dir, "log", "-1", "--no-show-signature", "-z", "--format="+commitFormat, spec+"^{commit}", "--")
	if err != nil {
		return commit{}, stars, missing
	}
	cs := parseCommits(out)
	if len(cs) == 0 {
		return commit{}, stars, missing
	}
	return cs[0], stars, nil
}

// atVersion is the tree t as it was in the commit seg names, eg @261024.4
// or @5430ac3, and the segment that names it best: its stardate, else
// git's short hash. A commit never changes, so each is built once per
// tree and name.
func (t *tree) atVersion(ctx context.Context, seg string) (*tree, string, error) {
	m := atSegment.FindStringSubmatch(seg)
	if m == nil {
		return nil, "", fs.ErrNotExist
	}
	c, stars, err := findCommit(ctx, t.dir, m[1])
	if err != nil {
		return nil, "", err
	}
	if t.shared {
		c.body = dropTrailers(c.body)
	}
	best := "@" + stars.addr(&c)
	if v, ok := t.versions.Load(best); ok {
		return v.(*tree), best, nil
	}
	v := &tree{dir: t.dir, realDir: t.realDir, prefix: t.prefix + best + "/", label: t.label, wd: -1, at: &c, atLabel: stars.label(&c), base: t}
	v.fsys = commitFS{dir: t.dir, rev: c.hash, when: c.date}
	got, _ := t.versions.LoadOrStore(best, v)
	return got.(*tree), best, nil
}

// serveVersion serves a path below a version of t; first is the path's
// first segment, eg @261024.4. A hash of a commit with a stardate, or a
// longer or shorter hash than git's own, redirects to the best name. A share has no versions, as it shows no history.
func (t *tree) serveVersion(w http.ResponseWriter, r *http.Request, first string) {
	name := t.name(r.URL.Path)
	v, seg, err := t.atVersion(r.Context(), first)
	if err != nil {
		t.fail(w, r, name, http.StatusNotFound, err.Error())
		return
	}
	if seg != first {
		u := t.prefix + seg + strings.TrimPrefix(r.URL.EscapedPath(), t.prefix+first)
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, u, http.StatusMovedPermanently)
		return
	}
	v.serve(w, r)
}

// versionLine is the top line of a page in a version: the page's last
// commit as of the version, a link to that commit's page, and the menu of
// the page's versions, whose first row leads to the page as it is now. A version reached through a commit that left
// the page alone, eg by browsing a directory's history, names the commit
// that last changed the page, so the menu can mark it.
func (t *tree) versionLine(ctx context.Context, name string, dir bool) string {
	rest := below(name, dir)
	c := t.base.lastAt(ctx, name, t.at)
	now := (&url.URL{Path: t.base.prefix + rest}).String()
	show := (&url.URL{Path: t.base.prefix + rest, RawQuery: "commit=" + c.hash}).String()
	stars := stardatesIn(ctx, t.dir)
	return topLine(c, stars.label(c), "", now, t.base.versionURL(stars, c, rest), now, show)
}

// lastAt is the newest commit up to at that touched name, a path below
// t, or at itself if git cannot say. A commit never changes, so the
// answer is kept under at's hash.
func (t *tree) lastAt(ctx context.Context, name string, at *commit) *commit {
	spec := "./"
	if name != "." {
		spec = "./" + name
	}
	key := "lastAt\x00" + t.dir + "\x00" + at.hash + "\x00" + spec
	if v, ok := gitCache.get(key); ok {
		return v.(*commit)
	}
	out, err := git(ctx, t.dir, "log", "--no-show-signature", "-z", "--format="+commitFormat, "-1", at.hash, "--", spec)
	if err != nil {
		return at
	}
	c := at
	if cs := t.commits(out); len(cs) > 0 {
		c = &cs[0]
	}
	gitCache.put(key, c, int64(len(c.hash)+len(c.short)+len(c.subject)+len(c.body))+64)
	return c
}

// below is the page at name as a path below its tree's URL: "" for the
// root, and a directory's ends in a slash.
func below(name string, dir bool) string {
	switch {
	case name == ".":
		return ""
	case dir:
		return name + "/"
	}
	return name
}

// hasVersions reports whether t serves its versions at @: a tree as it is
// now, on loopback. A repo on the web address has them too: its clone
// holds every commit and tree, and git fetches the old files a version
// shows from the forge as a page needs them.
func (t *tree) hasVersions() bool {
	return t.at == nil && !t.shared
}

// historyInVersions reports whether the history of the page at name is
// the one t's versions show. Versions read the repo t lies in, at name's
// own path, while a history follows links and runs git where the page's
// file lies, which may be another repo: one nested in t, or one of many
// in a tree that lies in none.
func (t *tree) historyInVersions(ctx context.Context, name string) bool {
	if !t.hasVersions() {
		return false
	}
	dir, spec, err := t.gitPlace(name)
	if err != nil || filepath.Join(dir, spec) != filepath.Join(t.realDir, filepath.FromSlash(name)) {
		return false
	}
	here, there := repoAt(ctx, t.dir), repoAt(ctx, dir)
	return here.complaint == "" && there.complaint == "" && here.gitDir == there.gitDir
}

// versionURL is the URL of rest, a path below t, in commit c's version,
// named by its stardate in s or else its short hash.
func (t *tree) versionURL(s *stardates, c *commit, rest string) string {
	return (&url.URL{Path: t.prefix + "@" + s.addr(c) + "/" + rest}).String()
}

// commitFS reads a directory as it was in one commit: listings from git
// ls-tree, files from git cat-file. dir is where git runs, so paths are
// relative to it, as the tree's are.
type commitFS struct {
	dir, rev string
	when     time.Time
}

// gitEntry is one name in a listing from git ls-tree.
type gitEntry struct {
	name, typ, obj string
	mode           uint32
	size           int64
}

// list is the directory name as it was, by git ls-tree, kept in the git
// cache under the commit, which never changes.
func (c commitFS) list(name string) ([]gitEntry, error) {
	key := "ls\x00" + c.dir + "\x00" + c.rev + "\x00" + name
	if v, ok := gitCache.get(key); ok {
		return v.([]gitEntry), nil
	}
	spec := "./"
	if name != "." {
		spec = "./" + name + "/"
	}
	out, err := git(context.Background(), c.dir, "ls-tree", "-l", "-z", c.rev, "--", spec)
	if err != nil {
		return nil, err
	}
	var entries []gitEntry
	size := int64(0)
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 {
			continue
		}
		mode, _ := strconv.ParseUint(f[0], 8, 32)
		n, _ := strconv.ParseInt(f[3], 10, 64)
		e := gitEntry{name: path.Base(p), typ: f[1], obj: f[2], mode: uint32(mode), size: n}
		entries = append(entries, e)
		size += int64(len(e.name)+len(e.obj)) + 48
	}
	gitCache.put(key, entries, size)
	return entries, nil
}

// entry finds name in its directory's listing.
func (c commitFS) entry(name string) (gitEntry, error) {
	entries, err := c.list(path.Dir(name))
	if err != nil {
		return gitEntry{}, err
	}
	base := path.Base(name)
	for _, e := range entries {
		if e.name == base {
			return e, nil
		}
	}
	return gitEntry{}, fs.ErrNotExist
}

// blob reads a file's content by its object, kept in the git cache.
func (c commitFS) blob(obj string) ([]byte, error) {
	key := "blob\x00" + c.dir + "\x00" + obj
	if v, ok := gitCache.get(key); ok {
		return v.([]byte), nil
	}
	b, err := git(context.Background(), c.dir, "cat-file", "blob", obj)
	if err != nil {
		return nil, err
	}
	gitCache.put(key, b, int64(len(b)))
	return b, nil
}

func (c commitFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return &commitDir{fs: c, info: commitInfo{name: ".", dir: true, when: c.when}}, nil
	}
	e, err := c.entry(name)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	info := c.info(e)
	if info.dir {
		return &commitDir{fs: c, path: name, info: info}, nil
	}
	b, err := c.blob(e.obj)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return &commitFile{Reader: bytes.NewReader(b), info: info}, nil
}

// info describes an entry; a submodule shows as an empty directory.
func (c commitFS) info(e gitEntry) commitInfo {
	return commitInfo{name: e.name, size: e.size, dir: e.typ == "tree" || e.typ == "commit", when: c.when}
}

type commitInfo struct {
	name string
	size int64
	dir  bool
	when time.Time
}

func (i commitInfo) Name() string       { return i.name }
func (i commitInfo) Size() int64        { return i.size }
func (i commitInfo) ModTime() time.Time { return i.when }
func (i commitInfo) IsDir() bool        { return i.dir }
func (i commitInfo) Sys() any           { return nil }
func (i commitInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i commitInfo) Type() fs.FileMode          { return i.Mode().Type() }
func (i commitInfo) Info() (fs.FileInfo, error) { return i, nil }

// commitFile is a file's content, seekable, as http.ServeContent needs.
type commitFile struct {
	*bytes.Reader
	info commitInfo
}

func (f *commitFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *commitFile) Close() error               { return nil }

type commitDir struct {
	fs   commitFS
	path string
	info commitInfo
	read bool
}

func (d *commitDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *commitDir) Close() error               { return nil }
func (d *commitDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.path, Err: errors.New("is a directory")}
}

func (d *commitDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if d.read {
		if n > 0 {
			return nil, io.EOF
		}
		return nil, nil
	}
	d.read = true
	name := d.path
	if name == "" {
		name = "."
	}
	entries, err := d.fs.list(name)
	if err != nil {
		return nil, err
	}
	out := make([]fs.DirEntry, len(entries))
	for i, e := range entries {
		out[i] = d.fs.info(e)
	}
	return out, nil
}
