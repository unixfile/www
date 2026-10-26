// Command www browses trees of Markdown docs in the browser. It renders
// each page with goldmark when requested and mirrors the file tree in its
// URLs. Trees on one address share a server, which lives while their locks
// exist.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	neturl "net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// readyEnv names the descriptor a detached child reports its start on.
var readyEnv = envPrefix + "READY_FD"

// defaultPort is 8080 until main finds that this process may bind port 80.
var defaultPort uint16 = 8080

// capNetBindService is the capability bit that lets a process bind ports
// below 1024.
const capNetBindService = 10

// lowPortOK reports whether this process may bind port 80: it holds
// CAP_NET_BIND_SERVICE, as the Debian package grants, or the machine lets
// every user bind low ports.
func lowPortOK() bool {
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_unprivileged_port_start"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n <= 80 {
			return true
		}
	}
	b, err := os.ReadFile("/proc/self/status")
	return err == nil && hasCap(string(b), capNetBindService)
}

// hasCap reads the effective capability set from a /proc status file.
func hasCap(status string, bit uint) bool {
	for _, line := range strings.Split(status, "\n") {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return err == nil && caps&(1<<bit) != 0
		}
	}
	return false
}

type config struct {
	dir     string
	addr    string
	sharing bool
	share   string // the address --share names; "" takes the default route's
	secret  bool
	token   string // the secret path's token, from WWW_SECRET or drawn at random
	ep      netip.AddrPort
	fg      bool
	noopen  bool
	persist bool   // write a persistent lock, so the tree is served after reboot
	debug   bool   // show the build stamp and the index's times
	noBug   bool   // hide the bug button and refuse reports
	cache   string // the memory for cached git answers, eg 32M; "" takes defaultCache
	disk    string // the disk for online repos, eg 4G; "" takes defaultDisk
	help    bool
	version bool
	restart bool
	force   bool // --restart takes servers of other binaries too
	list    bool
	kill    bool
	commit  string // the commit --commit looks up
}

// usageText is written in docopt notation, except that --share takes its
// HOST optionally, as GNU optional arguments do, which docopt cannot say.
// TestUsageIsDocopt checks it with docopt-ng. A pattern goes on over
// indented lines.
var usageText = strings.NewReplacer("@NAME@", progName, "@ENV@", envPrefix).Replace(`Usage:
  @NAME@ [-p] [--foreground] [--no-open] [-a ADDR] [--debug] [--no-bug]
      [--cache=SIZE] [--disk=SIZE] [--] [DIR]
  @NAME@ [-p] [--foreground] [--no-open] --share[=HOST] [--secret] [--debug]
      [--no-bug] [--cache=SIZE] [--disk=SIZE] [--] [DIR]
  @NAME@ --restart [-f] [--no-open] [--debug] [--no-bug]
      [-a ADDR | --share[=HOST]] [--] [DIR]
  @NAME@ -l [-a ADDR | --share[=HOST]] [--] [DIR]
  @NAME@ --kill [-a ADDR | --share[=HOST]]
  @NAME@ --commit=REV [--] [DIR]
  @NAME@ -h | --help
  @NAME@ -V | --version

Options:
  -a ADDR, --addr=ADDR  serve on this loopback address, IP or IP:PORT; the
                        port defaults to 80 when @NAME@ may bind it, else
                        8080, and the IP to 127.31.N.1, N = uid - 1000
  --share[=HOST]        share the tree on the address of the default route,
                        or on HOST, an address or name beyond loopback with
                        an optional :PORT, eg 192.168.1.5, 0.0.0.0 or
                        mate.peer.home; anyone who reaches it reads every
                        file in the tree that other users could read
  --secret              serve a shared tree under the token in @ENV@SECRET,
                        or a random one, so only those holding the link
                        reach it
  --foreground          when no server runs on the address yet, serve in the
                        foreground until Ctrl-C
  --no-open             print the URL without opening a browser
  -p, --persist         write a .LOCK instead of a .lock: the tree is served
                        again after a reboot or a stopped server, by the
                        next server on its address or by --restart
  --debug               show what serves debugging: above each page's
                        header, small and faint, the binary's build time,
                        the seconds since, its commit and the page's render
                        time; on the index, each server's build and start
                        and each lock's time; with --restart, on the new
                        servers
  --no-bug              hide the bug button at the foot of each page, which
                        files a numbered bug in this program, not in the
                        tree, with the page and the server's state; a share
                        never shows it; with --restart, on the new servers
  --cache=SIZE          memory a new server may spend on cached git answers,
                        eg 64M; 0 caches nothing [default: 32M]
  --disk=SIZE           disk a new server may fill with online repos, eg 4G;
                        it drops the repos read longest ago [default: 2G]
  --restart             replace every server this binary started with one
                        running it now, serving the same trees, and start
                        one for its persistent trees that have none; with an
                        address from -a or --share, only the server there,
                        whichever binary runs it; with DIR, serve it too and
                        open it unless a page of it was open
  -f, --force           with --restart, take the servers and persistent
                        trees of other binaries too, eg the package's
  -l, --list            print each served tree's directory, URL and the
                        binary that serves it as keyval; with DIR, only the
                        trees at or below it
  --kill                stop every server and print the trees it served,
                        as with --list; with an address from -a or --share,
                        only the server there
  --commit=REV          print as keyval the commit REV names in DIR's repo:
                        its full hash, its stardate and its subject; REV is
                        a stardate, eg 261024.4, a hash or any name git
                        takes
  -V, --version         print when the binary was built and its commit
  -h, --help            print this help
`)

