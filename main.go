// Package www browses trees of Markdown docs in the browser. It renders
// each page with goldmark when requested and mirrors the file tree in its
// URLs. Trees on one address share a server, which lives while their locks
// exist. The executables under cmd/ share it.
package www

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// readyEnv names the descriptor a detached child reports its start on.
var readyEnv = envPrefix + "READY_FD"

// Main runs the program on the process's arguments and exits.
func Main() {
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
	case c.public:
		os.Exit(servePublic(c))
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
