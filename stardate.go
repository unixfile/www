package main

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A stardate names a commit by its day and its place in that day, eg
// 261024.4, the fourth commit of 24 Oct 2026 on the repo's main line. A
// commit that a merge brought in is named after the merge, eg 261024.4.2
// for the second commit merge 261024.4 brought in. The main line is the
// first-parent line of origin's main or master, else of the local main or
// master, so every clone of one repo names its published commits alike,
// and checking out a branch moves nothing. The line only grows, so a
// stardate keeps naming one commit unless history is rewritten. Commits
// off the line, eg not yet pushed, have none and show their hash.
type stardates struct {
	tip    string            // the main line's head, which the numbers run to
	byHash map[string]string // full hash to stardate
	byDate map[string]string // stardate to full hash
	perDay map[string]int    // main-line commits per day so far
}

// mainRefs are the refs the main line follows, in order of preference.
var mainRefs = []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}

// mainTip is the commit at the head of r's main line.
func (r repo) mainTip(ctx context.Context) (string, bool) {
	table := exists(filepath.Join(r.common, "reftable"))
	for _, ref := range mainRefs {
		if !table {
			if h, ok := r.readRef(ref); ok && isHash(h) {
				return h, true
			}
			continue
		}
		if out, err := git(ctx, r.gitDir, "rev-parse", "--verify", "-q", ref); err == nil {
			return strings.TrimSpace(string(out)), true
		}
	}
	return "", false
}

// starFormat is how git prints a commit for numbering: hash, parents and
// committer date.
const starFormat = "--format=%H %P %cI"

// stardatesIn numbers the commits of the repo dir lies in, or returns nil
// when it has no main line. The numbers are kept under the line's tip. A
// fetch or push that moves the tip numbers only the new commits, as long
// as the new line runs through the old tip; otherwise, eg after a force
// push, it numbers all again. Numbering node's 49,203 commits took 657 ms
// on 2026-10-24.
func stardatesIn(ctx context.Context, dir string) *stardates {
	r := repoAt(ctx, dir)
	if r.complaint != "" {
		return nil
	}
	tip, ok := r.mainTip(ctx)
	if !ok {
		return nil
	}
	key, last := "stardates\x00"+r.common+"\x00"+tip, "stardates last\x00"+r.common
	if v, ok := gitCache.get(key); ok {
		return v.(*stardates)
	}
	var s *stardates
	if v, ok := gitCache.get(last); ok {
		if out, err := git(ctx, dir, "log", "--no-show-signature", "-z", starFormat, tip, "^"+v.(*stardates).tip); err == nil {
			s = v.(*stardates).extend(tip, out)
		}
	}
	if s == nil {
		out, err := git(ctx, dir, "log", "--no-show-signature", "-z", starFormat, tip)
		if err != nil {
			return nil
		}
		s = (&stardates{byHash: map[string]string{}, byDate: map[string]string{}, perDay: map[string]int{}}).extend(tip, out)
	}
	size := int64(0)
	for h, d := range s.byHash {
		size += 2 * int64(len(h)+len(d)+32)
	}
	gitCache.put(key, s, size)
	gitCache.put(last, s, 64)
	return s
}

// starNode is a commit as numbering needs it: its parents and the day it
// was committed, in the zone the commit records, so every reader sees the
// same day.
type starNode struct {
	parents []string
	day     string
}

// extend numbers out, git's records of the commits up to tip that s does
// not hold, along tip's first-parent line, and returns the result as a
// new set; s stays as it was. It returns nil when tip's line does not run
// through s's tip, since the old numbers would then be wrong.
func (s *stardates) extend(tip string, out []byte) *stardates {
	nodes := map[string]starNode{}
	for _, rec := range strings.Split(string(out), "\x00") {
		f := strings.Fields(rec)
		if len(f) < 2 {
			continue
		}
		when, err := time.Parse(time.RFC3339, f[len(f)-1])
		if err != nil {
			continue
		}
		nodes[f[0]] = starNode{parents: f[1 : len(f)-1], day: when.Format("060102")}
	}
	if s.tip != "" {
		h := tip
		for n, ok := nodes[h]; ok && len(n.parents) > 0; n, ok = nodes[h] {
			h = n.parents[0]
		}
		if h != s.tip {
			return nil
		}
	}
	n := &stardates{tip: tip, byHash: maps.Clone(s.byHash), byDate: maps.Clone(s.byDate), perDay: maps.Clone(s.perDay)}
	n.number(tip, nodes)
	return n
}

// number names the commits of nodes along tip's first-parent line, the
// main line, by day and place in the day.
func (s *stardates) number(tip string, nodes map[string]starNode) {
	name := func(h, d string) {
		s.byHash[h], s.byDate[d] = d, h
	}
	// chain is the first-parent line back from h to the first commit
	// already named, oldest first.
	chain := func(h string) []string {
		var c []string
		for h != "" && s.byHash[h] == "" {
			n, ok := nodes[h]
			if !ok {
				break
			}
			c = append(c, h)
			h = ""
			if len(n.parents) > 0 {
				h = n.parents[0]
			}
		}
		slices.Reverse(c)
		return c
	}
	// mergedIn names what merge m brought in: each further parent's line
	// back to what m's first parent already holds, oldest first, and
	// within it what its own merges brought in. Every commit the first
	// parent reaches is named by then, so each is named once.
	var mergedIn func(m string)
	mergedIn = func(m string) {
		n := 0
		for _, p := range nodes[m].parents[min(1, len(nodes[m].parents)):] {
			for _, h := range chain(p) {
				n++
				name(h, s.byHash[m]+"."+strconv.Itoa(n))
				mergedIn(h)
			}
		}
	}
	for _, h := range chain(tip) {
		d := nodes[h].day
		s.perDay[d]++
		name(h, d+"."+strconv.Itoa(s.perDay[d]))
		mergedIn(h)
	}
}

// label is what a reader sees for c: its stardate in s, or its short hash
// when it has none.
func (s *stardates) label(c *commit) string {
	if s != nil {
		if d := s.byHash[c.hash]; d != "" {
			return d
		}
	}
	return c.short
}
