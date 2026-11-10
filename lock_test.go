package www

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
	body := fmt.Sprintf("url: http://127.31.1.1:8080/\npid: %d\nboot_id: %s\npid_ns: %s\n", pid, boot, pidNS())
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

// setPIDNS makes the test run as if in namespace ns.
func setPIDNS(t *testing.T, ns string) {
	t.Helper()
	old := pidNS
	pidNS = func() string { return ns }
	t.Cleanup(func() { pidNS = old })
}

// A PID from another namespace says nothing here. Deleting its lock stopped
// a live server when `www -l` ran inside a sandbox. A lock written before
// locks named their namespace counts as the host's, so a sandbox keeps it
// and the host still clears it once its server is gone. A reader that
// cannot name its own namespace can check no PID.
func TestLockInOtherPIDNamespace(t *testing.T) {
	dead := 1<<22 + 1
	const sandbox = "pid:[4026533000]"
	for _, c := range []struct {
		name   string
		reader string
		boot   string
		ns     string
		live   bool
	}{
		{"other namespace", sandbox, bootID(), "pid:[1]", true},
		{"this namespace", sandbox, bootID(), sandbox, false},
		{"no record, read on the host", initPIDNS, bootID(), "", false},
		{"no record, read in a sandbox", sandbox, bootID(), "", true},
		{"reader cannot name its namespace", "", bootID(), sandbox, true},
		{"other namespace, earlier boot", sandbox, "earlier-boot", "pid:[1]", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			setPIDNS(t, c.reader)
			p := filepath.Join(t.TempDir(), lockName(testEP))
			body := fmt.Sprintf("url: http://127.31.1.1:8080/\npid: %d\nboot_id: %s\n", dead, c.boot)
			if c.ns != "" {
				body += "pid_ns: " + c.ns + "\n"
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, ok := findLive(testEP, p); ok != c.live {
				t.Errorf("live %v, want %v", ok, c.live)
			}
			if _, err := os.Stat(p); (err == nil) != c.live {
				t.Errorf("lock kept %v, want %v", err == nil, c.live)
			}
		})
	}
}

// A server here that exits leaves alone the record of a server in another
// namespace that has the same PID.
func TestUnmarkServerSparesOtherNamespace(t *testing.T) {
	setPIDNS(t, "pid:[4026533000]")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	reg, err := openRegistry(testEP)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.close()
	p := filepath.Join(reg.dir, serverFile)
	other := fmt.Sprintf("pid: %d\nboot_id: %s\npid_ns: pid:[1]\n", os.Getpid(), bootID())
	if err := os.WriteFile(p, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	reg.unmarkServer()
	if _, err := os.Stat(p); err != nil {
		t.Fatal("removed another namespace's record")
	}
	if err := reg.markServer(); err != nil {
		t.Fatal(err)
	}
	reg.unmarkServer()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("kept its own record")
	}
}

func TestLockAndRecordNamePIDNamespace(t *testing.T) {
	if pidNS() == "" {
		t.Skip("/proc does not name this namespace")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	l, err := createLock(filepath.Join(dir, lockName(testEP)), dir, "http://127.31.1.1:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if got := parseLock(l.body).pidNS; got != pidNS() {
		t.Errorf("lock names %q, want %q", got, pidNS())
	}
	reg, err := openRegistry(testEP)
	if err != nil {
		t.Fatal(err)
	}
	defer reg.close()
	if err := reg.markServer(); err != nil {
		t.Fatal(err)
	}
	srv, ok := serverOf(testEP)
	if !ok || srv.pidNS != pidNS() {
		t.Errorf("server record: live %v, names %q", ok, srv.pidNS)
	}
}

// A signal to another namespace's PID would reach whichever process holds
// that number here.
func TestStopServerRefusesOtherPIDNamespace(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()
	pid := cmd.Process.Pid
	err := stopServer(testEP, pid, []servedTree{{foreign: true, pid: pid, dir: t.TempDir()}})
	if errnoOf(err) != syscall.EPERM {
		t.Errorf("err %v, want EPERM", err)
	}
	select {
	case <-done:
		t.Error("the process was signalled")
	case <-time.After(100 * time.Millisecond):
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
	got := linkName("/home/u/100%/a")
	if !strings.HasPrefix(got, "%2Fhome%2Fu%2F100%25%2Fa~") || len(got) != len("%2Fhome%2Fu%2F100%25%2Fa~")+8 {
		t.Errorf("link name %q", got)
	}
	if linkName("/a") == linkName("/a") {
		t.Error("two joins of a tree got links of one name")
	}
}

// A deep directory's link still fits in a file name.
func TestLinkNameFitsDeepDir(t *testing.T) {
	dir := "/" + strings.Repeat("deep/", 60)
	if n := len(linkName(dir)); n > 255 {
		t.Errorf("link name of %d bytes", n)
	}
	reg := &registry{dir: t.TempDir()}
	if _, _, err := reg.add(filepath.Join(dir, "127.31.1.1-8080.lock"), ""); err != nil {
		t.Fatal(err)
	}
}

// hasLink reports whether the registry in regDir links a lock in dir.
func hasLink(regDir, dir string) bool {
	links, _ := readLinks(regDir)
	for _, target := range links {
		if lockPath, _ := splitTarget(target); filepath.Dir(lockPath) == dir {
			return true
		}
	}
	return false
}
