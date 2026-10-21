package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// maxNameHits and maxLineHits bound what one search returns.
	maxNameHits = 30
	maxLineHits = 100
	// maxSearchFile skips larger files for content, as a code page does.
	maxSearchFile = maxCode
	// searchBudget bounds one search, since a tree may hold many files; the
	// answer says when it stopped early.
	searchBudget = 500 * time.Millisecond
	// minContentQuery is the shortest query searched in contents; one
	// letter matches nearly every line.
	minContentQuery = 2
)

// hit is one search result: a name that matched, or a line that did.
type hit struct {
	Href string `json:"href"`
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
	Here bool   `json:"here,omitempty"` // in the searched directory itself, not below it
}

type found struct {
	Names []hit `json:"names"`
	Lines []hit `json:"lines"`
	Files int   `json:"files"` // files searched for content
	Cut   bool  `json:"cut"`   // stopped at the time budget or the hit limits
}

// serveSearch answers ?search=q below name: names that hold q first, then
// lines that hold it, both ignoring case. It reads the tree through its
// own file system, so a share searches only what it would serve. Dot
// entries are skipped, since .git alone would swamp the answer.
func (t *tree) serveSearch(w http.ResponseWriter, r *http.Request, name, q string) {
	ctx, cancel := context.WithTimeout(r.Context(), searchBudget)
	defer cancel()
	res := t.search(ctx, name, q)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(res)
}

func (t *tree) search(ctx context.Context, dir, q string) found {
	res := found{Names: []hit{}, Lines: []hit{}}
	lq := strings.ToLower(strings.TrimSpace(q))
	if lq == "" {
		return res
	}
	needle := []byte(lq)
	contents := len(lq) >= minContentQuery
	visit := func(p string, d fs.DirEntry) {
		here := path.Dir(p) == dir
		if strings.Contains(strings.ToLower(d.Name()), lq) {
			if len(res.Names) == maxNameHits {
				res.Cut = true
			} else {
				label := p
				if d.IsDir() {
					label += "/"
				}
				res.Names = append(res.Names, hit{Href: t.href(label, 0), Path: label, Here: here})
			}
		}
		if d.IsDir() || !contents || len(res.Lines) == maxLineHits {
			return
		}
		if info, err := d.Info(); err != nil || !info.Mode().IsRegular() || info.Size() > maxSearchFile {
			return
		}
		b, err := fs.ReadFile(t.fsys, p)
		if err != nil || !isText(b) {
			return
		}
		res.Files++
		lower := bytes.ToLower(b)
		if !bytes.Contains(lower, needle) {
			return
		}
		for i, line := range bytes.Split(lower, []byte("\n")) {
			if !bytes.Contains(line, needle) {
				continue
			}
			if len(res.Lines) == maxLineHits {
				res.Cut = true
				break
			}
			// Lowering may change a line's length outside ASCII but keeps
			// its newlines, so the line is cut from the original by number.
			res.Lines = append(res.Lines, hit{Href: t.href(p, i+1), Path: p, Line: i + 1, Text: lineAt(b, i), Here: here})
		}
	}
	skip := func(d fs.DirEntry) bool { return strings.HasPrefix(d.Name(), ".") || isLockName(d.Name()) }
	// The directory's own entries come first, so the hit limits cannot
	// crowd them out with deeper ones; then each subdirectory in turn.
	entries, err := fs.ReadDir(t.fsys, dir)
	if err != nil {
		res.Cut = true
		return res
	}
	for _, d := range entries {
		if ctx.Err() != nil {
			res.Cut = true
			return res
		}
		if !skip(d) {
			visit(path.Join(dir, d.Name()), d)
		}
	}
	errStop := errors.New("stop")
	for _, e := range entries {
		if !e.IsDir() || skip(e) {
			continue
		}
		sub := path.Join(dir, e.Name())
		err := fs.WalkDir(t.fsys, sub, func(p string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				res.Cut = true
				return errStop
			}
			if err != nil || (p != sub && skip(d)) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if p != sub {
				visit(p, d)
			}
			return nil
		})
		if err == errStop {
			break
		}
		if err != nil {
			res.Cut = true
		}
	}
	return res
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

// lineAt returns line i of b, trimmed and cut to 200 bytes at a rune
// boundary.
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
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		cut := 200
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}
