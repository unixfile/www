package www

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestForeignHostRejected(t *testing.T) {
	s, _, _ := serveTree(t)
	if rec := get(s.handler(), "/", "Host", "evil.example:7417"); rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want 421", rec.Code)
	}
	for _, host := range []string{"localhost:9", "[::1]:7417", "127.0.0.1"} {
		if rec := get(s.handler(), "/bin.dat", "Host", host); rec.Code != http.StatusOK {
			t.Errorf("host %s: status %d, want 200", host, rec.Code)
		}
	}
}

func TestLocalHost(t *testing.T) {
	hosts := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(hosts, []byte("127.31.1.1 docs docs.local # "+progName+"\n192.168.1.5 nas\n# 127.0.0.1 commented\n"), 0o644)
	old := hostsFile
	hostsFile = hosts
	t.Cleanup(func() { hostsFile = old })
	for host, want := range map[string]bool{
		"127.31.1.1": true, "127.31.200.1": true, "127.0.0.1": true, "::1": true, "localhost": true,
		"docs": true, "DOCS.local": true,
		"nas": false, "commented": false, "evil.example": false, "10.0.0.1": false,
	} {
		if got := localHost(host); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
}
