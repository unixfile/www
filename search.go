package www

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// maxNameHits and maxLineHits bound each level of a search, so one
	// level cannot use up the answer; maxSearchRows bounds the whole.
	maxNameHits   = 30
	maxLineHits   = 100
	maxSearchRows = 300
	// maxFileLines is how many lines of one file a search shows; the rest
	// are counted.
	maxFileLines = 3
	// rowLead is how far along a result's row, path included, a match may
	// start before the line is cut; leadBefore is how much of the line
	// stays before a match once it is.
	rowLead    = 50
	leadBefore = 16
	// maxSearchFile skips larger files for content, as a code page does.
	maxSearchFile = maxCode
	// searchBudget bounds one search, since a tree may hold many files; the
	// answer says when it stopped early.
	searchBudget = 500 * time.Millisecond
	// minContentQuery is the shortest query searched in contents; one
	// letter matches nearly every line.
	minContentQuery = 2
)

// unsearched names the hidden directories a search leaves out: stores
// that machines write, not people, which would swamp the answer. Other
// dot entries, eg .gitignore, .github or .vscode, are searched as the
// listing shows them.
var unsearched = map[string]bool{
	// version control
	".git": true, ".hg": true, ".svn": true, ".jj": true, ".bzr": true,
	// environments and caches
	".venv": true, ".tox": true, ".cache": true, ".mypy_cache": true,
	".pytest_cache": true, ".ruff_cache": true, ".gradle": true, ".next": true,
}

// hit is one search result: a name that matched, or a line that did.
type hit struct {
	Href string `json:"href"`
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
	More int    `json:"more,omitempty"` // this file's further matching lines, on its last row

	Depth   int  `json:"depth,omitempty"`   // levels below the searched directory
	Ignored bool `json:"ignored,omitempty"` // git ignores it
}

type found struct {
	Names []hit `json:"names"`
	Lines []hit `json:"lines"`
	Files int   `json:"files"` // files searched for content
	Cut   bool  `json:"cut"`   // stopped at the time budget or the hit limits
	// Web reads the query on the web address, when it is a forge's URL.
	Web *hit `json:"web,omitempty"`
}

// serveSearch answers ?search=q below name: names that hold q, and lines
// that hold it, ignoring case, nearest first. Each hit's Path is relative
// to name. It reads the tree through its
// own file system, so a share searches only what it would serve. The
// directories in unsearched are skipped.
func (t *tree) serveSearch(w http.ResponseWriter, r *http.Request, name, q string) {
	ctx, cancel := context.WithTimeout(r.Context(), searchBudget)
	defer cancel()
	res := t.search(ctx, name, q)
	res.Web = t.webHit(q)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(res)
}

// webHit reads the query on the web address when it is a link to a repo
// on a forge, or a file in one, with or without its scheme, eg
// github.com/junegunn/fzf.
func (t *tree) webHit(q string) *hit {
	base := t.webBase()
	q = strings.TrimSpace(q)
	if base == nil || strings.ContainsAny(q, " \t") {
		return nil
	}
	if !strings.Contains(q, "://") {
		q = "https://" + q
	}
	u, err := url.Parse(q)
	if err != nil {
		return nil
	}
	_, p := webPath(u)
	if p == "" {
		return nil
	}
	return &hit{Href: *base + (&url.URL{Path: p, Fragment: u.Fragment}).String(), Path: strings.TrimPrefix(p, "/")}
}

// search answers q below dir, level by level: dir's own entries, then what
// lies one directory down, and so on, so a deep tree cannot crowd out a
// shallow one. What git ignores waits until every other entry is done.
func (t *tree) search(ctx context.Context, dir, q string) found {
	lq := strings.ToLower(strings.TrimSpace(q))
	s := searcher{t: t, ctx: ctx, dir: dir, lq: lq, needle: []byte(lq), contents: len(lq) >= minContentQuery,
		ignored: map[string]bool{}, res: found{Names: []hit{}, Lines: []hit{}}}
	if lq == "" {
		return s.res
	}
	entries, err := fs.ReadDir(t.fsys, dir)
	if err != nil {
		s.res.Cut = true
		return s.res
	}
	s.gitIgnored(dir)
	s.walk(s.listed(dir, entries))
	if !s.done() {
		first := s.deferred
		s.deferred, s.ignored, s.ignoring = nil, nil, true
		s.walk(first)
	}
	return s.res
}

