package main

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The main line numbers each day from one; a merge names what it brought
// in after itself, and so does a merge inside that. The day is the one the
// commit records, whatever the reader's zone.
func TestNumberCommits(t *testing.T) {
	// Newest first, as git log prints: hash, parents, committer date.
	log := strings.Join([]string{
		"c m 2026-10-25T09:00:00+02:00",
		"m a x2 2026-10-24T18:00:00+02:00",
		"x2 x1 z 2026-10-24T17:00:00+02:00",
		"z a 2026-10-24T16:30:00+02:00",
		"x1 a 2026-10-24T16:00:00+02:00",
		"a 2026-10-24T23:30:00-07:00",
	}, "\x00")
	s := fresh().extend("c", []byte(log))
	for h, want := range map[string]string{"a": "261024.1", "m": "261024.2", "x1": "261024.2.1", "x2": "261024.2.2", "z": "261024.2.2.1", "c": "261025.1"} {
		if got := s.byHash[h]; got != want {
			t.Errorf("%s: %q, want %q", h, got, want)
		}
		if got := s.byDate[want]; got != h {
			t.Errorf("%s names %q, want %q", want, got, h)
		}
	}
	if got := s.label(&commit{hash: "off", short: "0ff"}); got != "0ff" {
		t.Errorf("a commit off the line is %q, want its hash", got)
	}
	// Numbering up to m, then the commits after it, gives the same names;
	// a line that does not run through the old tip numbers nothing.
	old := fresh().extend("m", []byte(strings.Join(strings.Split(log, "\x00")[1:], "\x00")))
	if got := old.extend("c", []byte("c m 2026-10-25T09:00:00+02:00")); got == nil || got.byHash["c"] != "261025.1" || got.byHash["z"] != "261024.2.2.1" {
		t.Errorf("extended past m: %v", got)
	}
	if old.byHash["c"] != "" {
		t.Error("extend changed the set it extended")
	}
	if got := old.extend("d", []byte("d x1 2026-10-25T09:00:00+02:00")); got != nil {
		t.Errorf("a line off the old tip extended: %v", got.byHash)
	}
}

// A commit on the local branch that origin's line has not reached reads as
// its day and x, and a merge it made names what it brought in after
// itself; none of them gets a place or an address.
func TestPendingCommits(t *testing.T) {
	s := fresh().extend("a", []byte("a 2026-10-24T16:00:00+02:00"))
	p := s.withPending("m", []byte(strings.Join([]string{
		"m b y 2026-10-26T10:00:00+02:00",
		"y a 2026-10-26T09:30:00+02:00",
		"b a 2026-10-26T09:00:00+02:00",
	}, "\x00")))
	for h, want := range map[string]string{"a": "261024.1", "b": "261026.x", "m": "261026.x", "y": "261026.x.1"} {
		c := &commit{hash: h, short: h + "0"}
		if got := p.label(c); got != want {
			t.Errorf("%s: %q, want %q", h, got, want)
		}
		if h != "a" && p.addr(c) != c.short {
			t.Errorf("%s addresses as %q, want its hash", h, p.addr(c))
		}
		// A list's rows tell pending commits apart by their hash.
		row := want
		if h != "a" {
			row += " " + c.short
		}
		if got := p.rowLabel(c); got != row {
			t.Errorf("%s: row %q, want %q", h, got, row)
		}
	}
	if s.pending != nil || len(p.byDate) != 1 || p.perDay["261026"] != 0 {
		t.Errorf("pending commits were numbered: %v %v", p.byDate, p.perDay)
	}
}

// An unpushed commit shows its day and x on the page and in a version's
// crumb, and its version keeps the hash in its address.
func TestPendingVersion(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	branch, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "update-ref", "refs/remotes/origin/"+strings.TrimSpace(string(branch)), "HEAD~1").CombinedOutput(); err != nil {
		t.Fatalf("update-ref: %v\n%s", err, out)
	}
	short := head(t, dir, "HEAD")[:7]
	star := stardatesIn(context.Background(), dir).label(&commit{hash: head(t, dir, "HEAD"), short: short})
	if !regexp.MustCompile(`^[0-9]{6}\.x$`).MatchString(star) {
		t.Fatalf("unpushed commit is %q, want its day and x", star)
	}
	if body := get(h, "/a/b.go").Body.String(); !strings.Contains(body, `title="`+star+` · `) {
		t.Errorf("top line names no %s:\n%s", star, body)
	}
	if body := get(h, "/a/b.go?log").Body.String(); !strings.Contains(body, ">"+star+" "+short+"</span>") {
		t.Errorf("history names no %s %s:\n%s", star, short, body)
	}
	rec := get(h, "/@"+short+"/a/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), " @ "+star+"</a>") {
		t.Errorf("version @%s: %d\n%s", short, rec.Code, rec.Body.String())
	}
}

// A repo whose main branch is neither main nor master follows the branch
// origin's HEAD names.
func TestOriginHeadIsTheMainLine(t *testing.T) {
	_, dir := repoTree(t)
	full := head(t, dir, "HEAD")
	c := &commit{hash: full, short: full[:7]}
	if star := stardatesIn(context.Background(), dir).label(c); star == c.short {
		t.Fatalf("on the local line HEAD has no stardate")
	}
	for _, args := range [][]string{
		{"update-ref", "refs/remotes/origin/trunk", "HEAD~1"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	s := stardatesIn(context.Background(), dir)
	if got := s.label(c); got != c.short {
		t.Errorf("HEAD, past origin/trunk, is %q, want its hash", got)
	}
	if s.byHash[head(t, dir, "HEAD~1")] == "" {
		t.Error("origin/trunk's tip has no stardate")
	}
}

func fresh() *stardates {
	return &stardates{byHash: map[string]string{}, byDate: map[string]string{}, perDay: map[string]int{}}
}
