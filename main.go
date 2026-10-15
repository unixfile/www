// Command mdserve browses trees of Markdown docs in the browser. It renders
// each page through pandoc when requested and mirrors the file tree in its
// URLs. Trees on one address share a server, which lives while their locks
// exist.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
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
	dir      string
	addr     string
	ep       netip.AddrPort
	fg       bool
	nolock   bool
	nobreaks bool
	noopen   bool
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: mdserve [-fg] [-nolock] [-nobreaks] [-noopen] [-addr ip[:port]] [dir]\n")
	flag.PrintDefaults()
}

func main() {
	var c config
	flag.StringVar(&c.addr, "addr", "", "loopback address to serve on, with an optional port (default 127.31.N.1:8080, N = uid - 1000)")
	flag.BoolVar(&c.fg, "fg", false, "when no server runs on the address yet, serve in the foreground until Ctrl-C")
	flag.BoolVar(&c.nolock, "nolock", false, "write no lockfile and serve alone in the foreground; never joins a server")
	flag.BoolVar(&c.nobreaks, "nobreaks", false, "join consecutive lines into one paragraph, as standard Markdown does")
	flag.BoolVar(&c.noopen, "noopen", false, "do not open a browser")
	flag.Usage = usage
	flag.Parse()
	switch flag.NArg() {
	case 0:
		c.dir = "."
	case 1:
		c.dir = flag.Arg(0)
	default:
		usage()
		os.Exit(int(syscall.EINVAL))
	}
	if c.nolock {
		c.fg = true
	}
	ep, err := endpointFor(c.addr, os.Getuid())
	if err != nil {
		os.Exit(failNow(errnoOf(err), err.Error()))
	}
	c.ep = ep

	fd := os.Getenv(readyEnv)
	switch {
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
			return netip.AddrPort{}, &errno{syscall.EINVAL, fmt.Sprintf("uid %d has no default address; pass -addr", uid)}
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
	case "msg":
		fmt.Println(text)
	case "err":
		fmt.Fprintln(os.Stderr, text)
	case "url":
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
