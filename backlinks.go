package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// linkIndex keeps, for each Markdown file in a tree, the files its
// relative links name. Each request walks the tree and reads again only
// the files whose size or time changed, so the answer is current without
// a watch on every directory, and a walk cut short by the budget leaves
// what it read for the next one.
type linkIndex struct {
	mu    sync.Mutex
	files map[string]linkedFile
}

type linkedFile struct {
	mod  time.Time
	size int64
	to   []string // the files its links name, each once
}

type backlinks struct {
	Links []hit `json:"links"`
	Files int   `json:"files"` // Markdown files read for links
	Cut   bool  `json:"cut"`   // stopped at the time budget
}

// serveBacklinks answers ?backlinks on a file: the Markdown files in the
// tree that link to it. The page loads first and asks for this after, since
// a walk of a large tree takes far over a page's budget.
func (t *tree) serveBacklinks(w http.ResponseWriter, r *http.Request, name string) {
	ctx, cancel := context.WithTimeout(r.Context(), searchBudget)
	defer cancel()
	// A version has no link index, and walking a commit to build one runs
	// git for every directory, so its docs list no backlinks.
	res := backlinks{Links: []hit{}}
	if t.at == nil {
		res = t.backlinks(ctx, name)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(res)
}

// warmLinks reads a new tree's links in the background, so the first doc
// opened finds them read: a tree of 1,200 docs takes about a second.
func (t *tree) warmLinks() {
	ctx, cancel := context.WithTimeout(context.Background(), linkWarmBudget)
	defer cancel()
	start := time.Now()
	res := t.backlinks(ctx, "")
	logInfo("read the links of %d docs in %s in %d ms", res.Files, t.dir, time.Since(start).Milliseconds())
}

// linkWarmBudget bounds warmLinks; what it leaves, requests read.
const linkWarmBudget = 10 * time.Second

func (t *tree) backlinks(ctx context.Context, name string) backlinks {
	ix := &t.linked
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.files == nil {
		ix.files = map[string]linkedFile{}
	}
	res := backlinks{Links: []hit{}}
	seen := map[string]bool{}
	// The walk skips what search skips, so a share reads only what it
	// would serve.
	fs.WalkDir(t.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			res.Cut = true
			return fs.SkipAll
		}
		if p == "." {
			return nil
		}
		if err != nil || t.unlisted(path.Dir(p), d.Name()) || d.IsDir() && unsearched[d.Name()] {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !isMarkdown(p) {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxSearchFile {
			return nil
		}
		seen[p] = true
		if f, ok := ix.files[p]; ok && f.size == info.Size() && f.mod.Equal(info.ModTime()) {
			return nil
		}
		b, err := fs.ReadFile(t.fsys, p)
		if err != nil {
			return nil
		}
		res.Files++
		ix.files[p] = linkedFile{mod: info.ModTime(), size: info.Size(), to: linkTargets(p, b)}
		return nil
	})
	// Only a whole walk shows which files are gone; a cut one answers from
	// what the index knew.
	if !res.Cut {
		for p := range ix.files {
			if !seen[p] {
				delete(ix.files, p)
			}
		}
	}
	for p, f := range ix.files {
		if p != name && slices.Contains(f.to, name) {
			res.Links = append(res.Links, hit{Href: t.href(p, 0), Path: p})
		}
	}
	slices.SortFunc(res.Links, func(a, b hit) int { return strings.Compare(a.Path, b.Path) })
	return res
}

// linkTargets lists the files in the tree that the doc p links to, each
// once and without p itself. A link that climbs out of the tree is left
// out.
func linkTargets(p string, src []byte) []string {
	// Most of a walk's time goes to parsing, so a doc without a link's
	// brackets is not parsed.
	if !bytes.Contains(src, []byte("](")) && !bytes.Contains(src, []byte("]:")) && !bytes.Contains(src, []byte("[[")) {
		return nil
	}
	var out []string
	doc := markdown.Parser().Parse(text.NewReader(src))
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		l, ok := n.(*ast.Link)
		if !entering || !ok {
			return ast.WalkContinue, nil
		}
		dest := relativePath(string(l.Destination))
		if dest == "" {
			return ast.WalkContinue, nil
		}
		to := path.Join(path.Dir(p), dest)
		if to != p && to != ".." && !strings.HasPrefix(to, "../") && !slices.Contains(out, to) {
			out = append(out, to)
		}
		return ast.WalkContinue, nil
	})
	return out
}
