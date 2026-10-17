package main

import (
	"bytes"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain keeps the tests' entries out of the user's journal and state
// directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mdserve-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	journalSocket = filepath.Join(dir, "no-journal")
	journalStream = filepath.Join(dir, "no-journal-stream")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// useJournal points the logger at a fresh socket and returns what reaches
// it; with sock "" it points at none, so entries go to the file.
func useJournal(t *testing.T, sock string) {
	t.Helper()
	old := journalSocket
	logger.mu.Lock()
	if logger.conn != nil {
		logger.conn.Close()
	}
	logger.conn, logger.tried, logger.ep = nil, false, ""
	journalSocket = sock
	if sock == "" {
		journalSocket = filepath.Join(t.TempDir(), "none")
	}
	logger.mu.Unlock()
	t.Cleanup(func() {
		logger.mu.Lock()
		if logger.conn != nil {
			logger.conn.Close()
		}
		logger.conn, logger.tried, logger.ep = nil, false, ""
		journalSocket = old
		logger.mu.Unlock()
	})
}

func TestJournalEntry(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "socket")
	pc, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		t.Skip("no unix datagram sockets:", err)
	}
	defer pc.Close()
	useJournal(t, sock)
	setLogEndpoint("127.31.1.1-80")
	logErr("E5: git: exit 1\nline two")

	pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 65536)
	n, err := pc.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	got := buf[:n]
	for _, want := range []string{"PRIORITY=3\n", "SYSLOG_IDENTIFIER=mdserve\n", "MDSERVE_ENDPOINT=127.31.1.1-80\n"} {
		if !bytes.Contains(got, []byte(want)) {
			t.Errorf("entry lacks %q:\n%q", want, got)
		}
	}
	// A message with a newline goes as its length and bytes.
	msg := "E5: git: exit 1\nline two"
	var want bytes.Buffer
	want.WriteString("MESSAGE\n")
	binary.Write(&want, binary.LittleEndian, uint64(len(msg)))
	want.WriteString(msg + "\n")
	if !bytes.HasPrefix(got, want.Bytes()) {
		t.Errorf("message encoded as %q, want %q", got, want.Bytes())
	}
}

func TestLogFileFallback(t *testing.T) {
	useJournal(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	setLogEndpoint("127.31.1.1-8080")
	logInfo("server starts\nsecond line")
	path, _ := logFile()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := string(b)
	if !strings.Contains(line, " 127.31.1.1-8080 info server starts\n    second line\n") {
		t.Errorf("log line %q", line)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("log mode %o, want 600", info.Mode().Perm())
	}
}

// The file rotates once at maxLogFile, and the next entry starts a new one.
func TestLogFileRotates(t *testing.T) {
	useJournal(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, _ := logFile()
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, bytes.Repeat([]byte("x"), maxLogFile), 0o600)
	logInfo("after")
	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != maxLogFile {
		t.Fatalf("log.1: %v %v", info, err)
	}
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), " info after\n") || len(b) > 200 {
		t.Errorf("new log %q", b)
	}
}

func TestCommandLine(t *testing.T) {
	got := commandLine([]string{"-n", "--share", "--secret=abc", "my docs", "it's"})
	want := `mdserve -n --share --secret=<token> 'my docs' 'it'\''s'`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// Requests are logged but for the ones each page brings along, and a
// secret tree's token stays out.
func TestRequestLog(t *testing.T) {
	useJournal(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, tr, _ := serveTree(t)
	h := s.handler()
	for _, p := range []string{"/_mdserve/style.css", "/favicon.ico", "/_mdserve/live?path=/", "/index.md"} {
		get(h, p)
	}
	tr.prefix, tr.secret = "/s3cr3t/", true
	get(h, "/s3cr3t/index.md?raw")
	get(h, "/s3cr3t?log")

	path, _ := logFile()
	b, _ := os.ReadFile(path)
	log := string(b)
	for _, skip := range []string{"style.css", "favicon", "/_mdserve/live", "s3cr3t"} {
		if strings.Contains(log, skip) {
			t.Errorf("log holds %s:\n%s", skip, log)
		}
	}
	if !strings.Contains(log, " info GET /index.md 200 ") || !strings.Contains(log, " info GET /<token>/index.md?raw 200 ") {
		t.Errorf("log lacks the page requests:\n%s", log)
	}
	if !strings.Contains(log, " info GET /<token>?log 302 ") {
		t.Errorf("log lacks the bare secret path:\n%s", log)
	}
}

// A shared server logs who asked.
func TestRequestLogShared(t *testing.T) {
	useJournal(t, "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s, tr, _ := serveTree(t)
	s.shared, tr.shared = true, true
	get(s.handler(), "/index.md")
	path, _ := logFile()
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), " from 192.0.2.1:1234\n") {
		t.Errorf("shared log lacks the visitor:\n%s", b)
	}
}
