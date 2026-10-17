package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The cache drops the least recently used value past its budget, and a
// budget of 0 keeps nothing.
func TestCacheBudget(t *testing.T) {
	c := newCache(25) // two of the 10-byte entries, keys included
	c.put("a", 1, 9)
	c.put("b", 2, 9)
	c.get("a")
	c.put("c", 3, 9)
	if _, ok := c.get("b"); ok {
		t.Error("b, the least recently used, was kept")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s was dropped", k)
		}
	}
	none := newCache(0)
	none.put("a", 1, 0)
	if _, ok := none.get("a"); ok {
		t.Error("a budget of 0 kept a value")
	}
}

func TestParseSize(t *testing.T) {
	for s, want := range map[string]int64{"0": 0, "512": 512, "64K": 64 << 10, "32M": 32 << 20, "1g": 1 << 30} {
		if got, err := parseSize(s); err != nil || got != want {
			t.Errorf("%s: %d %v, want %d", s, got, err, want)
		}
	}
	for _, s := range []string{"", "M", "lots", "-1", "1T"} {
		if _, err := parseSize(s); err == nil {
			t.Errorf("%q: no error", s)
		}
	}
}

// HEAD read from the repo's files follows each commit, so a cached
// history never outlives it; a repo made in a directory that had none is
// found.
func TestCachedHeadFollowsCommits(t *testing.T) {
	needGit(t)
	s, dir := repoTree(t)
	tr := s.list()[0]
	ctx := context.Background()
	_, first := tr.gitHead("index.md")
	before := tr.lastCommit(ctx, "index.md")
	os.WriteFile(filepath.Join(dir, "index.md"), []byte("# Changed\n"), 0o644)
	commitAll(t, dir, "Change the index")
	_, second := tr.gitHead("index.md")
	after := tr.lastCommit(ctx, "index.md")
	if first == second || before == nil || after == nil || after.subject != "Change the index" {
		t.Errorf("HEAD %q then %q; last commit %v then %v", first, second, before, after)
	}

	plain := t.TempDir()
	if r := repoAt(ctx, plain); r.complaint == "" {
		t.Fatalf("%s is in a repo: %+v", plain, r)
	}
	git(ctx, plain, "init", "-q")
	if r := repoAt(ctx, plain); r.complaint != "" {
		t.Errorf("new repo not found: %s", r.complaint)
	}
}
