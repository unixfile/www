// Command mdserve browses a directory of Markdown docs in the browser.
// It renders each page through pandoc when requested, mirrors the file tree
// in its URLs and stops when its lockfile is deleted.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultPort = 7417
	// readyEnv names the descriptor a detached child reports its start on.
	readyEnv = "MDSERVE_READY_FD"
)

type config struct {
	dir      string
	port     int
	portSet  bool
	fg       bool
	nolock   bool
	nobreaks bool
	noopen   bool
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: mdserve [-fg] [-nolock] [-nobreaks] [-noopen] [-port n] [dir]\n")
	flag.PrintDefaults()
}

func main() {
	var c config
	flag.IntVar(&c.port, "port", defaultPort, "port on 127.0.0.1; if the default is taken, a free port is used")
	flag.BoolVar(&c.fg, "fg", false, "stay in the foreground until Ctrl-C")
	flag.BoolVar(&c.nolock, "nolock", false, "write no lockfile; implies -fg")
	flag.BoolVar(&c.nobreaks, "nobreaks", false, "join consecutive lines into one paragraph, as standard Markdown does")
	flag.BoolVar(&c.noopen, "noopen", false, "do not open a browser")
	flag.Usage = usage
	flag.Parse()
	flag.Visit(func(f *flag.Flag) { c.portSet = c.portSet || f.Name == "port" })
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

func listen(c config) (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", c.port))
	if err != nil && !c.portSet && errors.Is(err, syscall.EADDRINUSE) {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	return ln, err
}

func run(c config, rep *reporter) int {
	dir, err := filepath.Abs(c.dir)
	if err != nil {
		return rep.fail(errnoOf(err), "bad directory: "+err.Error())
	}
	info, err := os.Stat(dir)
	if err != nil {
		return rep.fail(errnoOf(err), "cannot open "+dir+": "+err.Error())
	}
	if !info.IsDir() {
		return rep.fail(syscall.ENOTDIR, "not a directory: "+dir)
	}
	if !c.nolock {
		if l, ok := findLive(dir); ok {
			rep.send("msg", "already serving "+dir+" at "+l.url)
			rep.send("url", l.url)
			rep.finish()
			return 0
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return rep.fail(errnoOf(err), "cannot open "+dir+": "+err.Error())
	}
	defer root.Close()

	ln, err := listen(c)
	if err != nil {
		return rep.fail(errnoOf(err), "cannot listen: "+err.Error())
	}
	port := ln.Addr().(*net.TCPAddr).Port
	home, _ := os.UserHomeDir()
	prefix, label := mount(dir, home)
	filter := ""
	if !c.nobreaks {
		if filter, err = writeFilter(); err != nil {
			ln.Close()
			return rep.fail(errnoOf(err), "cannot write pandoc filter: "+err.Error())
		}
		defer os.Remove(filter)
	}
	s := newServer(root, prefix, label, filter)
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, s.prefixURL())

	var lk *lock
	if !c.nolock {
		if lk, err = createLock(dir, port, url); err != nil {
			ln.Close()
			return rep.fail(errnoOf(err), "cannot write lockfile: "+err.Error())
		}
	}

	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	stopped := make(chan struct{})
	// Every way to stop runs this: a signal, the deleted lock, the button.
	stop := sync.OnceFunc(func() {
		if lk != nil {
			lk.remove()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		close(stopped)
	})
	s.stop = stop

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sigs
		stop()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if lk != nil {
		go lk.watch(ctx, time.Second, stop)
	}

	rep.send("msg", "serving "+dir+" at "+url)
	if lk != nil {
		rep.send("msg", "stop: rm "+lk.path+", or the Stop server button")
	} else {
		rep.send("msg", "stop: Ctrl-C, or the Stop server button")
	}
	rep.send("url", url)
	rep.finish()

	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		stop()
		fmt.Fprintf(os.Stderr, "E%d: %s\n", syscall.EIO, err)
		return int(syscall.EIO)
	}
	<-stopped
	return 0
}
