package www

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Options parse the GNU way: short ones combine, values sit joined or apart,
// and options may follow the directory.
func TestParseArgs(t *testing.T) {
	for _, tc := range parseOK {
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
		{"--public", "--disk=6G", "-a", "127.0.0.1"},
	} {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

// parseOK holds command lines www takes, and what each parses to.
var parseOK = []struct {
	args []string
	want config
}{
	{nil, config{dir: "."}},
	{[]string{"--foreground", "--debug"}, config{dir: ".", fg: true, debug: true}},
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
	{[]string{"--restart", "--debug", "--no-bug"}, config{restart: true, debug: true, noBug: true}},
	{[]string{"--disk=4G", "--no-bug", "docs"}, config{dir: "docs", disk: "4G", noBug: true}},
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

// The public executable takes an address, --disk, --cache and --debug. It
// takes no directory and none of the reader's other options, --public
// among them.
func TestParsePublicArgs(t *testing.T) {
	for _, tc := range parsePublicOK {
		got, err := parsePublicArgs(tc.args)
		if err != nil || got != tc.want {
			t.Errorf("%q: %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
	for _, args := range [][]string{
		nil,
		{"-a", "127.0.0.1"},
		{"--disk=6G"},
		{"--disk", "-a", "127.0.0.1"},
		{"--disk=", "-a", "127.0.0.1"},
		{"--disk=lots", "-a", "127.0.0.1"},
		{"--disk=6G", "-a"},
		{"--disk=6G", "--addr="},
		{"--disk=6G", "--share="},
		{"--disk=6G", "-a", "127.0.0.1", "--share"},
		{"--disk=6G", "-a", "127.0.0.1", "docs"},
		{"--disk=6G", "-a", "127.0.0.1", "--", "docs"},
		{"--disk=6G", "-a", "127.0.0.1", "--cache=lots"},
		{"--disk=6G", "-a", "127.0.0.1", "--debug=yes"},
		{"--disk=6G", "-a", "127.0.0.1", "--public"},
		{"--disk=6G", "-a", "127.0.0.1", "--foreground"},
		{"--disk=6G", "-a", "127.0.0.1", "--no-open"},
		{"--disk=6G", "-a", "127.0.0.1", "--no-bug"},
		{"--disk=6G", "-a", "127.0.0.1", "--restart"},
		{"--disk=6G", "-a", "127.0.0.1", "--kill"},
		{"--disk=6G", "-a", "127.0.0.1", "-p"},
		{"--disk=6G", "-a", "127.0.0.1", "-l"},
		{"--disk=6G", "-a", "127.0.0.1", "-f"},
		{"--disk=6G", "--share", "--secret"},
		{"--disk=6G", "-a", "127.0.0.1", "--commit=HEAD"},
	} {
		if _, err := parsePublicArgs(args); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
}

// parsePublicOK holds command lines the public executable takes.
var parsePublicOK = []struct {
	args []string
	want config
}{
	{[]string{"--disk=6G", "-a", "127.0.0.1:8080"}, config{public: true, disk: "6G", addr: "127.0.0.1:8080"}},
	{[]string{"--disk", "6G", "-a127.0.0.1"}, config{public: true, disk: "6G", addr: "127.0.0.1"}},
	{[]string{"--addr=127.0.0.1", "--disk=6G", "--cache=64M"}, config{public: true, disk: "6G", addr: "127.0.0.1", cache: "64M"}},
	{[]string{"--disk=6G", "--share=0.0.0.0", "--debug"}, config{public: true, disk: "6G", sharing: true, share: "0.0.0.0", debug: true}},
	{[]string{"--disk=6G", "--share"}, config{public: true, disk: "6G", sharing: true}},
	{[]string{"-h"}, config{public: true, help: true}},
	{[]string{"--help"}, config{public: true, help: true}},
	{[]string{"-V"}, config{public: true, version: true}},
	{[]string{"--version"}, config{public: true, version: true}},
}

// Each executable's usage text is docopt, and docopt takes every command
// line in parseOK and parsePublicOK. docopt-ng runs through uv, and the
// test skips without it. docopt has no option with an optional value, so
// --share[=HOST] is checked as --share.
func TestUsageIsDocopt(t *testing.T) {
	var local, public [][]string
	for _, tc := range parseOK {
		local = append(local, tc.args)
	}
	for _, tc := range parsePublicOK {
		public = append(public, tc.args)
	}
	t.Run("local", func(t *testing.T) { checkDocopt(t, usageText, local) })
	t.Run("public", func(t *testing.T) { checkDocopt(t, publicUsage, public) })
}

func checkDocopt(t *testing.T, text string, ok [][]string) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv is not installed")
	}
	usage := filepath.Join(t.TempDir(), "usage")
	if err := os.WriteFile(usage, []byte(strings.ReplaceAll(text, "--share[=HOST]", "--share")), 0o600); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for _, line := range ok {
		args := make([]string, len(line))
		for i, a := range line {
			if strings.HasPrefix(a, "--share=") {
				a = "--share"
			}
			args[i] = shellQuote(a)
		}
		lines.WriteString(strings.Join(args, " ") + "\n")
	}
	cmd := exec.Command(uv, "run", "--quiet", "--script", "testdata/docopt-check.py", usage)
	cmd.Stdin = strings.NewReader(lines.String())
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee) && ee.ExitCode() == 22:
		t.Errorf("docopt-ng:\n%s", out)
	default:
		t.Skipf("uv could not run docopt-ng: %v\n%s", err, out)
	}
}

// shellQuote quotes s for shlex.split.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
