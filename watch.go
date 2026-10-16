package main

import (
	"errors"
	"os"
	"sync/atomic"
	"syscall"
)

// watcher wakes the server when a registry link, a lock or a shown file may
// have changed. It reads inotify through the standard library and does not
// parse events: any change in a watched directory means a rescan, which
// costs a few stats. Where inotify fails or runs out of watches, the server
// polls instead.
//
// Each watcher has an inotify instance of its own. Within one instance a
// second watch on a directory replaces the first, so the page watches of
// live reload would otherwise end the lock watch on a tree's root.
type watcher struct {
	fd   int
	f    *os.File // nil when inotify is unavailable
	mask uint32
	wake chan struct{}
	poll atomic.Bool
}

// lockMask sees links and locks come and go.
const lockMask = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_FROM |
	syscall.IN_MOVED_TO | syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF | syscall.IN_ONLYDIR

// pageMask also sees a file written in place, and touch.
const pageMask = lockMask | syscall.IN_CLOSE_WRITE | syscall.IN_ATTRIB

// inotifyInit is replaced in tests to force polling.
var inotifyInit = syscall.InotifyInit1

func newWatcher(mask uint32) *watcher {
	w := &watcher{fd: -1, mask: mask, wake: make(chan struct{}, 1)}
	fd, err := inotifyInit(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		logErr("inotify unavailable, polling every second: %v", err)
		w.poll.Store(true)
		return w
	}
	// A non-blocking descriptor makes the File pollable, so Close ends a
	// pending Read. The raw fd serves the watch calls; Fd would make the
	// File blocking again.
	w.fd, w.f = fd, os.NewFile(uintptr(fd), "inotify")
	go w.read()
	return w
}

func (w *watcher) read() {
	buf := make([]byte, 4096)
	for {
		if _, err := w.f.Read(buf); err != nil {
			return
		}
		w.signal()
	}
}

func (w *watcher) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// add watches dir and returns its watch descriptor, or -1. A dir that is
// missing needs no watch; any other failure, such as running out of
// watches, means the server has to poll.
func (w *watcher) add(dir string) int {
	if w.f == nil {
		return -1
	}
	wd, err := syscall.InotifyAddWatch(w.fd, dir, w.mask)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR) {
		return -1
	}
	if err != nil {
		if !w.poll.Swap(true) {
			logErr("cannot watch %s, polling every second: %v", dir, err)
		}
		return -1
	}
	return wd
}

func (w *watcher) remove(wd int) {
	if w.f != nil && wd >= 0 {
		syscall.InotifyRmWatch(w.fd, uint32(wd))
	}
}

func (w *watcher) close() {
	if w.f != nil {
		w.f.Close()
	}
}
