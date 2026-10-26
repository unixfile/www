package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// lookupCommit writes, as keyval, the commit c.commit names in the repo
// c.dir lies in: its full hash, its stardate when it has one, and its
// subject. A stardate seen on a page turns into a hash git takes.
func lookupCommit(w io.Writer, c config) int {
	dir, err := filepath.Abs(c.dir)
	if err != nil {
		return failNow(errnoOf(err), "bad directory: "+err.Error())
	}
	if fi, err := os.Stat(dir); err != nil {
		return failNow(errnoOf(err), "no directory "+c.dir)
	} else if !fi.IsDir() {
		return failNow(syscall.ENOTDIR, "not a directory: "+c.dir)
	}
	ctx := context.Background()
	if r := repoAt(ctx, dir); r.complaint != "" {
		return failNow(syscall.ENOENT, c.dir+": "+r.complaint)
	}
	cm, stars, err := findCommit(ctx, dir, c.commit)
	if err != nil {
		return failNow(errnoOf(err), err.Error())
	}
	fmt.Fprintf(w, "hash %s\n", cm.hash)
	if d := stars.label(&cm); d != cm.short {
		fmt.Fprintf(w, "stardate %s\n", d)
	}
	fmt.Fprintf(w, "subject %s\n", kvValue(cm.subject))
	return 0
}
