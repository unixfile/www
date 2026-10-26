package main

import (
	"context"
	"maps"
	"os"
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
// stardate keeps naming one commit unless history is rewritten. A commit
// on the local branch of that name but not yet pushed has a day and no
// place yet, eg 261026.x; it gets its number when origin's line reaches
// it, so a rebase before the push renames nothing. Commits on no main
// line, eg on another branch, show their hash.
type stardates struct {
	tip     string            // the main line's head, which the numbers run to
	byHash  map[string]string // full hash to stardate
	byDate  map[string]string // stardate to full hash
	perDay  map[string]int    // main-line commits per day so far
	pending map[string]string // full hash to day.x, for unpushed commits
}

// mainRefs are the refs the main line follows, in order of preference.
// "" stands for the branch origin's HEAD names, for a repo whose main
// branch has another name, eg origin/trunk. It comes after main and
// master, so naming origin's HEAD renumbers no repo that has them.
var mainRefs = []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "", "refs/heads/main", "refs/heads/master"}

// mainTip is the commit at the head of r's main line, and the ref it
// came from.
func (r repo) mainTip(ctx context.Context) (string, string, bool) {
	for _, ref := range mainRefs {
		if ref == "" {
			var ok bool
			if ref, ok = r.originHead(ctx); !ok {
				continue
			}
		}
		if h, ok := r.refTip(ctx, ref); ok {
			return h, ref, true
		}
	}
	return "", "", false
}

// originHead is the branch origin's HEAD points at, eg
// refs/remotes/origin/trunk, as git clone set it.
func (r repo) originHead(ctx context.Context) (string, bool) {
	var ref string
	if exists(filepath.Join(r.common, "reftable")) {
		out, err := git(ctx, r.gitDir, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
		if err != nil {
			return "", false
		}
		ref = strings.TrimSpace(string(out))
	} else {
		b, err := os.ReadFile(filepath.Join(r.common, "refs", "remotes", "origin", "HEAD"))
		if err != nil {
			return "", false
		}
		ref, _ = strings.CutPrefix(strings.TrimSpace(string(b)), "ref: ")
	}
	return ref, strings.HasPrefix(ref, "refs/remotes/origin/") && ref != "refs/remotes/origin/HEAD"
}

// refTip is the commit ref points at.
func (r repo) refTip(ctx context.Context, ref string) (string, bool) {
	if !exists(filepath.Join(r.common, "reftable")) {
		h, ok := r.readRef(ref)
		return h, ok && isHash(h)
	}
	out, err := git(ctx, r.gitDir, "rev-parse", "--verify", "-q", ref)
	return strings.TrimSpace(string(out)), err == nil
}

// starFormat is how git prints a commit for numbering: hash, parents and
// committer date.
const starFormat = "--format=%H %P %cI"

// stardatesIn numbers the commits of the repo dir lies in, or returns nil
// when it has no main line. The numbers are kept under the line's tip. A
// fetch or push that moves the tip numbers only the new commits, as long
// as the new line runs through the old tip; otherwise, eg after a force
// push, it numbers all again. Numbering node's 49,203 commits took 657 ms
// on 2026-10-24. When the line is origin's, the local branch of the same
// name adds its unpushed commits as pending, kept under both tips.
func stardatesIn(ctx context.Context, dir string) *stardates {
	r := repoAt(ctx, dir)
	if r.complaint != "" {
		return nil
	}
	tip, ref, ok := r.mainTip(ctx)
	if !ok {
		return nil
	}
	s := numbered(ctx, dir, r, tip)
	if s == nil || !strings.HasPrefix(ref, "refs/remotes/origin/") {
		return s
	}
	local, ok := r.refTip(ctx, "refs/heads/"+strings.TrimPrefix(ref, "refs/remotes/origin/"))
	if !ok || local == tip {
		return s
	}
	key := "stardates local\x00" + r.common + "\x00" + tip + "\x00" + local
	if v, ok := gitCache.get(key); ok {
		return v.(*stardates)
	}
	out, err := git(ctx, dir, "log", "--no-show-signature", "-z", starFormat, local, "^"+tip)
	if err != nil {
		return s
	}
	p := s.withPending(local, out)
	gitCache.put(key, p, 64+int64(len(p.pending))*2*(40+16+32))
	return p
}

// numbered is the stardates of r's main line up to tip, kept under tip.
func numbered(ctx context.Context, dir string, r repo, tip string) *stardates {
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
	nodes := starNodes(out)
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

// withPending is s with the commits of out, git's records of a local
// line up to local beyond s's tip, named pending by their day and x. s
// stays as it was; the result shares its numbers.
func (s *stardates) withPending(local string, out []byte) *stardates {
	n := *s
	n.pending = map[string]string{}
	n.number(local, starNodes(out))
	return &n
}

// starNodes reads out, git's records in starFormat, by hash.
func starNodes(out []byte) map[string]starNode {
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
	return nodes
}

// number names the commits of nodes along tip's first-parent line, the
// main line, by day and place in the day. With s.pending set, it names
// them pending instead: by day and x, with no place and no address.
func (s *stardates) number(tip string, nodes map[string]starNode) {
	name := func(h, d string) {
		if s.pending != nil {
			s.pending[h] = d
			return
		}
		s.byHash[h], s.byDate[d] = d, h
	}
	known := func(h string) string {
		if d := s.byHash[h]; d != "" {
			return d
		}
		return s.pending[h]
	}
	// chain is the first-parent line back from h to the first commit
	// already named, oldest first.
	chain := func(h string) []string {
		var c []string
		for h != "" && known(h) == "" {
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
				name(h, known(m)+"."+strconv.Itoa(n))
				mergedIn(h)
			}
		}
	}
	for _, h := range chain(tip) {
		d := nodes[h].day
		if s.pending != nil {
			name(h, d+".x")
			mergedIn(h)
			continue
		}
		s.perDay[d]++
		name(h, d+"."+strconv.Itoa(s.perDay[d]))
		mergedIn(h)
	}
}

// rowLabel is label for a row in a list of commits. A day and x, which
// several pending commits share, adds the short hash, eg 261026.x 8392cf3,
// so each row names its own commit.
func (s *stardates) rowLabel(c *commit) string {
	if s != nil && s.pending[c.hash] != "" {
		return s.pending[c.hash] + " " + c.short
	}
	return s.label(c)
}

// label is what a reader sees for c: its stardate in s, its day and x
// while it waits for a push, or its short hash when it has neither.
func (s *stardates) label(c *commit) string {
	if s != nil && s.pending[c.hash] != "" {
		return s.pending[c.hash]
	}
	return s.addr(c)
}

// addr is what names c in a version's address: its stardate in s, or its
// short hash when it has none. A pending day and x names no one commit,
// so it never addresses one.
func (s *stardates) addr(c *commit) string {
	if s != nil {
		if d := s.byHash[c.hash]; d != "" {
			return d
		}
	}
	return c.short
}