// switches maps each long boolean option to its field; shorts names the
// ones with a short form.
func (c *config) switches() map[string]*bool {
	return map[string]*bool{
		"foreground": &c.fg,
		"no-open":    &c.noopen,
		"persist":    &c.persist,
		"debug":      &c.debug,
		"no-bug":     &c.noBug,
		"help":       &c.help,
		"version":    &c.version,
		"restart":    &c.restart,
		"force":      &c.force,
		"list":       &c.list,
		"kill":       &c.kill,
	}
}

// -f and -n are reserved for --force and --dry-run in every tool of the
// user's, so --foreground and --no-open have no short form.
var shorts = map[byte]string{'p': "persist", 'h': "help", 'V': "version", 'l': "list", 'f': "force"}

// parseArgs reads options the GNU way. Short ones combine, as in -pl, and
// take a value joined or apart: -aIP or -a IP. Long ones take --addr IP or
// --addr=IP. Options may follow the directory, and "--" ends them.
func parseArgs(args []string) (config, error) {
	var c config
	sw := c.switches()
	var dirs []string
	value := func(i *int, opt, joined string) (string, error) {
		if joined != "" {
			return joined, nil
		}
		if *i+1 == len(args) {
			return "", &errno{syscall.EINVAL, "option " + opt + " needs a value"}
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			dirs = append(dirs, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			if name == "addr" {
				if hasVal && val == "" {
					return c, &errno{syscall.EINVAL, "option --addr needs a value"}
				}
				v, err := value(&i, "--addr", val)
				if err != nil {
					return c, err
				}
				c.addr = v
				continue
			}
			// --share, like --secret, takes its address only joined; alone
			// it takes the default route's.
			if name == "share" {
				if hasVal && val == "" {
					return c, &errno{syscall.EINVAL, "option --share= needs an address"}
				}
				c.sharing, c.share = true, val
				continue
			}
			// --secret takes no token: one on the command line shows in
			// ps to every local user and stays in shell history.
			if name == "secret" {
				if hasVal {
					return c, &errno{syscall.EINVAL, "--secret takes no token, since ps shows it; set " + secretEnv}
				}
				c.secret = true
				continue
			}
			if name == "commit" {
				v, err := value(&i, "--commit", val)
				if err != nil {
					return c, err
				}
				if v == "" {
					return c, &errno{syscall.EINVAL, "option --commit needs a value"}
				}
				c.commit = v
				continue
			}
			if name == "cache" || name == "disk" {
				v, err := value(&i, "--"+name, val)
				if err != nil {
					return c, err
				}
				if _, err := parseSize(v); err != nil {
					return c, err
				}
				if name == "cache" {
					c.cache = v
				} else {
					c.disk = v
				}
				continue
			}
			p, ok := sw[name]
			switch {
			case !ok:
				return c, &errno{syscall.EINVAL, "unknown option --" + name}
			case hasVal:
				return c, &errno{syscall.EINVAL, "option --" + name + " takes no value"}
			}
			*p = true
		case len(a) > 1 && a[0] == '-':
			for j := 1; j < len(a); j++ {
				if a[j] == 'a' {
					v, err := value(&i, "-a", a[j+1:])
					if err != nil {
						return c, err
					}
					c.addr = v
					break
				}
				name, ok := shorts[a[j]]
				if !ok {
					return c, &errno{syscall.EINVAL, "unknown option -" + string(a[j])}
				}
				*sw[name] = true
			}
		default:
			dirs = append(dirs, a)
		}
	}
	if c.commit != "" && (c.restart || c.list || c.kill || c.fg || c.noopen || c.persist || c.debug || c.noBug ||
		c.sharing || c.secret || c.addr != "" || c.cache != "" || c.disk != "") {
		return c, &errno{syscall.EINVAL, "--commit takes a directory and no other option"}
	}
	if c.force && !c.restart {
		return c, &errno{syscall.EINVAL, "--force goes with --restart"}
	}
	if c.restart && c.fg {
		return c, &errno{syscall.EINVAL, "--restart takes no --foreground"}
	}
	if c.persist && (c.restart || c.list || c.kill) {
		return c, &errno{syscall.EINVAL, "--persist goes with a start, not with --restart, --list or --kill"}
	}
	if c.list && (c.restart || c.fg) {
		return c, &errno{syscall.EINVAL, "--list takes no --restart or --foreground"}
	}
	if c.kill && (len(dirs) > 0 || c.fg || c.restart || c.list) {
		return c, &errno{syscall.EINVAL, "--kill takes no directory, --foreground, --restart or --list"}
	}
	if c.addr != "" && c.sharing {
		return c, &errno{syscall.EINVAL, "-a and --share name one address each; give one"}
	}
	if c.secret && (!c.sharing || c.restart || c.list || c.kill) {
		return c, &errno{syscall.EINVAL, "--secret goes with --share, and not with --restart, --list or --kill"}
	}
	switch len(dirs) {
	case 0:
		// --list without a directory lists every tree, and --restart
		// without one serves no tree beyond those it restarts.
		if !c.list && !c.kill && !c.restart {
			c.dir = "."
		}
	case 1:
		c.dir = dirs[0]
	default:
		return c, &errno{syscall.EINVAL, "one directory at most, got " + strings.Join(dirs, " ")}
	}
	return c, nil
}

func main() {
	c, err := parseArgs(os.Args[1:])
	switch {
	case err != nil:
		noteRun()
		code := failNow(errnoOf(err), err.Error())
		io.WriteString(os.Stderr, usageText)
		os.Exit(code)
	}
	if lowPortOK() {
		defaultPort = 80
	}
	if c.help {
		io.WriteString(os.Stdout, usageText+environment(os.Getuid())+logHelp())
		os.Exit(0)
	}
	if c.version {
		fmt.Println(versionLine(built, buildCommit))
		os.Exit(0)
	}
	if c.commit != "" {
		noteRun()
		os.Exit(lookupCommit(os.Stdout, c))
	}
	if hint := spacedShare(c); hint != "" {
		noteRun()
		os.Exit(failNow(syscall.ENOENT, hint))
	}
	ep, err := endpointOf(c, os.Getuid())
	if err != nil {
		noteRun()
		os.Exit(failNow(errnoOf(err), err.Error()))
	}
	c.ep = ep
	// --list, --kill and --restart without an address cover every server.
	if !(c.list || c.kill || c.restart) || c.addr != "" || c.sharing {
		setLogEndpoint(endpointName(ep))
	}
	noteRun()
	if c.secret {
		if c.token, err = secretToken(); err != nil {
			os.Exit(failNow(errnoOf(err), err.Error()))
		}
	}

	fd := os.Getenv(readyEnv)
	switch {
	case c.restart:
		os.Exit(restart(c))
	case c.list:
		os.Exit(list(c))
	case c.kill:
		os.Exit(kill(c))
	case fd != "":
		n, _ := strconv.Atoi(fd)
		os.Exit(run(c, &reporter{pipe: os.NewFile(uintptr(n), "ready")}))
	case c.fg:
		os.Exit(run(c, &reporter{noopen: c.noopen}))
	default:
		os.Exit(detach(c))
	}
}

// noteRun logs this run, once its endpoint is known or it fails without
// one. A detached server is the same run, re-executed; its start says so.
func noteRun() {
	if os.Getenv(readyEnv) == "" {
		logRun(os.Args[1:])
	}
}

// Environment variables that set the default addresses, the command that
// opens a loopback URL, and a share's chosen token.
var (
	addrEnv   = envPrefix + "ADDR"
	shareEnv  = envPrefix + "SHARE"
	openEnv   = envPrefix + "OPEN"
	secretEnv = envPrefix + "SECRET"
)

// secretToken returns the token in WWW_SECRET, or a random one when it is
// unset. A bad token's error names the variable but not its value, which
// is a secret.
func secretToken() (string, error) {
	v := os.Getenv(secretEnv)
	switch {
	case v == "":
		return newToken(), nil
	case !validToken(v):
		return "", &errno{syscall.EINVAL, secretEnv + " holds a bad token: want letters, digits, - and _"}
	}
	return v, nil
}

// endpointOf returns where c serves. A bare --share takes WWW_SHARE,
// and a start without -a takes WWW_ADDR; --list, --kill and --restart
// without an address cover every server, so WWW_ADDR leaves them be.
// An error from a variable's value names the variable.
func endpointOf(c config, uid int) (netip.AddrPort, error) {
	if c.sharing {
		if c.share == "" && os.Getenv(shareEnv) != "" {
			ep, err := shareEndpoint(os.Getenv(shareEnv))
			return ep, fromEnv(shareEnv, err)
		}
		return shareEndpoint(c.share)
	}
	if c.addr == "" && !c.list && !c.kill && !c.restart && os.Getenv(addrEnv) != "" {
		ep, err := endpointFor(os.Getenv(addrEnv), uid)
		return ep, fromEnv(addrEnv, err)
	}
	return endpointFor(c.addr, uid)
}

func fromEnv(name string, err error) error {
	if err == nil {
		return nil
	}
	return &errno{errnoOf(err), name + "=" + os.Getenv(name) + ": " + err.Error()}
}

// environment is the help's account of the default addresses: each value
// and where it comes from.
func environment(uid int) string {
	addr := "built in"
	if ep, err := endpointFor("", uid); err == nil {
		addr = ep.String() + ", built in"
	}
	if v := os.Getenv(addrEnv); v != "" {
		addr = v + ", from " + addrEnv
	}
	share := "the default route's address"
	if a, err := routeAddr(); err == nil {
		share = a.String() + ", the default route's address"
	}
	if v := os.Getenv(shareEnv); v != "" {
		share = v + ", from " + shareEnv
	}
	open := "xdg-open, built in"
	if v := os.Getenv(openEnv); v != "" {
		open = v + ", from " + openEnv
	}
	secret := "unset, so a random token"
	if os.Getenv(secretEnv) != "" {
		secret = "set"
	}
	return "\nenvironment:\n" +
		"  " + addrEnv + "    default for a start without -a; now " + addr + "\n" +
		"  " + shareEnv + "   default for a bare --share; now " + share + "\n" +
		"  " + openEnv + "    command that opens a loopback URL, %s for the URL or\n" +
		"              appended, eg 'brave-browser --app=%s'; now " + open + "\n" +
		"  " + secretEnv + "  the token for --secret; now " + secret + "\n"
}

// endpointFor returns where to serve: addr when given, else the user's own
// 127.31.N.1 with N = uid - 1000, so users on one machine never collide.
// The port is defaultPort unless addr names one. -a takes only loopback, so
// a typo cannot share a tree; --share goes through shareEndpoint.
func endpointFor(addr string, uid int) (netip.AddrPort, error) {
	if addr == "" {
		n := uid - 1000
		if n < 0 || n > 255 {
			return netip.AddrPort{}, &errno{syscall.EINVAL, fmt.Sprintf("uid %d has no default address; pass --addr", uid)}
		}
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 31, byte(n), 1}), defaultPort), nil
	}
	ep, err := parseEndpoint(addr, "127.31.1.2")
	if err != nil {
		return ep, err
	}
	if !ep.Addr().IsLoopback() {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": -a takes a loopback address; --share takes the others"}
	}
	return ep, nil
}

