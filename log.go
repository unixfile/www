package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
)

// mdserve logs every action, so a later look can tell what the user and
// mdserve did: each run with its arguments, each server's start and stop
// with the cause, trees joining and leaving, and page requests. Entries go
// to the systemd journal, tagged mdserve, or to a file in the state
// directory where no journal runs. Nothing is filtered when written; the
// journal's PRIORITY filters when read.

// Syslog priorities, as the journal's PRIORITY field takes them.
const (
	prioErr  = 3
	prioInfo = 6
)

// journalSocket takes entries in the journal's native protocol, and
// journalStream takes a process's stderr; tests point them elsewhere.
var (
	journalSocket = "/run/systemd/journal/socket"
	journalStream = "/run/systemd/journal/stdout"
)

// maxEntry caps a message, so one entry fits a datagram.
const maxEntry = 8 << 10

// maxLogFile is the size at which the fallback file rotates to log.1.
const maxLogFile = 1 << 20

var logger struct {
	mu    sync.Mutex
	ep    string // the endpoint this process acts on, eg 127.31.1.1-80, or ""
	conn  *net.UnixConn
	tried bool
}

// setLogEndpoint tags this process's later entries with ep.
func setLogEndpoint(ep string) {
	logger.mu.Lock()
	logger.ep = ep
	logger.mu.Unlock()
}

func logInfo(format string, a ...any) { logEntry(prioInfo, fmt.Sprintf(format, a...)) }
func logErr(format string, a ...any)  { logEntry(prioErr, fmt.Sprintf(format, a...)) }

// logEntry sends msg to the journal, or to the fallback file when the
// journal is absent or refuses it. A lost entry is not worth failing for.
func logEntry(prio int, msg string) {
	if len(msg) > maxEntry {
		msg = msg[:maxEntry] + " [cut]"
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if !logger.tried {
		logger.tried = true
		if c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: journalSocket, Net: "unixgram"}); err == nil {
			logger.conn = c
		}
	}
	if logger.conn != nil {
		if _, err := logger.conn.Write(journalEntry(prio, logger.ep, msg)); err == nil {
			return
		}
		// journald may have restarted; the next entry dials again.
		logger.conn.Close()
		logger.conn, logger.tried = nil, false
	}
	appendLogFile(prio, logger.ep, msg)
}

// journalEntry encodes one entry in the native protocol: KEY=value lines,
// and a value that holds a newline as the key, a newline, its length as a
// little-endian uint64, the value and a newline.
func journalEntry(prio int, ep, msg string) []byte {
	var b bytes.Buffer
	field := func(k, v string) {
		if !strings.Contains(v, "\n") {
			b.WriteString(k + "=" + v + "\n")
			return
		}
		b.WriteString(k + "\n")
		binary.Write(&b, binary.LittleEndian, uint64(len(v)))
		b.WriteString(v + "\n")
	}
	field("MESSAGE", msg)
	field("PRIORITY", fmt.Sprint(prio))
	field("SYSLOG_IDENTIFIER", "mdserve")
	if ep != "" {
		field("MDSERVE_ENDPOINT", ep)
	}
	return b.Bytes()
}

// logFile is the fallback log, eg ~/.local/state/mdserve/log.
func logFile() (string, error) {
	base, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mdserve", "log"), nil
}

// appendLogFile writes one line: time, PID, endpoint, priority and the
// message, its later lines indented. Each process opens the file per
// entry, so a rotation by one process never strands another on log.1.
func appendLogFile(prio int, ep, msg string) {
	path, err := logFile()
	if err != nil {
		return
	}
	f, err := openLogFile(path)
	if err != nil {
		return
	}
	defer f.Close()
	if ep == "" {
		ep = "-"
	}
	level := "info"
	if prio <= prioErr {
		level = "err"
	}
	line := fmt.Sprintf("%s %d %s %s %s\n", time.Now().Format("2006-01-02T15:04:05.000Z07:00"),
		os.Getpid(), ep, level, strings.ReplaceAll(msg, "\n", "\n    "))
	f.WriteString(line)
}

// openLogFile opens path for appending, after rotating it to path.1 once
// it reaches maxLogFile. An flock orders rotations: a process that waited
// finds a new file at path and leaves it be.
func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err != nil || info.Size() < maxLogFile {
		return f, nil
	}
	flock(f, syscall.LOCK_EX)
	if info, err := os.Stat(path); err == nil && info.Size() >= maxLogFile {
		if fi, err := f.Stat(); err == nil && os.SameFile(info, fi) {
			os.Rename(path, path+".1")
		}
	}
	f.Close()
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
}

// logSink is where a detached server's stderr goes: a journal stream, or
// the fallback file. A panic writes there, past every logger. It returns
// nil when neither opens, and stderr then goes nowhere, as before.
func logSink() *os.File {
	if c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: journalStream, Net: "unix"}); err == nil {
		defer c.Close()
		// The stream's header: identifier, unit, priority, level prefix,
		// and forwarding to syslog, kmsg and console.
		if _, err := c.Write([]byte("mdserve\n\n3\n0\n0\n0\n0\n")); err == nil {
			if f, err := c.File(); err == nil {
				return f
			}
		}
	}
	path, err := logFile()
	if err != nil {
		return nil
	}
	f, err := openLogFile(path)
	if err != nil {
		return nil
	}
	return f
}

// build names the running binary and its commit, eg /usr/bin/mdserve
// 54cca2a, with +modified for a build from uncommitted code. A stale binary
// is the first thing to rule out.
func build() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "mdserve"
	}
	return exe + " " + buildRev()
}

// buildRev names the commit the binary was built from, eg 54cca2a, with
// +modified for a build from uncommitted code.
func buildRev() string {
	rev, modified := "", false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if rev == "" {
		rev = "unknown commit"
	}
	if modified {
		rev += "+modified"
	}
	return rev
}

// commandLine quotes args for the log as a shell would need them, with a
// secret token hidden.
func commandLine(args []string) string {
	out := []string{"mdserve"}
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--secret=") && a != "--secret=":
			a = "--secret=<token>"
		case a == "" || strings.ContainsAny(a, " \t\n'\"\\$`*?[]{}()<>|&;#~"):
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}

// logRun records an invocation: its arguments, directory and binary.
func logRun(args []string) {
	cwd, _ := os.Getwd()
	logInfo("run: %s (in %s, %s)", commandLine(args), cwd, build())
}

// logHelp tells --help where the log is.
func logHelp() string {
	where := "  journalctl --user -t mdserve\n"
	if info, err := os.Stat(journalSocket); err != nil || info.Mode().Type() != os.ModeSocket {
		where = "  " + filepath.Join("$XDG_STATE_HOME", "mdserve", "log") + "\n"
		if p, err := logFile(); err == nil {
			where = "  " + p + "\n"
		}
	}
	return "\nlog: every run, server event and page request\n" + where
}

// logWriter takes what net/http logs, such as a handler's panic, as errors.
type logWriter struct{}

func (logWriter) Write(b []byte) (int, error) {
	logErr("%s", strings.TrimRight(string(b), "\n"))
	return len(b), nil
}
