package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// restart replaces the server on c.ep, when -a or --share names it, or
// else every server, with one running the current binary and serving the
// same trees, as --list and --kill pick their servers. An address with no
// server but with persistent locks in its registry gets a server again,
// which is how trees come back after a reboot. Without a named address or
// --force, it leaves the addresses another binary last served, so the
// package's unit and a build from make install never take each other's
// servers. With --debug, the new servers show what serves debugging, and with --no-bug they hide the bug button. With a
// directory, it then serves that directory on c.ep, as a start does, and
// opens it unless a page of it was open on the old server.
func restart(c config) int {
	eps, err := endpoints()
	if err != nil {
		return failNow(errnoOf(err), err.Error())
	}
	named := c.addr != "" || c.sharing
	if named {
		eps = []netip.AddrPort{c.ep}
	}
	code := 0
	var open []string // pages that were open on c.ep's old server
	for _, ep := range eps {
		other := ""
		if !named && !c.force {
			other = otherBinary(ep)
		}
		pid, trees, err := servedTrees(ep)
		if errors.Is(err, syscall.ESRCH) {
			if kept := persistedTrees(ep); len(kept) > 0 && other != "" {
				say("the persistent trees on %s wait for %s, so they stay; -f serves them with this binary", ep, other)
				continue
			} else if len(kept) > 0 {
				err = startTrees(ep, kept, c.debug, c.noBug)
			} else if !named || c.dir != "" {
				continue
			}
		} else if err == nil && other != "" {
			say("the server on %s runs %s, so it stays; -f moves it to this binary", ep, other)
			continue
		} else if err == nil {
			var pages []string
			if pages, err = restartServer(ep, pid, trees, c.debug, c.noBug); err == nil {
				say("the server on %s is replaced; %s", ep, reloading(len(pages)))
			}
			if ep == c.ep {
				open = pages
			}
		}
		if err != nil && code == 0 {
			code = failNow(errnoOf(err), err.Error())
		} else if err != nil {
			failNow(errnoOf(err), err.Error())
		}
	}
	if c.dir != "" && code == 0 {
		code = startDir(c, open)
	}
	return code
}

// reloading says how many open pages a new server takes over.
func reloading(n int) string {
	switch n {
	case 0:
		return "no page was open"
	case 1:
		return "1 open page reloads onto it"
	}
	return strconv.Itoa(n) + " open pages reload onto it"
}

// say tells the user and the log what a restart did.
func say(format string, args ...any) {
	logInfo(format, args...)
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// startDir serves c.dir on c.ep once the servers are replaced. A tree the
// old server served has had its URL printed; it opens in the browser only
// when none of open, the old server's pages, lies in it, since an open
// page reloads onto the new server. A hidden tab holds no stream, so it
// does not count. Any other directory starts as from the command line.
func startDir(c config, open []string) int {
	dir, err := filepath.Abs(c.dir)
	if err != nil {
		return failNow(errnoOf(err), "bad directory: "+err.Error())
	}
	if _, trees, err := servedTrees(c.ep); err == nil {
		for _, t := range trees {
			if t.dir != dir {
				continue
			}
			if c.noopen {
				return 0
			}
			n := 0
			if u, err := neturl.Parse(t.url); err == nil {
				for _, p := range open {
					if strings.HasPrefix(p, u.Path) {
						n++
					}
				}
			}
			if n > 0 {
				say("%s has %s, so no browser opens", dir, pagesOf(n))
				return 0
			}
			say("no page of %s was open, so one opens", dir)
			openBrowser(t.url)
			return 0
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return failNow(errnoOf(err), "cannot find own executable: "+err.Error())
	}
	args := []string{"-a", c.ep.String()}
	if shared(c.ep) {
		args = []string{"--share=" + c.ep.String()}
	}
	if c.noopen {
		args = append(args, "--no-open")
	}
	if c.debug {
		args = append(args, "--debug")
	}
	if c.noBug {
		args = append(args, "--no-bug")
	}
	cmd := exec.Command(exe, append(args, "--", dir)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// The child has printed its own error.
			return ee.ExitCode()
		}
		return failNow(errnoOf(err), "cannot serve "+dir+": "+err.Error())
	}
	return 0
}

// pagesOf counts open pages in a sentence.
func pagesOf(n int) string {
	if n == 1 {
		return "1 open page"
	}
	return strconv.Itoa(n) + " open pages"
}

// restartServer reads the trees from the registry, sends the server
// SIGTERM, waits for its locks to go and starts each tree again. It
// returns the pages that held a stream to the old server, from the record
// the server leaves as it stops. A binary that could not bind the
// server's port leaves the server alone, since stopping it would drop its
// trees.
func restartServer(ep netip.AddrPort, pid int, trees []servedTree, debug, noBug bool) ([]string, error) {
	if ep.Port() < 1024 && !lowPortOK() {
		return nil, &errno{syscall.EACCES, "this binary cannot bind port " + strconv.Itoa(int(ep.Port())) +
			", so the server on " + ep.String() + " keeps its old one. Run the installed package's " + progName + ", which holds cap_net_bind_service"}
	}
	var record string
	if dir, err := registryDir(ep); err == nil {
		record = filepath.Join(dir, pagesFile)
		os.WriteFile(record, nil, 0o600)
	}
	if err := stopServer(ep, pid, trees); err != nil {
		os.Remove(record)
		return nil, err
	}
	var pages []string
	if b, err := os.ReadFile(record); record != "" && err == nil {
		// A path may hold spaces, so lines divide them.
		pages = strings.FieldsFunc(string(b), func(r rune) bool { return r == '\n' })
		os.Remove(record)
	}
	return pages, startTrees(ep, trees, debug, noBug)
}

// startTrees serves each tree on ep through this binary, as a start from
// the command line would, persistent ones with --persist. The first start
// runs the server, so --debug and --no-bug reach it.
func startTrees(ep netip.AddrPort, trees []servedTree, debug, noBug bool) error {
	if ep.Port() < 1024 && !lowPortOK() {
		return &errno{syscall.EACCES, "this binary cannot bind port " + strconv.Itoa(int(ep.Port())) +
			", so it cannot serve " + ep.String() + ". Run the installed package's " + progName + ", which holds cap_net_bind_service"}
	}
	exe, err := os.Executable()
	if err != nil {
		return &errno{errnoOf(err), "cannot find own executable: " + err.Error()}
	}
	var failed error
	for _, t := range trees {
		args := []string{"--no-open", "-a", ep.String()}
		if shared(ep) {
			args = []string{"--no-open", "--share=" + ep.String()}
		}
		// The token goes in the environment, since ps shows the command
		// line.
		var env []string
		if t.token != "" {
			args = append(args, "--secret")
			env = append(os.Environ(), secretEnv+"="+t.token)
		}
		if t.persist {
			args = append(args, "--persist")
		}
		if debug {
			args = append(args, "--debug")
		}
		if noBug {
			args = append(args, "--no-bug")
		}
		cmd := exec.Command(exe, append(args, "--", t.dir)...)
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil && failed == nil {
			code := syscall.EIO
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = syscall.Errno(ee.ExitCode())
			}
			// The child has printed its own error.
			failed = &errno{code, "cannot serve " + t.dir + " again on " + ep.String()}
		}
	}
	return failed
}

