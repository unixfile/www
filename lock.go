package www

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A lockfile marks a served tree. Its name is the endpoint that serves it,
// eg 127.31.1.1-8080.lock, its body says how to reach and stop the server,
// and deleting it withdraws the tree. A persistent lock, written by
// --persist, ends in capitals, eg 127.31.1.1-8080.LOCK: it holds no PID,
// outlives its server and a reboot, and the next server on its endpoint
// serves the tree again.
const (
	lockSuffix    = ".lock"
	persistSuffix = ".LOCK"
)

// endpointName joins address and port with a dash. A colon trips up shell
// completion, scp, rsync and Make.
func endpointName(ep netip.AddrPort) string {
	return fmt.Sprintf("%s-%d", ep.Addr(), ep.Port())
}

func lockName(ep netip.AddrPort) string {
	return endpointName(ep) + lockSuffix
}

func persistName(ep netip.AddrPort) string {
	return endpointName(ep) + persistSuffix
}

// isPersist reports whether a lock path names a persistent lock.
func isPersist(path string) bool {
	return strings.HasSuffix(path, persistSuffix)
}

// lockNames reports whether base is either lock of ep.
func lockNames(ep netip.AddrPort, base string) bool {
	return base == lockName(ep) || base == persistName(ep)
}

func isLockName(name string) bool {
	base, ok := strings.CutSuffix(name, lockSuffix)
	if !ok {
		if base, ok = strings.CutSuffix(name, persistSuffix); !ok {
			return false
		}
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
	pidNS  string    // the PID namespace pid means something in
	since  time.Time // when a server record was written, as the server started
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
		case "pid_ns":
			l.pidNS = v
		}
	}
	return l
}

// bootID changes on every boot, so a lock from before a reboot is stale
// even when its PID now belongs to a live process. It holds for the life
// of the process, so it is read once.
var bootID = sync.OnceValue(func() string {
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b))
})

// pidNS names this process's PID namespace, or "" where /proc cannot say,
// eg where it is not mounted. It is read once. Tests replace it.
var pidNS = sync.OnceValue(func() string {
	ns, _ := os.Readlink("/proc/self/ns/pid")
	return ns
})

// initPIDNS names the initial PID namespace, the host's own. The kernel
// gives it a fixed inode, PROC_PID_INIT_INO.
const initPIDNS = "pid:[4026531836]"

func (l lockInfo) live() bool {
	return l.url != "" && l.running()
}

// foreign reports whether l's PID cannot be checked from here: the server
// wrote it in another PID namespace, eg outside the sandbox or container
// this process runs in, or this process cannot name its own. A lock with
// no record was written before locks named their namespace, most likely
// by a server on the host, so it counts as the initial namespace's.
func (l lockInfo) foreign() bool {
	ns := l.pidNS
	if ns == "" {
		ns = initPIDNS
	}
	return ns != pidNS()
}

// running reports whether l's server still runs. A PID from an earlier boot
// is dead. A PID from another namespace cannot be checked from here, and
// wrongly calling it dead would delete a live server's lock and stop it, so
// it counts as running. Its lock then stays until a reboot or until someone
// deletes it, which costs less.
func (l lockInfo) running() bool {
	if l.pid <= 0 {
		return false
	}
	if now := bootID(); l.bootID != "" && now != "" && l.bootID != now {
		return false
	}
	if l.foreign() {
		return true
	}
	err := syscall.Kill(l.pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// readLive reads the lock at path and reports whether its server runs. A
// persistent lock holds no PID, so it takes the PID of the server that
// the registry of ep names. That server serves the tree only if it wrote
// the lock, after it started: a lock from an earlier server, kept for
// the next, may name a URL that another tree has taken since.
func readLive(ep netip.AddrPort, path string) (lockInfo, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockInfo{}, false
	}
	l := parseLock(b)
	if isPersist(path) {
		srv, ok := serverOf(ep)
		if !ok || l.url == "" {
			return l, false
		}
		info, err := os.Stat(path)
		if err != nil || info.ModTime().Before(srv.since) {
			return l, false
		}
		l.pid, l.bootID, l.pidNS = srv.pid, srv.bootID, srv.pidNS
	}
	return l, l.live()
}

// findLive returns the lock at path if its server still runs. A stale lock
// is deleted on the way, so a crash leaves nothing behind for long; a
// persistent one stays for the next server. Callers hold the registry
// lock, so the server cannot be halfway through writing it.
func findLive(ep netip.AddrPort, path string) (lockInfo, bool) {
	l, ok := readLive(ep, path)
	if ok || isPersist(path) {
		return l, ok
	}
	if os.Remove(path) == nil {
		logInfo("removed stale lock %s: pid %d is gone or from an earlier boot", path, l.pid)
	}
	return lockInfo{}, false
}

type lock struct {
	path string
	info os.FileInfo
	body []byte // what this server wrote, to tell its lock from a new one
}

// createLock writes the lock at path. A persistent lock that is already
// there, from an earlier server, is rewritten in place, so its URL is the
// one this server answers on.
func createLock(path, root, url string) (*lock, error) {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if isPersist(path) {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return nil, err
	}
	var body string
	if isPersist(path) {
		body = fmt.Sprintf("# "+progName+" serves this directory, after reboot too. Delete this file to stop serving it.\n"+
			"url: %s\nroot: %s\nstarted: %s\n", url, root, time.Now().Format(time.RFC3339))
	} else {
		body = fmt.Sprintf("# "+progName+" is serving this directory. Delete this file to stop serving it.\n"+
			"url: %s\nroot: %s\npid: %d\nstarted: %s\nboot_id: %s\npid_ns: %s\n",
			url, root, os.Getpid(), time.Now().Format(time.RFC3339), bootID(), pidNS())
	}
	_, err = f.WriteString(body)
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
	return &lock{path: path, info: info, body: []byte(body)}, nil
}

// gone reports whether the lock was deleted or replaced by another file.
// The file's identity alone cannot tell: ext4 hands a new file the inode
// of one just deleted, so a replacement can look like the same file. The
// body tells, since another server's lock holds another PID or start.
func (l *lock) gone() bool {
	info, err := os.Stat(l.path)
	if err != nil || !os.SameFile(info, l.info) {
		return true
	}
	b, err := os.ReadFile(l.path)
	return err != nil || !bytes.Equal(b, l.body)
}

// persist reports whether the lock outlives its server.
func (l *lock) persist() bool {
	return isPersist(l.path)
}

// remove deletes the lock unless it is already gone, so it never deletes
// a lock that belongs to another server.
func (l *lock) remove() {
	if !l.gone() {
		os.Remove(l.path)
	}
}
