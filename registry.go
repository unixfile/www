package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// A registry directory joins trees to the server of one endpoint, eg
// ~/.local/state/www/127.31.1.1-8080/, with one symlink per lock. A
// joining www adds a link; the server sees it through inotify, writes
// the lock and serves the tree. An flock on the directory orders a join
// against the server's start and exit, so no tree joins a dying server.
type registry struct {
	dir string
	f   *os.File
}

func stateDir() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

func registryDir(ep netip.AddrPort) (string, error) {
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, progName, endpointName(ep)), nil
}

func openRegistry(ep netip.AddrPort) (*registry, error) {
	dir, err := registryDir(ep)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	return &registry{dir: dir, f: f}, nil
}

func (r *registry) lock() error {
	return flock(r.f, syscall.LOCK_EX)
}

func (r *registry) unlock() {
	flock(r.f, syscall.LOCK_UN)
}

// close also releases the flock.
func (r *registry) close() {
	r.f.Close()
}

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// linkName turns a directory into one file name for its link: % and /
// become %25 and %2F, cut short to keep within NAME_MAX, and a random
// suffix follows. So every join makes a link of a new name, which the
// server tells from the link it already has without trusting the inode,
// which a new file may reuse. The name only helps a reader of the
// registry; www reads the tree from the link's target.
func linkName(dir string) string {
	name := strings.NewReplacer("%", "%25", "/", "%2F").Replace(dir)
	if len(name) > linkNameMax {
		name = name[:linkNameMax]
	}
	return name + "~" + rand.Text()[:8]
}

// linkNameMax leaves room under NAME_MAX, 255 bytes, for the suffix.
const linkNameMax = 200

// add links lockPath into the registry. Any older link for the same tree
// goes, and the new one takes a new name, so the server can tell a new
// join from the link it already has.
// A shared tree's secret token follows the lock path after a #, which the
// lock name never holds; the registry is private, so the token stays so.
func (r *registry) add(lockPath, token string) (string, os.FileInfo, error) {
	links, err := r.links()
	if err != nil {
		return "", nil, err
	}
	for link, target := range links {
		if old, _ := splitTarget(target); filepath.Dir(old) == filepath.Dir(lockPath) {
			if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return "", nil, err
			}
		}
	}
	target := lockPath
	if token != "" {
		target += "#" + token
	}
	link := filepath.Join(r.dir, linkName(filepath.Dir(lockPath)))
	if err := os.Symlink(target, link); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(link)
	if err != nil {
		os.Remove(link)
		return "", nil, err
	}
	return link, info, nil
}

// splitTarget parses a link's target into the lock path and the token.
func splitTarget(target string) (lockPath, token string) {
	if strings.HasSuffix(target, lockSuffix) || strings.HasSuffix(target, persistSuffix) {
		return target, ""
	}
	i := strings.LastIndex(target, "#")
	if i < 0 {
		return target, ""
	}
	return target[:i], target[i+1:]
}

// serves reports whether a live server holds a tree in the registry of ep.
// The caller holds the lock.
func (r *registry) serves(ep netip.AddrPort) bool {
	links, _ := r.links()
	for _, target := range links {
		if _, ok := readTree(ep, target); ok {
			return true
		}
	}
	return false
}

// links maps every link in the registry to its target, which splitTarget
// parses.
func (r *registry) links() (map[string]string, error) {
	return readLinks(r.dir)
}

func readLinks(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		link := filepath.Join(dir, e.Name())
		if target, err := os.Readlink(link); err == nil {
			m[link] = target
		}
	}
	return m, nil
}

// serverFile, in a registry, names the server that runs on its endpoint:
// its PID and boot, as a lock does. A persistent lock holds no PID, so this
// is how to tell whether its tree is served and which process to stop.
const serverFile = "server"

// pagesFile, in a registry, lists the path of each page that held a live
// stream when its server stopped, one per line. --restart creates it empty
// before it stops the server and reads it after, to say which pages
// reload onto the new server and whether a tree needs a browser opened. A
// server stopped any other way finds no file and writes none.
const pagesFile = "pages"

// writePages records the pages open on a stopping server, when a restart
// has asked for them.
func (r *registry) writePages(paths []string) {
	f, err := os.OpenFile(filepath.Join(r.dir, pagesFile), os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return
	}
	defer f.Close()
	for _, p := range paths {
		f.WriteString(p + "\n")
	}
}

// markServer records this process as the server of the registry.
func (r *registry) markServer() error {
	return os.WriteFile(filepath.Join(r.dir, serverFile),
		[]byte(fmt.Sprintf("pid: %d\nboot_id: %s\n", os.Getpid(), bootID())), 0o600)
}

// unmarkServer removes the record, unless another server has written its
// own.
func (r *registry) unmarkServer() {
	p := filepath.Join(r.dir, serverFile)
	if b, err := os.ReadFile(p); err == nil && parseLock(b).pid == os.Getpid() {
		os.Remove(p)
	}
}

// serverOf reads the server record of ep, and reports whether that server
// still runs.
func serverOf(ep netip.AddrPort) (lockInfo, bool) {
	dir, err := registryDir(ep)
	if err != nil {
		return lockInfo{}, false
	}
	f, err := os.Open(filepath.Join(dir, serverFile))
	if err != nil {
		return lockInfo{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return lockInfo{}, false
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return lockInfo{}, false
	}
	l := parseLock(b)
	l.since = info.ModTime()
	return l, pidLive(l.pid, l.bootID, bootID())
}
