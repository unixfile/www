package main

import (
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

func fresh() *stardates {
	return &stardates{byHash: map[string]string{}, byDate: map[string]string{}, perDay: map[string]int{}}
}