// shareEndpoint returns where to share: any IPv4 address beyond loopback,
// 0.0.0.0 included, on defaultPort unless addr names one. With no addr it
// is the default route's address, so a share needs no address remembered.
func shareEndpoint(addr string) (netip.AddrPort, error) {
	if addr == "" {
		a, err := routeAddr()
		if err != nil {
			return netip.AddrPort{}, err
		}
		return netip.AddrPortFrom(a, defaultPort), nil
	}
	ep, err := parseEndpoint(addr, "192.168.1.5")
	if err != nil {
		if ep, err = resolveEndpoint(addr); err != nil {
			return ep, err
		}
	}
	if ep.Addr().IsLoopback() {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": --share takes an address beyond loopback; -a takes loopback"}
	}
	return ep, nil
}

// resolveEndpoint reads name[:port] and resolves the name to an IPv4
// address that an interface here holds; only such an address can be bound.
func resolveEndpoint(addr string) (netip.AddrPort, error) {
	name, port := addr, defaultPort
	if h, p, err := net.SplitHostPort(addr); err == nil {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return netip.AddrPort{}, &errno{syscall.EINVAL, "bad port in " + addr + ": want 1 to 65535"}
		}
		name, port = h, uint16(n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", name)
	if err != nil {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want an IPv4 address or a name, eg 192.168.1.5 or mate.peer.home (" + err.Error() + ")"}
	}
	for _, ip := range ips {
		if ifaceOf(ip.Unmap()) != "any" {
			return netip.AddrPortFrom(ip.Unmap(), port), nil
		}
	}
	return netip.AddrPort{}, &errno{syscall.EADDRNOTAVAIL, fmt.Sprintf("%s resolves to %v, which no interface here holds", name, ips)}
}

// spacedShare catches --share 192.168.1.5, where the address became the
// directory: a bare --share with a directory that does not exist but reads
// or resolves as an address. It returns the hint, or "".
func spacedShare(c config) string {
	if !c.sharing || c.share != "" || c.dir == "" {
		return ""
	}
	if _, err := os.Stat(c.dir); !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if _, err := parseEndpoint(c.dir, ""); err != nil {
		if _, err := resolveEndpoint(c.dir); errors.Is(err, syscall.EINVAL) {
			return ""
		}
	}
	return "no directory " + c.dir + "; to share on that address, write --share=" + c.dir
}

// routeAddr asks the kernel which source address it would use to reach
// beyond this machine. Connecting a UDP socket picks the route and sends
// nothing. 192.0.2.1 is reserved for documentation, so only the default
// route leads there.
func routeAddr() (netip.Addr, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return netip.Addr{}, &errno{errnoOf(err), "no default route to share on (" + err.Error() + "); name an address, eg --share=192.168.1.5"}
	}
	defer conn.Close()
	a := conn.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	if a.IsLoopback() {
		return netip.Addr{}, &errno{syscall.ENETUNREACH, "the default route leaves from loopback; name an address, eg --share=192.168.1.5"}
	}
	return a, nil
}

