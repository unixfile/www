package main

import (
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// restart replaces the server on c.ep with one running the current binary
// and serving the same trees. It reads the trees from the registry, sends
// the server SIGTERM, waits for its locks to go and starts each tree again.
func restart(c config) int {
	pid, dirs, err := servedTrees(c.ep)
	if err != nil {
		return failNow(errnoOf(err), err.Error())
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return failNow(errnoOf(err), "cannot stop the server on "+c.ep.String()+": "+err.Error())
	}
	if !waitGone(dirs, c.ep) {
		return failNow(syscall.ETIMEDOUT, "the server on "+c.ep.String()+" kept its locks")
	}
	exe, err := os.Executable()
	if err != nil {
		return failNow(errnoOf(err), "cannot find own executable: "+err.Error())
	}
	code := 0
	for _, dir := range dirs {
		cmd := exec.Command(exe, "-n", "-a", c.ep.String(), "--", dir)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil && code == 0 {
			code = int(syscall.EIO)
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
	}
	return code
}

// servedTrees returns the server's PID and the trees it serves.
func servedTrees(ep netip.AddrPort) (int, []string, error) {
	trees, err := liveTrees(ep)
	if err != nil {
		return 0, nil, err
	}
	if len(trees) == 0 {
		return 0, nil, &errno{syscall.ESRCH, "no server runs on " + ep.String()}
	}
	dirs := make([]string, len(trees))
	for i, t := range trees {
		dirs[i] = t.dir
	}
	slices.Sort(dirs)
	return trees[0].pid, dirs, nil
}

// waitGone waits for the server to withdraw every tree. It closes its
// listener as it does, so a new server can bind before the old one exits.
func waitGone(dirs []string, ep netip.AddrPort) bool {
	for end := time.Now().Add(joinWait); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if !slices.ContainsFunc(dirs, func(dir string) bool {
			_, err := os.Lstat(filepath.Join(dir, lockName(ep)))
			return !errors.Is(err, fs.ErrNotExist)
		}) {
			return true
		}
	}
	return false
}