// searcher holds one search's state as it walks.
type searcher struct {
	t        *tree
	ctx      context.Context
	dir      string
	lq       string
	needle   []byte
	contents bool
	res      found
	ignored  map[string]bool // paths git ignores, by their path in the tree
	deferred []searchEntry   // ignored entries met so far
	ignoring bool            // walking the deferred entries
	depth    int
	names    int // name hits at this level
	lines    int // line hits at this level
}

type searchEntry struct {
	path string
	d    fs.DirEntry
}

// done reports whether the search must stop, and says so in its answer.
func (s *searcher) done() bool {
	if s.ctx.Err() != nil || len(s.res.Names)+len(s.res.Lines) >= maxSearchRows {
		s.res.Cut = true
		return true
	}
	return false
}

// walk visits entries, then the entries of the directories among them,
// and so on down. An entry git ignores goes to deferred.
func (s *searcher) walk(level []searchEntry) {
	for s.depth = 0; len(level) > 0; s.depth++ {
		s.names, s.lines = 0, 0
		var next []searchEntry
		for _, e := range level {
			if s.done() {
				return
			}
			if s.ignored[e.path] {
				s.deferred = append(s.deferred, e)
				continue
			}
			s.visit(e.path, e.d)
			if e.d.IsDir() {
				if entries, err := fs.ReadDir(s.t.fsys, e.path); err == nil {
					// A repo inside the tree has its own ignores.
					if !s.ignoring && slices.ContainsFunc(entries, func(d fs.DirEntry) bool { return d.Name() == ".git" }) {
						s.gitIgnored(e.path)
					}
					next = append(next, s.listed(e.path, entries)...)
				}
			}
		}
		level = next
	}
}

// listed keeps what a search covers. What a listing leaves out, search
// leaves out too: a share hides dot entries, though its file system lists
// them.
func (s *searcher) listed(dir string, entries []fs.DirEntry) []searchEntry {
	var out []searchEntry
	for _, d := range entries {
		if s.t.unlisted(dir, d.Name()) || d.IsDir() && unsearched[d.Name()] {
			continue
		}
		out = append(out, searchEntry{path.Join(dir, d.Name()), d})
	}
	return out
}

