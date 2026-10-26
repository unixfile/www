package main

import (
	"context"
	"strings"
	"syscall"
	"testing"
)

// --commit turns a stardate, a hash or a git name into keyval, and fails
// with E2 on a commit or repo that is not there.
func TestLookupCommit(t *testing.T) {
	_, dir := repoTree(t)
	full := head(t, dir, "HEAD")
	star := stardatesIn(context.Background(), dir).byHash[full]
	if star == "" {
		t.Fatal("HEAD has no stardate")
	}
	want := "hash " + full + "\nstardate " + star + "\nsubject Rename the package <b>\n"
	for _, rev := range []string{star, full, full[:7], "HEAD"} {
		var b strings.Builder
		if code := lookupCommit(&b, config{dir: dir, commit: rev}); code != 0 || b.String() != want {
			t.Errorf("%s: exit %d, got\n%s\nwant\n%s", rev, code, b.String(), want)
		}
	}
	for _, c := range []config{
		{dir: dir, commit: "990101.1"},
		{dir: dir, commit: "cafe"},
		{dir: dir, commit: "--all"},
		{dir: t.TempDir(), commit: "HEAD"},
		{dir: dir + "/nowhere", commit: "HEAD"},
	} {
		var b strings.Builder
		if code := lookupCommit(&b, c); code != int(syscall.ENOENT) || b.Len() != 0 {
			t.Errorf("%s in %s: exit %d, printed %q; want E2 and nothing", c.commit, c.dir, code, b.String())
		}
	}
	// A day alone names many commits, though six digits are hex too.
	var b strings.Builder
	if code := lookupCommit(&b, config{dir: dir, commit: star[:6]}); code != int(syscall.EINVAL) {
		t.Errorf("%s: exit %d, want E22", star[:6], code)
	}
}
