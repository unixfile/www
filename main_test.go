package main

import (
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
)

// Options parse the GNU way: short ones combine, values sit joined or apart,
// and options may follow the directory.
func TestParseArgs(t *testing.T) {
	ok := []struct {
		args []string
		want config
	}{
		{nil, config{dir: "."}},
		{[]string{"--foreground", "--stamp"}, config{dir: ".", fg: true, stamp: true}},
		{[]string{"--cache=64M"}, config{dir: ".", cache: "64M"}},
		{[]string{"--cache", "0"}, config{dir: ".", cache: "0"}},
		{[]string{"docs", "--foreground", "--no-open"}, config{dir: "docs", fg: true, noopen: true}},
		{[]string{"-a", "127.31.1.2"}, config{dir: ".", addr: "127.31.1.2"}},
		{[]string{"-a127.31.1.2:9000"}, config{dir: ".", addr: "127.31.1.2:9000"}},
		{[]string{"-pa", "127.31.1.2"}, config{dir: ".", persist: true, addr: "127.31.1.2"}},
		{[]string{"--addr", "127.31.1.2"}, config{dir: ".", addr: "127.31.1.2"}},
		{[]string{"--addr=127.31.1.2"}, config{dir: ".", addr: "127.31.1.2"}},
		{[]string{"--", "-f"}, config{dir: "-f"}},
		{[]string{"-h"}, config{dir: ".", help: true}},
		{[]string{"--help"}, config{dir: ".", help: true}},
		{[]string{"--version"}, config{dir: ".", version: true}},
		{[]string{"-V"}, config{dir: ".", version: true}},
		{[]string{"--restart", "-a", "127.31.1.2"}, config{addr: "127.31.1.2", restart: true}},
		{[]string{"--restart", "-a", "127.31.1.2", "."}, config{dir: ".", addr: "127.31.1.2", restart: true}},
		{[]string{"-l"}, config{list: true}},
		{[]string{"--list", "docs"}, config{dir: "docs", list: true}},
		{[]string{"--share=0.0.0.0"}, config{dir: ".", sharing: true, share: "0.0.0.0"}},
		{[]string{"--share"}, config{dir: ".", sharing: true}},
		{[]string{"--share", "docs"}, config{dir: "docs", sharing: true}},
		{[]string{"--share=10.0.0.1:9000", "--secret", "docs"}, config{dir: "docs", sharing: true, share: "10.0.0.1:9000", secret: true}},
		{[]string{"--restart", "--share=10.0.0.1"}, config{sharing: true, share: "10.0.0.1", restart: true}},
		{[]string{"--kill"}, config{kill: true}},
		{[]string{"--commit=261024.4"}, config{dir: ".", commit: "261024.4"}},
		{[]string{"--commit", "HEAD", "docs"}, config{dir: "docs", commit: "HEAD"}},
		{[]string{"--kill", "--share"}, config{sharing: true, kill: true}},
	}
	for _, tc := range ok {
		got, err := parseArgs(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%q: %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
	for _, args := range [][]string{
		{"-x"},
		{"-f"},
		{"-n"},
		{"-fn"},
		{"-fg"},
		{"-nolock"},
		{"--no-lock"},
		{"--no-breaks"},
		{"--fg"},
		{"--foreground=yes"},
		{"-a"},
		{"--addr"},
		{"--addr="},
		{"one", "two"},
		{"--restart", "--foreground"},
		{"-l", "--restart"},
		{"--share="},
		{"--share", "a", "b"},
		{"-a", "127.31.1.2", "--share"},
		{"--secret"},
		{"--share", "--secret=k3x9"},
		{"--share", "--secret="},
		{"--restart", "--share", "--secret"},
		{"--kill", "docs"},
		{"--commit"},
		{"--commit="},
		{"--commit=HEAD", "-l"},
		{"--commit=HEAD", "-a", "127.31.1.2"},
		{"--commit=HEAD", "--no-open"},
		{"--kill", "-l"},
		{"--kill", "--restart"},
		{"--kill", "--share", "--secret"},
		{"--cache=lots"},
		{"--cache=-1M"},
		{"--cache"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

// A tree is within a directory at or above it, never beside it.
func TestWithin(t *testing.T) {
	for _, tc := range []struct {
		dir, root string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a", true},
		{"/a/b", "/", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/x/..y", "/x", true},
	} {
		if got := within(tc.dir, tc.root); got != tc.want {
			t.Errorf("within(%q, %q) = %v, want %v", tc.dir, tc.root, got, tc.want)
		}
	}
}

// CAP_NET_BIND_SERVICE is bit 10 of the effective set.
func TestHasCap(t *testing.T) {
	for status, want := range map[string]bool{
		"Name:\t" + progName + "\nCapInh:\t0000000000000000\nCapEff:\t0000000000000400\n": true,
		"CapEff:\t000001ffffffffff\n":                            true,
		"CapPrm:\t0000000000000400\nCapEff:\t0000000000000000\n": false,
		"Name:\t" + progName + "\n":                              false,
	} {
		if got := hasCap(status, capNetBindService); got != want {
			t.Errorf("%q: %v, want %v", status, got, want)
		}
	}
}

func TestOrigin(t *testing.T) {
	for ep, want := range map[string]string{
		"127.31.1.1:80":   "http://127.31.1.1",
		"127.31.1.1:8080": "http://127.31.1.1:8080",
		"10.0.0.1:80":     "http://10.0.0.1",
	} {
		if got := origin(netip.MustParseAddrPort(ep)); got != want {
			t.Errorf("%s: %s, want %s", ep, got, want)
		}
	}
}

// A server on 0.0.0.0 hands out the machine's name.
func TestOriginUnspecified(t *testing.T) {
	name, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname")
	}
	if got, want := origin(netip.MustParseAddrPort("0.0.0.0:8080")), "http://"+name+":8080"; got != want {
		t.Errorf("0.0.0.0:8080: %s, want %s", got, want)
	}
}

func TestOpenArgs(t *testing.T) {
	const lo, lan = "http://127.31.1.1/x/", "http://192.168.1.5/x/"
	for _, c := range []struct {
		url, open string
		want      []string
	}{
		{lo, "", []string{"xdg-open", lo}},
		{lo, "brave-browser --app=%s", []string{"brave-browser", "--app=" + lo}},
		{lo, "firefox", []string{"firefox", lo}},
		{lan, "brave-browser --app=%s", []string{"xdg-open", lan}},
	} {
		if got := openArgs(c.url, c.open); !slices.Equal(got, c.want) {
			t.Errorf("openArgs(%q, %q) = %q, want %q", c.url, c.open, got, c.want)
		}
	}
}

// A chosen token comes from WWW_SECRET, never the command line, and a bad
// one's error names the variable without echoing the secret.
func TestSecretToken(t *testing.T) {
	t.Setenv(secretEnv, "k3x9")
	if got, err := secretToken(); err != nil || got != "k3x9" {
		t.Errorf("set: %q, %v; want k3x9", got, err)
	}
	t.Setenv(secretEnv, "a/b")
	if _, err := secretToken(); err == nil || !strings.Contains(err.Error(), secretEnv) || strings.Contains(err.Error(), "a/b") {
		t.Errorf("bad token: %v; want an error naming %s without the token", err, secretEnv)
	}
	t.Setenv(secretEnv, "")
	a, errA := secretToken()
	b, errB := secretToken()
	if errA != nil || errB != nil || a == "" || a == b {
		t.Errorf("unset: %q, %q, %v, %v; want two different random tokens", a, b, errA, errB)
	}
}
