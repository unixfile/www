// Command mdserve browses trees of Markdown docs in the browser. It renders
// each page through pandoc when requested and mirrors the file tree in its
// URLs. Trees on one address share a server, which lives while their locks
// exist.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const (
	defaultPort = 8080
	// readyEnv names the descriptor a detached child reports its start on.
	readyEnv = "MDSERVE_READY_FD"
)

type config struct {
	dir     string
	addr    string
	ep      netip.AddrPort
	fg      bool
	noopen  bool
	help    bool
	restart bool
	list    bool
}

const usageText = `usage: mdserve [-fnh] [-a ip[:port]] [dir]
       mdserve --restart [-a ip[:port]]
       mdserve -l [-a ip[:port]] [dir]

  -a, --addr ip[:port]  serve on this loopback address; the port defaults to 8080
                        (default 127.31.N.1:8080, N = uid - 1000)
  -f, --foreground      when no server runs on the address yet, serve in the
                        foreground until Ctrl-C
  -n, --no-open         print the URL without opening a browser
      --restart         replace the server on the address with one running
                        this binary, serving the same trees
  -l, --list            print each served tree's URL and directory; with a
                        directory, only the trees at or below it
  -h, --help            print this help
`

// switches maps each long boolean option to its field; shorts names the
// ones with a short form.
func (c *config) switches() map[string]*bool {
	return map[string]*bool{
		"foreground": &c.fg,
		"no-open":    &c.noopen,
		"help":       &c.help,
		"restart":    &c.restart,
		"list":       &c.list,
	}
}

var shorts = map[byte]string{'f': "foreground", 'n': "no-open", 'h': "help", 'l': "list"}

// parseArgs reads options the GNU way. Short ones combine, as in -fn, and
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
	if c.restart && (len(dirs) > 0 || c.fg) {
		return c, &errno{syscall.EINVAL, "--restart takes no directory or -f"}
	}
	if c.list && (c.restart || c.fg) {
		return c, &errno{syscall.EINVAL, "--list takes no --restart or -f"}
	}
	switch len(dirs) {
	case 0:
		// --list without a directory lists every tree.
		if !c.list {
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
		code := failNow(errnoOf(err), err.Error())
		io.WriteString(os.Stderr, usageText)
		os.Exit(code)
	case c.help:
		io.WriteString(os.Stdout, usageText)
		os.Exit(0)
	}
	ep, err := endpointFor(c.addr, os.Getuid())
	if err != nil {
		os.Exit(failNow(errnoOf(err), err.Error()))
	}
	c.ep = ep

	fd := os.Getenv(readyEnv)
	switch {
	case c.restart:
		os.Exit(restart(c))
	case c.list:
		os.Exit(list(c))
	case fd != "":
		n, _ := strconv.Atoi(fd)
		os.Exit(run(c, &reporter{pipe: os.NewFile(uintptr(n), "ready")}))
	case c.fg:
		os.Exit(run(c, &reporter{noopen: c.noopen}))
	default:
		os.Exit(detach(c))
	}
}

// endpointFor returns where to serve: addr when given, else the user's own
// 127.31.N.1:8080 with N = uid - 1000, so users on one machine never
// collide. Only loopback serves; nothing beyond this machine may reach it.
func endpointFor(addr string, uid int) (netip.AddrPort, error) {
	if addr == "" {
		n := uid - 1000
		if n < 0 || n > 255 {
			return netip.AddrPort{}, &errno{syscall.EINVAL, fmt.Sprintf("uid %d has no default address; pass --addr", uid)}
		}
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 31, byte(n), 1}), defaultPort), nil
	}
	ep, err := netip.ParseAddrPort(addr)
	if err != nil {
		a, aerr := netip.ParseAddr(addr)
		if aerr != nil {
			return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want eg 127.31.1.2 or 127.31.1.2:9000"}
		}
		ep = netip.AddrPortFrom(a, defaultPort)
	}
	if !ep.Addr().Is4() || !ep.Addr().IsLoopback() || ep.Port() == 0 {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want an IPv4 loopback address and a port above 0"}
	}
	return ep, nil
}

// errno is an error with the code mdserve exits with.
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
	cmd := exec.Command("xdg-open", url)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// detach re-runs mdserve in a new session and relays its start report.
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
	fmt.Fprintf(os.Stderr, "E%d: %s\n", code, msg)
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
