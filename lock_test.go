package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsLockName(t *testing.T) {
	for name, want := range map[string]bool{
		"serving-port-7417.lock": true,
		"serving-port-.lock":     false,
		"serving-port-x.lock":    false,
		"serving-port-7417":      false,
		"index.md":               false,
	} {
		if got := isLockName(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

func writeLock(t *testing.T, dir string, pid int, boot string) string {
	t.Helper()
	p := filepath.Join(dir, "serving-port-1.lock")
	body := fmt.Sprintf("url: http://127.0.0.1:1/\npid: %d\nboot_id: %s\n", pid, boot)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLiveLockFound(t *testing.T) {
	dir := t.TempDir()
	writeLock(t, dir, os.Getpid(), bootID())
	if _, ok := findLive(dir); !ok {
		t.Fatal("own live lock not found")
	}
}

// A lock from an earlier boot is stale even if its PID is alive now.
func TestLockFromEarlierBootRemoved(t *testing.T) {
	dir := t.TempDir()
	p := writeLock(t, dir, os.Getpid(), "earlier-boot")
	if _, ok := findLive(dir); ok {
		t.Fatal("lock from an earlier boot counted as live")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("stale lock not removed")
	}
}

func TestLockOfDeadProcessRemoved(t *testing.T) {
	dir := t.TempDir()
	p := writeLock(t, dir, 1<<22+1, bootID())
	if _, ok := findLive(dir); ok {
		t.Fatal("lock of a dead process counted as live")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("stale lock not removed")
	}
}

func TestWatchStopsWhenLockReplaced(t *testing.T) {
	dir := t.TempDir()
	l, err := createLock(dir, 1, "http://127.0.0.1:1/")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(l.path)
	os.WriteFile(l.path, []byte("other\n"), 0o644)
	stopped := make(chan struct{})
	go l.watch(context.Background(), 10*time.Millisecond, func() { close(stopped) })
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("replaced lock did not stop the server")
	}
	l.remove()
	if _, err := os.Stat(l.path); err != nil {
		t.Error("remove deleted a lock it does not own")
	}
}
