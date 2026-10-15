package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A lockfile marks a served directory. Its name carries the port, its body
// says how to reach and stop the server, and deleting it stops the server.
const (
	lockPrefix = "serving-port-"
	lockSuffix = ".lock"
)

func isLockName(name string) bool {
	port, ok := strings.CutPrefix(name, lockPrefix)
	if !ok {
		return false
	}
	port, ok = strings.CutSuffix(port, lockSuffix)
	if !ok || port == "" {
		return false
	}
	_, err := strconv.Atoi(port)
	return err == nil
}

type lockInfo struct {
	pid    int
	url    string
	bootID string
}

func parseLock(b []byte) lockInfo {
	var l lockInfo
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch k {
		case "pid":
			l.pid, _ = strconv.Atoi(v)
		case "url":
			l.url = v
		case "boot_id":
			l.bootID = v
		}
	}
	return l
}

// bootID changes on every boot, so a lock from before a reboot is stale
// even when its PID now belongs to a live process.
func bootID() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
}

func (l lockInfo) live(boot string) bool {
	if l.pid <= 0 || l.url == "" {
		return false
	}
	if l.bootID != "" && boot != "" && l.bootID != boot {
		return false
	}
	err := syscall.Kill(l.pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// findLive returns the lock of a server already running in dir. It deletes
// stale locks on the way, so a crash leaves nothing behind for long.
func findLive(dir string) (lockInfo, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return lockInfo{}, false
	}
	boot := bootID()
	for _, e := range entries {
		if !isLockName(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if l := parseLock(b); l.live(boot) {
			return l, true
		}
		os.Remove(path)
	}
	return lockInfo{}, false
}

type lock struct {
	path string
	info os.FileInfo
}

func createLock(dir string, port int, url string) (*lock, error) {
	path := filepath.Join(dir, fmt.Sprintf("%s%d%s", lockPrefix, port, lockSuffix))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintf(f, "# mdserve is serving this directory. Delete this file to stop it.\n"+
		"url: %s\nroot: %s\npid: %d\nstarted: %s\nboot_id: %s\n",
		url, dir, os.Getpid(), time.Now().Format(time.RFC3339), bootID())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &lock{path: path, info: info}, nil
}

// gone reports whether the lock was deleted or replaced by another file.
func (l *lock) gone() bool {
	info, err := os.Stat(l.path)
	return err != nil || !os.SameFile(info, l.info)
}

// remove deletes the lock unless it is already gone, so it never deletes
// a lock that belongs to another server.
func (l *lock) remove() {
	if !l.gone() {
		os.Remove(l.path)
	}
}

// watch calls stop once the lock is gone. It polls; a second of delay on
// shutdown costs nothing and needs no inotify.
func (l *lock) watch(ctx context.Context, every time.Duration, stop func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if l.gone() {
				stop()
				return
			}
		}
	}
}