// parseEndpoint reads ip[:port] as an IPv4 address and a port above 0.
func parseEndpoint(addr, example string) (netip.AddrPort, error) {
	ep, err := netip.ParseAddrPort(addr)
	if err != nil {
		a, aerr := netip.ParseAddr(addr)
		if aerr != nil {
			return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want eg " + example + " or " + example + ":9000"}
		}
		ep = netip.AddrPortFrom(a, defaultPort)
	}
	if !ep.Addr().Is4() || ep.Port() == 0 {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want an IPv4 address and a port above 0"}
	}
	return ep, nil
}

// shared reports whether ep serves beyond this machine.
func shared(ep netip.AddrPort) bool {
	return !ep.Addr().IsLoopback()
}

// newToken draws 128 bits for a secret path, in lower-case base32 so the
// link survives being read aloud or typed.
func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// validToken keeps a token to one URL path segment that needs no escaping.
func validToken(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
	})
}

// errno is an error with the code www exits with.
type errno struct {
	code syscall.Errno
	msg  string
}

func (e *errno) Error() string { return e.msg }
func (e *errno) Unwrap() error { return e.code }

// reporter carries the start outcome: to the terminal in the foreground,
// or through a pipe to the parent that detached this process.
type reporter struct {
	pipe   *os.File
	noopen bool
	done   bool
}

