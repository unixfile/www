package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// servedTree is a tree with a live lock.
type servedTree struct {
	ep       netip.AddrPort
	pid      int
	dir, url string
	token    string    // the secret path's token, or ""
	since    time.Time // when the lock was written
	lockPath string
	persist  bool   // the lock outlives its server
	binary   string // the server's binary, or "" when unrecorded
}

// readTree reads the lock that a registry link targets and reports whether
// its server runs.
func readTree(ep netip.AddrPort, target string) (servedTree, bool) {
	lockPath, token := splitTarget(target)
	l, ok := readLive(ep, lockPath)
	if !ok {
		return servedTree{}, false
	}
	t := servedTree{ep: ep, pid: l.pid, dir: filepath.Dir(lockPath), url: l.url, token: token,
		lockPath: lockPath, persist: isPersist(lockPath), binary: binaryOf(ep)}
	if info, err := os.Stat(lockPath); err == nil {
		t.since = info.ModTime()
	}
	return t, true
}

// peekTrees reads the live trees of ep without locking its registry or
// removing stale locks, for a page that only shows them.
func peekTrees(ep netip.AddrPort) []servedTree {
	dir, err := registryDir(ep)
	if err != nil {
		return nil
	}
	links, _ := readLinks(dir)
	var trees []servedTree
	for _, target := range links {
		if t, ok := readTree(ep, target); ok {
			trees = append(trees, t)
		}
	}
	return trees
}

// liveTrees returns the trees whose locks the registry of ep links to and
// whose server still runs. Stale locks go on the way, as findLive does.
func liveTrees(ep netip.AddrPort) ([]servedTree, error) {
	reg, err := openRegistry(ep)
	if err != nil {
		return nil, &errno{errnoOf(err), "cannot open the registry: " + err.Error()}
	}
	defer reg.close()
	if err := reg.lock(); err != nil {
		return nil, &errno{errnoOf(err), "cannot lock the registry " + reg.dir + ": " + err.Error()}
	}
	links, err := reg.links()
	if err != nil {
		return nil, &errno{errnoOf(err), "cannot read the registry " + reg.dir + ": " + err.Error()}
	}
	var trees []servedTree
	for _, target := range links {
		lockPath, _ := splitTarget(target)
		if _, ok := findLive(ep, lockPath); !ok {
			continue
		}
		if t, ok := readTree(ep, target); ok {
			trees = append(trees, t)
		}
	}
	return trees, nil
}

// list prints every served tree as its URL and directory, sorted by URL.
// With -a or --share it reads that address only; with a directory it keeps
// the trees at or below it.
func list(c config) int {
	eps, err := endpoints()
	if err != nil {
		return failNow(errnoOf(err), err.Error())
	}
	if c.addr != "" || c.sharing {
		eps = slices.DeleteFunc(eps, func(ep netip.AddrPort) bool { return ep != c.ep })
	}
	under := ""
	if c.dir != "" {
		if under, err = filepath.Abs(c.dir); err != nil {
			return failNow(errnoOf(err), "bad directory: "+err.Error())
		}
		// A directory that is not there names nothing to list; one that
		// is there and holds no served tree lists nothing and succeeds.
		if fi, err := os.Stat(under); err != nil {
			return failNow(errnoOf(err), "no directory "+c.dir)
		} else if !fi.IsDir() {
			return failNow(syscall.ENOTDIR, "not a directory: "+c.dir)
		}
	}
	var trees []servedTree
	for _, ep := range eps {
		ts, err := liveTrees(ep)
		if err != nil {
			return failNow(errnoOf(err), err.Error())
		}
		for _, t := range ts {
			if under == "" || within(t.dir, under) {
				trees = append(trees, t)
			}
		}
	}
	slices.SortFunc(trees, func(a, b servedTree) int { return strings.Compare(a.url, b.url) })
	writeTrees(os.Stdout, trees)
	return 0
}

// writeTrees prints trees as keyval: a tree.N.binary line when the binary
// is known, then a tree.N.dir and a tree.N.url line for each, every index
// the same width, so the lines sort in key order. No trees print nothing,
// which is an empty keyval file.
func writeTrees(w io.Writer, trees []servedTree) {
	width := len(strconv.Itoa(max(len(trees)-1, 0)))
	for i, t := range trees {
		if t.binary != "" {
			fmt.Fprintf(w, "tree.%0*d.binary %s\n", width, i, kvValue(t.binary))
		}
		fmt.Fprintf(w, "tree.%0*d.dir %s\n", width, i, kvValue(t.dir))
		fmt.Fprintf(w, "tree.%0*d.url %s\n", width, i, kvValue(t.url))
	}
}

// kvValue escapes s as a keyval value: backslash, newline, tab and
// carriage return as escapes, and \0 after a value that would otherwise
// end the line in a space or be empty.
func kvValue(s string) string {
	s = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`).Replace(s)
	if s == "" || strings.HasSuffix(s, " ") {
		s += `\0`
	}
	return s
}

func within(dir, root string) bool {
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// endpoints reads every endpoint that has a registry, from directory names
// such as 127.31.1.1-8080.
func endpoints() ([]netip.AddrPort, error) {
	base, err := stateDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(base, progName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var eps []netip.AddrPort
	for _, e := range entries {
		addr, port, ok := strings.Cut(e.Name(), "-")
		a, aerr := netip.ParseAddr(addr)
		n, nerr := strconv.Atoi(port)
		if e.IsDir() && ok && aerr == nil && nerr == nil && n > 0 && n < 1<<16 {
			eps = append(eps, netip.AddrPortFrom(a, uint16(n)))
		}
	}
	return eps, nil
}
