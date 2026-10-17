package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// pageBudget is the time mdserve promises for any page from a warm server.
const pageBudget = 5 * time.Millisecond

// Every kind of page renders within pageBudget once its git answers are
// cached: a doc and a code page the size of mdserve's own README and
// render.go, a listing, histories and a commit's diff. Each takes the
// median of 15 runs, so one slow run does not fail it.
func TestPageBudget(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	for _, name := range []string{"README.md", "render.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	commitAll(t, dir, "Add the README and render.go")
	s := newServer()
	addTree(t, s, dir, "/", "budget")
	h := s.handler()
	c := head(t, dir, "HEAD")
	for _, p := range []string{"/README.md", "/render.go", "/", "/?log", "/README.md?log", "/README.md?commit=" + c} {
		get(h, p)
		times := make([]time.Duration, 15)
		for i := range times {
			start := time.Now()
			rec := get(h, p)
			times[i] = time.Since(start)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status %d", p, rec.Code)
			}
		}
		slices.Sort(times)
		if m := times[len(times)/2]; m > pageBudget {
			t.Errorf("%s: %v, over the budget of %v", p, m, pageBudget)
		} else {
			t.Logf("%s: %v", p, m)
		}
	}
}