// kill stops the server on c.ep when -a or --share names it, else every
// server, and prints the trees each one served as --list does.
func kill(c config) int {
	eps, err := endpoints()
	if err != nil {
		return failNow(errnoOf(err), err.Error())
	}
	named := c.addr != "" || c.sharing
	if named {
		eps = []netip.AddrPort{c.ep}
	}
	// The stopped trees print once at the end, since keyval numbers them
	// across every server.
	var stopped []servedTree
	code := 0
	for _, ep := range eps {
		pid, trees, err := servedTrees(ep)
		if errors.Is(err, syscall.ESRCH) && !named {
			continue
		}
		if err == nil {
			err = stopServer(ep, pid, trees)
		}
		if err != nil {
			code = failNow(errnoOf(err), err.Error())
			continue
		}
		stopped = append(stopped, trees...)
	}
	writeTrees(os.Stdout, stopped)
	return code
}

// stopServer sends the server on ep SIGTERM and waits until it has removed
// the locks of trees.
func stopServer(ep netip.AddrPort, pid int, trees []servedTree) error {
	logInfo("sends SIGTERM to the server on %s, pid %d", ep, pid)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return &errno{errnoOf(err), "cannot stop the server on " + ep.String() + ": " + err.Error()}
	}
	dirs := make([]string, len(trees))
	for i, t := range trees {
		dirs[i] = t.dir
	}
	if !waitGone(dirs, ep) {
		return &errno{syscall.ETIMEDOUT, "the server on " + ep.String() + " kept its locks"}
	}
	return nil
}

// servedTrees returns the server's PID and the trees it serves, sorted by
// directory.
func servedTrees(ep netip.AddrPort) (int, []servedTree, error) {
	trees, err := liveTrees(ep)
	if err != nil {
		return 0, nil, err
	}
	if len(trees) == 0 {
		return 0, nil, &errno{syscall.ESRCH, "no server runs on " + ep.String()}
	}
	slices.SortFunc(trees, func(a, b servedTree) int { return strings.Compare(a.dir, b.dir) })
	return trees[0].pid, trees, nil
}

// waitGone waits for the server to withdraw every tree and drop its
// record; persistent locks stay. It closes its listener as it does, so a
// new server can bind before the old one exits.
func waitGone(dirs []string, ep netip.AddrPort) bool {
	for end := time.Now().Add(joinWait); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if _, up := serverOf(ep); !up && !slices.ContainsFunc(dirs, func(dir string) bool {
			_, err := os.Lstat(filepath.Join(dir, lockName(ep)))
			return !errors.Is(err, fs.ErrNotExist)
		}) {
			return true
		}
	}
	return false
}

// persistedTrees returns the trees whose persistent locks the registry of
// ep still links to, for a server that is not running.
func persistedTrees(ep netip.AddrPort) []servedTree {
	dir, err := registryDir(ep)
	if err != nil {
		return nil
	}
	links, _ := readLinks(dir)
	var trees []servedTree
	for _, target := range links {
		lockPath, token := splitTarget(target)
		if !isPersist(lockPath) {
			continue
		}
		if _, err := os.Stat(lockPath); err != nil {
			continue
		}
		trees = append(trees, servedTree{ep: ep, dir: filepath.Dir(lockPath), token: token, lockPath: lockPath, persist: true})
	}
	slices.SortFunc(trees, func(a, b servedTree) int { return strings.Compare(a.dir, b.dir) })
	return trees
}
