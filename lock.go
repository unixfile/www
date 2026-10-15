package main

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A lockfile marks a served tree. Its name is the endpoint that serves it,
// eg 127.31.1.1-8080.lock, its body says how to reach and stop the server,
// and deleting it withdraws the tree.
const lockSuffix = ".lock"

// endpointName joins address and port with a dash. A colon trips up shell
// completion, scp, rsync and Make.
func endpointName(ep netip.AddrPort) string {
	return fmt.Sprintf("%s-%d", ep.Addr(), ep.Port())
}

func lockName(ep netip.AddrPort) string {
	return endpointName(ep) + lockSuffix
}

func isLockName(name string) bool {
	base, ok := strings.CutSuffix(name, lockSuffix)
	if !ok {
		return false
	}
	// An IPv4 address holds no dash, so the first one ends it.
	addr, port, ok := strings.Cut(base, "-")
	if !ok {
		return false
	}
	a, err := netip.ParseAddr(addr)
	if err != nil || !a.Is4() {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n < 1<<16
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

// findLive returns the lock at path if its server still runs. A stale lock
// is deleted on the way, so a crash leaves nothing behind for long. Callers
// hold the registry lock, so the server cannot be halfway through writing it.
func findLive(path string) (lockInfo, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockInfo{}, false
	}
	if l := parseLock(b); l.live(bootID()) {
		return l, true
	}
	os.Remove(path)
	return lockInfo{}, false
}

type lock struct {
	path string
	info os.FileInfo
}

func createLock(path, root, url string) (*lock, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintf(f, "# mdserve is serving this directory. Delete this file to stop serving it.\n"+
		"url: %s\nroot: %s\npid: %d\nstarted: %s\nboot_id: %s\n",
		url, root, os.Getpid(), time.Now().Format(time.RFC3339), bootID())
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
