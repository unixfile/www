package main

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
)

// gone is where a path the tree lacks went, as git tells it: the commit
// that deleted or moved it, nil when only the work tree lacks it; the
// last commit that held it; and where it moved, a name the tree holds now.
type gone struct {
	removed, last *commit
	dir           bool
	to            string
}

// whereGone asks git what became of name, a path below t that t lacks.
// It is nil when git never held name, or t is a share or a version,
// which have no versions to link. The answer is kept under HEAD.
func (t *tree) whereGone(ctx context.Context, name string) *gone {
	if !t.hasVersions() {
		return nil
	}
	r := repoAt(ctx, t.dir)
	if r.complaint != "" {
		return nil
	}
	key := "gone\x00" + r.gitDir + "\x00" + r.head(ctx) + "\x00" + t.dir + "\x00" + name
	if v, ok := gitCache.get(key); ok {
		return v.(*gone)
	}
	g := t.findGone(ctx, name)
	gitCache.put(key, g, 256)
	return g
}

func (t *tree) findGone(ctx context.Context, name string) *gone {
	out, err := git(ctx, t.dir, "log", "--no-show-signature", "-z", "--format="+commitFormat, "-n", "2", "--", "./"+name)
	if err != nil {
		return nil
	}
	log := t.commits(out)
	held := func(c *commit) (bool, bool) {
		e, err := commitFS{dir: t.dir, rev: c.hash}.entry(name)
		return err == nil, e.typ == "tree"
	}
	if len(log) == 0 {
		return nil
	}
	g := &gone{}
	if ok, dir := held(&log[0]); ok {
		// HEAD holds it, so only the work tree lacks it.
		g.last, g.dir = &log[0], dir
		return g
	}
	g.removed = &log[0]
	if len(log) > 1 {
		if ok, dir := held(&log[1]); ok {
			g.last, g.dir = &log[1], dir
		}
	}
	g.to = t.movedTo(ctx, g.removed.hash, name)
	return g
}

// movedTo is where commit hash moved name, when the tree holds that name
// now, or "". A directory moved when its files moved with it.
func (t *tree) movedTo(ctx context.Context, hash, name string) string {
	out, err := git(ctx, t.dir, "show", "--no-show-signature", "--relative", "-M", "--name-status", "-z", "--format=", hash, "--")
	if err != nil {
		return ""
	}
	// Each record is a status and a path, or for a copy or a move, a
	// status and two.
	f := strings.Split(string(out), "\x00")
	for i := 0; i < len(f); {
		st := f[i]
		if st == "" || st[0] != 'R' && st[0] != 'C' {
			i += 2
			continue
		}
		if i+2 >= len(f) {
			break
		}
		from, to := f[i+1], f[i+2]
		i += 3
		if st[0] != 'R' {
			continue
		}
		if to, ok := moved(name, from, to); ok {
			if _, err := fs.Stat(t.fsys, to); err == nil {
				return to
			}
		}
	}
	return ""
}

// moved is where name went when a commit moved from to to: to itself, or
// for a directory the new directory, when from lies below name and the
// rest of its path stayed.
func moved(name, from, to string) (string, bool) {
	if from == name {
		return to, true
	}
	rest, ok := strings.CutPrefix(from, name+"/")
	if !ok || !strings.HasSuffix(to, "/"+rest) {
		return "", false
	}
	return strings.TrimSuffix(to, "/"+rest), true
}

// existing is the nearest directory above name that t holds, and the
// first segment below it that t lacks.
func (t *tree) existing(name string) (dir, missing string) {
	dir, missing = path.Dir(name), path.Base(name)
	for dir != "." {
		if info, err := fs.Stat(t.fsys, dir); err == nil && info.IsDir() {
			return dir, missing
		}
		dir, missing = path.Dir(dir), path.Base(dir)
	}
	return ".", missing
}

