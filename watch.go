package main

import (
	"os"
	"sync/atomic"
	"syscall"
)

// watcher wakes the server when a registry link or a lock may have changed.
// It reads inotify through the standard library and does not parse events:
// any change in a watched directory means a rescan, which costs a few stats.
// Where inotify fails or runs out of watches, the server polls instead.
type watcher struct {
	fd   int
	f    *os.File // nil when inotify is unavailable
	wake chan struct{}
	poll atomic.Bool
}

const watchMask = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_FROM |
	syscall.IN_MOVED_TO | syscall.IN_DELETE_SELF | syscall.IN_MOVE_SELF | syscall.IN_ONLYDIR

// inotifyInit is replaced in tests to force polling.
var inotifyInit = syscall.InotifyInit1

func newWatcher() *watcher {
	w := &watcher{fd: -1, wake: make(chan struct{}, 1)}
	fd, err := inotifyInit(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
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

// add watches dir and returns its watch descriptor, or -1 if the server
// has to poll.
func (w *watcher) add(dir string) int {
	if w.f == nil {
		return -1
	}
	wd, err := syscall.InotifyAddWatch(w.fd, dir, watchMask)
	if err != nil {
		w.poll.Store(true)
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
