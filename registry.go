package main

import (
	"errors"
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

// linkName turns a directory into one file name: % and / become %25 and %2F.
func linkName(dir string) string {
	return strings.NewReplacer("%", "%25", "/", "%2F").Replace(dir)
}

func (r *registry) linkPath(lockPath string) string {
	return filepath.Join(r.dir, linkName(filepath.Dir(lockPath)))
}

// add links lockPath into the registry. An old link for the same tree is
// replaced, so the server can tell a new join from the link it already has.
// A shared tree's secret token follows the lock path after a #, which the
// lock name never holds; the registry is private, so the token stays so.
func (r *registry) add(lockPath, token string) (string, os.FileInfo, error) {
	link := r.linkPath(lockPath)
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, err
	}
	target := lockPath
	if token != "" {
		target += "#" + token
	}
	if err := os.Symlink(target, link); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(link)
	if err != nil {
		return "", nil, err
	}
	return link, info, nil
}

// splitTarget parses a link's target into the lock path and the token.
func splitTarget(target string) (lockPath, token string) {
	if strings.HasSuffix(target, lockSuffix) {
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
