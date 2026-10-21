package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A persistent lock holds no PID, outlives its server and brings its tree
// back with the next server on its address. Deleting it ends the tree.
func TestPersistentLock(t *testing.T) {
	ep := testEndpoint(t)
	one, two := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(one, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(two, "b.txt"), []byte("two\n"), 0o644)
	base := "http://" + ep.String()
	kept := filepath.Join(one, persistName(ep))
	reg := filepath.Join(os.Getenv("XDG_STATE_HOME"), progName, endpointName(ep))

	lines, served := start(t, config{dir: one, ep: ep, persist: true})
	if !strings.HasPrefix(lines[0], "url "+base+one+"/") {
		t.Fatalf("server report: %q", lines)
	}
	b, err := os.ReadFile(kept)
	if err != nil || strings.Contains(string(b), "pid:") {
		t.Fatalf("persistent lock %q, %v", b, err)
	}
	if _, ok := readLive(ep, kept); !ok {
		t.Error("persistent lock of a running server reads as dead")
	}
	lines, again := start(t, config{dir: one, ep: ep})
	if <-again != 0 || lines[0] != "url "+base+one+"/" {
		t.Errorf("plain start in a persisted tree: %q", lines)
	}

	// SIGHUP stops the server as SIGTERM does, without ending the test.
	syscall.Kill(os.Getpid(), syscall.SIGHUP)
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal("stopping the server removed the persistent lock")
	}
	if _, err := os.Lstat(filepath.Join(reg, linkName(one))); err != nil {
		t.Fatal("stopping the server removed the persistent link")
	}
	if _, ok := readLive(ep, kept); ok {
		t.Error("persistent lock of a stopped server reads as live")
	}
	if got := persistedTrees(ep); len(got) != 1 || got[0].dir != one {
		t.Errorf("persisted trees: %+v", got)
	}

	// A plain start in the kept tree keeps its lock persistent.
	_, served = start(t, config{dir: one, ep: ep})
	if _, err := os.Stat(filepath.Join(one, lockName(ep))); err == nil {
		t.Error("a plain start beside a .LOCK wrote a .lock")
	}
	if _, ok := readLive(ep, kept); !ok {
		t.Error("a plain start beside a .LOCK did not serve it")
	}
	syscall.Kill(os.Getpid(), syscall.SIGHUP)
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}

	// The next server on the address serves the kept tree again.
	_, served = start(t, config{dir: two, ep: ep})
	waitFor(t, "the kept tree to come back", func() bool { return status(t, base+one+"/a.txt?raw") == http.StatusOK })

	os.Remove(kept)
	waitFor(t, "the kept tree to go", func() bool { return status(t, base+one+"/a.txt?raw") == http.StatusNotFound })
	if _, err := os.Lstat(filepath.Join(reg, linkName(one))); err == nil {
		t.Error("a deleted persistent lock keeps its link")
	}
	os.Remove(filepath.Join(two, lockName(ep)))
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("server outlived its last lock")
	}
}

// --persist on a tree served until reboot moves it to a persistent lock.
func TestPersistUpgradesLock(t *testing.T) {
	ep := testEndpoint(t)
	one, two := t.TempDir(), t.TempDir()
	base := "http://" + ep.String()
	_, served := start(t, config{dir: two, ep: ep})
	lines, code := start(t, config{dir: one, ep: ep})
	if <-code != 0 {
		t.Fatalf("join: %q", lines)
	}
	lines, code = start(t, config{dir: one, ep: ep, persist: true})
	if <-code != 0 || lines[0] != "url "+base+one+"/" {
		t.Fatalf("persist: %q", lines)
	}
	if _, err := os.Stat(filepath.Join(one, lockName(ep))); err == nil {
		t.Error("the plain lock stayed")
	}
	if _, ok := readLive(ep, filepath.Join(one, persistName(ep))); !ok {
		t.Error("no live persistent lock")
	}
	os.Remove(filepath.Join(one, persistName(ep)))
	os.Remove(filepath.Join(two, lockName(ep)))
	select {
	case <-served:
	case <-time.After(3 * time.Second):
		t.Fatal("server outlived its last lock")
	}
}