// closeNames are up to five names t holds that a reader who typed name
// may have meant, nearest first. They lie in the nearest directory above
// name that exists, and where name went deeper, the rest of it follows
// when the tree holds that too.
func (t *tree) closeNames(name string) []string {
	dir, missing := t.existing(name)
	if t.refused(dir) != nil {
		return nil
	}
	entries, err := fs.ReadDir(t.fsys, dir)
	if err != nil {
		return nil
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(name, dir+"/"), missing)
	if dir == "." {
		rest = strings.TrimPrefix(name, missing)
	}
	type near struct {
		name string
		d    int
	}
	var found []near
	want := strings.ToLower(missing)
	for _, e := range entries {
		n := e.Name()
		if t.unlisted(dir, n) {
			continue
		}
		d := editDistance(want, strings.ToLower(n))
		if !closeEnough(want, strings.ToLower(n), d) {
			continue
		}
		p := path.Join(dir, n)
		if q := path.Join(dir, n+rest); rest != "" {
			if _, err := fs.Stat(t.fsys, q); err == nil {
				p = q
			}
		}
		if info, err := fs.Stat(t.fsys, p); err != nil {
			continue
		} else if info.IsDir() {
			p += "/"
		}
		found = append(found, near{p, d})
	}
	slices.SortStableFunc(found, func(a, b near) int { return a.d - b.d })
	var names []string
	for i := 0; i < len(found) && i < 5; i++ {
		names = append(names, found[i].name)
	}
	return names
}

// closeEnough reports whether have, d edits from want, is a name a reader
// may have meant: a slip of a few letters, in the whole name or in its
// stem with another extension, or the start of the name.
func closeEnough(want, have string, d int) bool {
	stem := func(s string) string { return strings.TrimSuffix(s, path.Ext(s)) }
	slip := func(a, b string) bool { return editDistance(a, b) <= max(1, len([]rune(a))/3) }
	switch {
	case d <= max(1, len([]rune(want))/3):
		return true
	case stem(want) != "" && slip(stem(want), stem(have)):
		return true
	}
	return len(want) >= 3 && strings.HasPrefix(have, want)
}

// editDistance is the number of letters to insert, delete or change to
// turn a into b.
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	row := make([]int, len(y)+1)
	for j := range row {
		row[j] = j
	}
	for i := 1; i <= len(x); i++ {
		prev := row[0]
		row[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur := min(row[j]+1, row[j-1]+1, prev+cost)
			prev, row[j] = row[j], cur
		}
	}
	return row[len(y)]
}

// notFoundRow is the first row of a 404's table: the whole URL r asked
// for, broken only after a slash.
func notFoundRow(r *http.Request) string {
	return goneRow("not found", segments(fullURL(r)))
}

// goneRow is one row of a 404's table: a label, then its value as HTML.
func goneRow(label, value string) string {
	return "<tr><th>" + template.HTMLEscapeString(label) + "</th><td>" + value + "</td></tr>\n"
}

// goneRows are the rows of the 404's table below "not found": where git
// says name went, and the names close to it.
func (t *tree) goneRows(ctx context.Context, name string) string {
	esc := template.HTMLEscapeString
	link := func(p string) string {
		return fmt.Sprintf("<a href=\"%s\">%s</a>", esc((&url.URL{Path: t.prefix + p}).String()), segments(p))
	}
	var b strings.Builder
	if g := t.whereGone(ctx, name); g != nil {
		stars := stardatesIn(ctx, t.dir)
		dir, _ := t.existing(name)
		if g.removed != nil {
			show := (&url.URL{Path: t.prefix + below(dir, true), RawQuery: "commit=" + g.removed.hash}).String()
			label := "deleted"
			if g.to != "" {
				label = "moved"
			}
			b.WriteString(goneRow(label, fmt.Sprintf("<a href=\"%s\">%s</a> · %s", esc(show), esc(stars.label(g.removed)), esc(subjectOf(g.removed)))))
			if g.to != "" {
				b.WriteString(goneRow("to", link(g.to)))
			}
		} else {
			b.WriteString(goneRow("deleted", "since the last commit"))
		}
		if g.last != nil {
			b.WriteString(goneRow("last version", fmt.Sprintf("<a href=\"%s\">%s</a> · %s",
				esc(t.versionURL(stars, g.last, below(name, g.dir))), esc(stars.label(g.last)), esc(subjectOf(g.last)))))
		}
	}
	for i, n := range t.closeNames(name) {
		label := ""
		if i == 0 {
			label = "close"
		}
		b.WriteString(goneRow(label, link(n)))
	}
	return b.String()
}
