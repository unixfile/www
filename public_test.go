package www

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// readerOnly names code of the features the public executable leaves out:
// the bug button, opening a browser, the stop buttons, the index of
// servers, --restart, -l, --kill, detaching, locks and the registry.
var readerOnly = []string{
	"(*server).handleBug",
	"fileBug",
	"openBrowser",
	"(*server).handleStop",
	"(*server).serveIndex",
	"restart",
	"restartServer",
	"list",
	"kill",
	"detach",
	"createLock",
	"openRegistry",
	"(*hub).adopt",
	"persistedTrees",
	"parseArgs",
}

// The public executable does not contain the reader's features, so no
// flag or request can reach them: the linker leaves out code that no call
// leads to. The reader's executable holds every name in the list, which
// shows that the list names real code.
func TestPublicLeavesOutTheReadersFeatures(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	// Symbols carry the package's import path, which the name test keeps
	// out of string literals.
	pkg := reflect.TypeOf(server{}).PkgPath()
	symbols := func(cmd string) map[string]bool {
		bin := filepath.Join(t.TempDir(), cmd)
		if out, err := exec.Command("go", "build", "-o", bin, "./cmd/"+cmd).CombinedOutput(); err != nil {
			t.Fatalf("go build ./cmd/%s: %v\n%s", cmd, err, out)
		}
		out, err := exec.Command("go", "tool", "nm", bin).Output()
		if err != nil {
			t.Fatalf("go tool nm: %v", err)
		}
		got := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) == 3 {
				got[f[2]] = true
			}
		}
		return got
	}
	local, public := symbols("local"), symbols("public")
	if !public[pkg+".servePublic"] {
		t.Fatalf("the public executable lacks %s.servePublic, so its symbols were not read", pkg)
	}
	for _, name := range readerOnly {
		if !local[pkg+"."+name] {
			t.Errorf("the reader's executable has no %s; the list is out of date", name)
		}
		if public[pkg+"."+name] {
			t.Errorf("the public executable contains %s", name)
		}
	}
}
