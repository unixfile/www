package main

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestIsLockName(t *testing.T) {
	for name, want := range map[string]bool{
		"127.31.1.1-8080.lock":   true,
		"127.0.0.1-1.lock":       true,
		"127.31.1.1-8080":        false,
		"127.31.1.1-.lock":       false,
		"127.31.1.1-99999.lock":  false,
		"127.31.1-8080.lock":     false,
		"::1-8080.lock":          false,
		"serving-port-7417.lock": false,
		"index.md":               false,
	} {
		if got := isLockName(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

var testEP = netip.MustParseAddrPort("127.31.1.1:8080")

func TestLockNameIsEndpoint(t *testing.T) {
	ep := netip.MustParseAddrPort("127.31.1.1:8080")
	if got := lockName(ep); got != "127.31.1.1-8080.lock" {
		t.Errorf("lock name %q", got)
	}
}

func writeLock(t *testing.T, dir string, pid int, boot string) string {
	t.Helper()
	p := filepath.Join(dir, "127.31.1.1-8080.lock")
	body := fmt.Sprintf("url: http://127.31.1.1:8080/\npid: %d\nboot_id: %s\n", pid, boot)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLiveLockFound(t *testing.T) {
	p := writeLock(t, t.TempDir(), os.Getpid(), bootID())
	if _, ok := findLive(testEP, p); !ok {
		t.Fatal("own live lock not found")
	}
}

// A lock from an earlier boot is stale even if its PID is alive now.
func TestLockFromEarlierBootRemoved(t *testing.T) {
	p := writeLock(t, t.TempDir(), os.Getpid(), "earlier-boot")
	if _, ok := findLive(testEP, p); ok {
		t.Fatal("lock from an earlier boot counted as live")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("stale lock not removed")
	}
}

func TestLockOfDeadProcessRemoved(t *testing.T) {
	p := writeLock(t, t.TempDir(), 1<<22+1, bootID())
	if _, ok := findLive(testEP, p); ok {
		t.Fatal("lock of a dead process counted as live")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("stale lock not removed")
	}
}

func TestRemoveSparesReplacedLock(t *testing.T) {
	dir := t.TempDir()
	l, err := createLock(filepath.Join(dir, "127.31.1.1-8080.lock"), dir, "http://127.31.1.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(l.path)
	os.WriteFile(l.path, []byte("other\n"), 0o644)
	if !l.gone() {
		t.Fatal("replaced lock not seen as gone")
	}
	l.remove()
	if _, err := os.Stat(l.path); err != nil {
		t.Error("remove deleted a lock it does not own")
	}
}

func TestLinkNameEscapes(t *testing.T) {
	if got := linkName("/home/u/100%/a"); got != "%2Fhome%2Fu%2F100%25%2Fa" {
		t.Errorf("link name %q", got)
	}
}
