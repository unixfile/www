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
// ~/.local/state/mdserve/127.31.1.1-8080/, with one symlink per lock. A
// joining mdserve adds a link; the server sees it through inotify, writes
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

func openRegistry(ep netip.AddrPort) (*registry, error) {
	base, err := stateDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "mdserve", endpointName(ep))
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
func (r *registry) add(lockPath string) (string, os.FileInfo, error) {
	link := r.linkPath(lockPath)
	if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, err
	}
	if err := os.Symlink(lockPath, link); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(link)
	if err != nil {
		return "", nil, err
	}
	return link, info, nil
}

// links maps every link in the registry to the lock path it names.
func (r *registry) links() (map[string]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink == 0 {
			continue
		}
		link := filepath.Join(r.dir, e.Name())
		if target, err := os.Readlink(link); err == nil {
			m[link] = target
		}
	}
	return m, nil
}