// gitIgnored adds what git ignores below dir. A directory that is itself
// ignored comes back as "./": the reader searching there asked for it, so
// nothing in it counts as ignored. A version has no ignored files, and a
// clean clone on the web address none worth a process, so neither asks.
func (s *searcher) gitIgnored(dir string) {
	if s.t.at != nil || s.t.web != nil {
		return
	}
	paths, ok := s.t.ignores.get(dir)
	if !ok {
		out, err := git(s.ctx, filepath.Join(s.t.dir, filepath.FromSlash(dir)), "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
		if err != nil && s.ctx.Err() != nil {
			return
		}
		// A directory outside any repo fails too, and keeps that answer.
		if err == nil {
			for _, p := range strings.Split(string(out), "\x00") {
				if p = strings.TrimSuffix(p, "/"); p != "" && p != "." {
					paths = append(paths, p)
				}
			}
		}
		s.t.ignores.put(dir, paths)
	}
	for _, p := range paths {
		s.ignored[path.Join(dir, p)] = true
	}
}

// ignoredTTL is how long a tree keeps git's answer for a directory. Each
// keystroke of a query searches again, and asking git each time starts a
// process for every repo the walk meets. A file git ignores that appears
// meanwhile is searched as if it were not ignored until the answer expires.
const ignoredTTL = 10 * time.Second

// ignoredCache keeps, by directory, what git ignores below it, relative to
// it.
type ignoredCache struct {
	mu   sync.Mutex
	dirs map[string]ignoredDir
}

type ignoredDir struct {
	at    time.Time
	paths []string
}

func (c *ignoredCache) get(dir string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.dirs[dir]
	if !ok || time.Since(d.at) >= ignoredTTL {
		return nil, false
	}
	return d.paths, true
}

// put keeps paths for dir and drops the answers that have expired.
func (c *ignoredCache) put(dir string, paths []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirs == nil {
		c.dirs = map[string]ignoredDir{}
	}
	for k, d := range c.dirs {
		if time.Since(d.at) >= ignoredTTL {
			delete(c.dirs, k)
		}
	}
	c.dirs[dir] = ignoredDir{time.Now(), paths}
}

func (s *searcher) visit(p string, d fs.DirEntry) {
	t := s.t
	rel := p
	if s.dir != "." {
		rel = strings.TrimPrefix(p, s.dir+"/")
	}
	if strings.Contains(strings.ToLower(d.Name()), s.lq) {
		if s.names == maxNameHits {
			s.res.Cut = true
		} else {
			s.names++
			label, full := rel, p
			if d.IsDir() {
				label += "/"
				full += "/"
			}
			s.res.Names = append(s.res.Names, hit{Href: t.href(full, 0), Path: label, Depth: s.depth, Ignored: s.ignoring})
		}
	}
	if d.IsDir() || !s.contents {
		return
	}
	if s.lines == maxLineHits {
		s.res.Cut = true
		return
	}
	if info, err := d.Info(); err != nil || !info.Mode().IsRegular() || info.Size() > maxSearchFile {
		return
	}
	b, err := fs.ReadFile(t.fsys, p)
	if err != nil || !isText(b) {
		return
	}
	s.res.Files++
	lower := bytes.ToLower(b)
	if !bytes.Contains(lower, s.needle) {
		return
	}
	shown, more := 0, 0
	for i, line := range bytes.Split(lower, []byte("\n")) {
		if !bytes.Contains(line, s.needle) {
			continue
		}
		if shown == maxFileLines || s.lines == maxLineHits {
			more++
			continue
		}
		shown++
		s.lines++
		// Lowering may change a line's length outside ASCII but keeps
		// its newlines, so the line is cut from the original by number.
		s.res.Lines = append(s.res.Lines, hit{Href: t.href(p, i+1), Path: rel, Line: i + 1, Text: around(lineAt(b, i), s.lq, len(rel)), Depth: s.depth, Ignored: s.ignoring})
	}
	if shown > 0 {
		s.res.Lines[len(s.res.Lines)-1].More = more
	}
}

// href links a result: a line of a code page by its anchor, anything
// else by its page.
func (t *tree) href(p string, line int) string {
	u := (&url.URL{Path: t.prefix + p}).String()
	if line > 0 && !isMarkdown(p) {
		u += "#L" + strconv.Itoa(line)
	}
	return u
}

// lineAt returns line i of b, trimmed.
func lineAt(b []byte, i int) string {
	for ; i > 0; i-- {
		n := bytes.IndexByte(b, '\n')
		if n < 0 {
			return ""
		}
		b = b[n+1:]
	}
	if n := bytes.IndexByte(b, '\n'); n >= 0 {
		b = b[:n]
	}
	return strings.TrimSpace(string(b))
}

// around cuts a line so its match shows on a result's one row: from the
// line's start when the match falls early enough beside a path of pathLen
// bytes, or else from shortly before the match. A line whose length
// changes when lowered shows from its start, since the match's offset does
// not carry over. The result is at most 200 bytes, cut at a rune boundary.
func around(s, lq string, pathLen int) string {
	lower := strings.ToLower(s)
	at := strings.Index(lower, lq)
	if len(lower) == len(s) && at > max(rowLead-pathLen, leadBefore) {
		start := at
		for n := 0; n < leadBefore && start > 0; n++ {
			_, size := utf8.DecodeLastRuneInString(s[:start])
			start -= size
		}
		s = "…" + s[start:]
	}
	if len(s) > 200 {
		cut := 200
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}