func (r *reporter) send(kind, text string) {
	switch {
	case r.done:
	case r.pipe != nil:
		fmt.Fprintf(r.pipe, "%s %s\n", kind, text)
	default:
		show(kind, text, r.noopen)
	}
}

// finish ends the report; a detached parent exits once the pipe closes.
func (r *reporter) finish() {
	if r.pipe != nil {
		r.pipe.Close()
	}
	r.done = true
}

func (r *reporter) fail(code syscall.Errno, msg string) int {
	logErr("E%d: %s", code, msg)
	r.send("err", fmt.Sprintf("E%d: %s", code, msg))
	r.finish()
	return int(code)
}

func show(kind, text string, noopen bool) {
	switch kind {
	case "err", "note":
		fmt.Fprintln(os.Stderr, text)
	case "url":
		fmt.Println(text)
		if !noopen {
			openBrowser(text)
		}
	}
}

// openBrowser opens url when a display exists. Over SSH there is none, and
// the printed URL has to do.
func openBrowser(url string) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	args := openArgs(url, os.Getenv(openEnv))
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "E%d: cannot open %s with %s: %v\n", errnoOf(err), url, args[0], err)
		return
	}
	go cmd.Wait()
}

// openArgs is the command that opens url. A loopback URL takes WWW_OPEN,
// split at spaces, with each %s replaced by the URL, or the URL appended
// when none holds %s; eg 'brave-browser --app=%s' opens an app window. A
// shared URL, and any URL without the variable, goes to xdg-open.
func openArgs(url, open string) []string {
	fields := strings.Fields(open)
	if len(fields) == 0 || !loopbackURL(url) {
		return []string{"xdg-open", url}
	}
	placed := false
	for i, f := range fields {
		if strings.Contains(f, "%s") {
			fields[i] = strings.ReplaceAll(f, "%s", url)
			placed = true
		}
	}
	if !placed {
		fields = append(fields, url)
	}
	return fields
}

