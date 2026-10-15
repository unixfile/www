package main

import (
	"net/netip"
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
		{[]string{"-fn"}, config{dir: ".", fg: true, noopen: true}},
		{[]string{"-f", "-n", "docs"}, config{dir: "docs", fg: true, noopen: true}},
		{[]string{"docs", "--foreground", "--no-open"}, config{dir: "docs", fg: true, noopen: true}},
		{[]string{"-a", "127.31.1.2"}, config{dir: ".", addr: "127.31.1.2"}},
		{[]string{"-a127.31.1.2:9000"}, config{dir: ".", addr: "127.31.1.2:9000"}},
		{[]string{"-fa", "127.31.1.2"}, config{dir: ".", fg: true, addr: "127.31.1.2"}},
		{[]string{"--addr", "127.31.1.2"}, config{dir: ".", addr: "127.31.1.2"}},
		{[]string{"--addr=127.31.1.2"}, config{dir: ".", addr: "127.31.1.2"}},
		{[]string{"--", "-f"}, config{dir: "-f"}},
		{[]string{"-h"}, config{dir: ".", help: true}},
		{[]string{"--help"}, config{dir: ".", help: true}},
		{[]string{"--restart", "-a", "127.31.1.2"}, config{dir: ".", addr: "127.31.1.2", restart: true}},
		{[]string{"-l"}, config{list: true}},
		{[]string{"--list", "docs"}, config{dir: "docs", list: true}},
	}
	for _, tc := range ok {
		got, err := parseArgs(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%q: %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
	for _, args := range [][]string{
		{"-x"},
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
		{"--restart", "docs"},
		{"--restart", "-f"},
		{"-l", "--restart"},
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
		"Name:\tmdserve\nCapInh:\t0000000000000000\nCapEff:\t0000000000000400\n": true,
		"CapEff:\t000001ffffffffff\n":                                            true,
		"CapPrm:\t0000000000000400\nCapEff:\t0000000000000000\n":                 false,
		"Name:\tmdserve\n": false,
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
	} {
		if got := origin(netip.MustParseAddrPort(ep)); got != want {
			t.Errorf("%s: %s, want %s", ep, got, want)
		}
	}
}
