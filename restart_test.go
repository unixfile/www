package main

import (
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A server records its binary, which outlives it. --restart leaves the
// server and the persistent trees of another binary that still exists,
// and takes those of a binary that is gone.
func TestRestartLeavesOtherBinaries(t *testing.T) {
	ep := testEndpoint(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	base := "http://" + ep.String()
	reg := filepath.Join(os.Getenv("XDG_STATE_HOME"), progName, endpointName(ep))
	record := filepath.Join(reg, binaryFile)
	exe, _ := os.Executable()

	_, served := start(t, config{dir: dir, ep: ep, persist: true})
	if got := binaryOf(ep); got != exe {
		t.Fatalf("binary record %q, want %q", got, exe)
	}
	if trees, _ := liveTrees(ep); len(trees) != 1 || trees[0].binary != exe {
		t.Errorf("listed trees: %+v", trees)
	}
	if got := otherBinary(ep); got != "" {
		t.Errorf("own binary reads as other: %q", got)
	}

	// /bin/sh stands in for the package's binary.
	os.WriteFile(record, []byte("/bin/sh\n"), 0o600)
	if got := otherBinary(ep); got != "/bin/sh" {
		t.Fatalf("other binary %q, want /bin/sh", got)
	}
	if code := restart(config{restart: true}); code != 0 {
		t.Errorf("restart exit %d", code)
	}
	select {
	case <-served:
		t.Fatal("restart stopped the server of another binary")
	case <-time.After(200 * time.Millisecond):
	}
	if status(t, base+dir+"/a.txt?raw") != http.StatusOK {
		t.Error("the other binary's tree went")
	}

	syscall.Kill(os.Getpid(), syscall.SIGHUP)
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
	os.WriteFile(record, []byte("/bin/sh\n"), 0o600)
	if code := restart(config{restart: true}); code != 0 {
		t.Errorf("restart exit %d", code)
	}
	if _, ok := readLive(ep, filepath.Join(dir, persistName(ep))); ok {
		t.Error("restart served the persistent trees of another binary")
	}

	os.WriteFile(record, []byte(filepath.Join(t.TempDir(), "gone")+"\n"), 0o600)
	if got := otherBinary(ep); got != "" {
		t.Errorf("a gone binary reads as other: %q", got)
	}
	os.Remove(record)
	if got := otherBinary(ep); got != "" {
		t.Errorf("no record reads as other: %q", got)
	}
}

func TestForceGoesWithRestart(t *testing.T) {
	if _, err := parseArgs([]string{"-f", "."}); err == nil {
		t.Error("-f without --restart parsed")
	}
	c, err := parseArgs([]string{"--restart", "-f"})
	if err != nil || !c.force {
		t.Errorf("--restart -f: %+v, %v", c, err)
	}
}