// loopbackURL reports whether u names a loopback host.
func loopbackURL(u string) bool {
	p, err := neturl.Parse(u)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(p.Hostname())
	return err == nil && a.IsLoopback() || p.Hostname() == "localhost"
}

// detach re-runs www in a new session and relays its start report.
// Go cannot fork safely, so a re-exec is the way to background itself.
func detach(c config) int {
	r, w, err := os.Pipe()
	if err != nil {
		return failNow(errnoOf(err), "cannot create pipe: "+err.Error())
	}
	exe, err := os.Executable()
	if err != nil {
		return failNow(errnoOf(err), "cannot find own executable: "+err.Error())
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), readyEnv+"=3")
	cmd.ExtraFiles = []*os.File{w}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Without a terminal, the server's stderr goes to the log, so a panic
	// is kept.
	if sink := logSink(); sink != nil {
		cmd.Stderr = sink
		defer sink.Close()
	}
	if err := cmd.Start(); err != nil {
		return failNow(errnoOf(err), "cannot start server: "+err.Error())
	}
	w.Close()

	code, got := 0, false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		kind, text, _ := strings.Cut(sc.Text(), " ")
		got = true
		show(kind, text, c.noopen)
		if kind == "err" {
			code = codeOf(text)
		}
	}
	if !got {
		cmd.Wait()
		return failNow(syscall.EIO, "server exited before it reported")
	}
	if code != 0 {
		cmd.Wait()
	}
	return code
}

func failNow(code syscall.Errno, msg string) int {
	logErr("E%d: %s", code, msg)
	if !stderrIsJournal() {
		fmt.Fprintf(os.Stderr, "E%d: %s\n", code, msg)
	}
	return int(code)
}

// codeOf reads the code back out of an "E98: message" line.
func codeOf(line string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(strings.SplitN(line, ":", 2)[0], "E"))
	if err != nil || n <= 0 {
		return int(syscall.EIO)
	}
	return n
}

func errnoOf(err error) syscall.Errno {
	var e syscall.Errno
	if errors.As(err, &e) {
		return e
	}
	return syscall.EIO
}
