package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// commitAll commits every change in dir with message msg.
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-qm", msg}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// repoTree is serveTree in a git repo with two commits: one adds every
// file, the next changes a/b.go. untracked/ is in no commit.
func repoTree(t *testing.T) (*server, string) {
	t.Helper()
	needGit(t)
	s, _, dir := serveTree(t, false)
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.lock\n"), 0o644)
	commitAll(t, dir, "Add the docs\n\nThe first line\nwraps here.\n- a list item\n- another\n\nA second paragraph.")
	os.WriteFile(filepath.Join(dir, "a", "b.go"), []byte("package b\n"), 0o644)
	commitAll(t, dir, "Rename the package <b>")
	os.Mkdir(filepath.Join(dir, "untracked"), 0o755)
	os.WriteFile(filepath.Join(dir, "untracked", "x.txt"), []byte("x\n"), 0o644)
	return s, dir
}

// A tracked directory's listing opens its history from its first entry.
func TestListingLeadsToHistory(t *testing.T) {
	s, _ := repoTree(t)
	h := s.handler()
	entry := `<ul class="listing">` + "\n" + `<li class="log"><a href="?log">&lt;(git log)</a></li>`
	for _, p := range []string{"/", "/a/"} {
		if body := get(h, p).Body.String(); !strings.Contains(body, entry) {
			t.Errorf("%s: no history entry first:\n%s", p, body)
		}
	}
	if body := get(h, "/untracked/").Body.String(); strings.Contains(body, "?log") {
		t.Errorf("untracked directory leads to history:\n%s", body)
	}
}

// A directory's history covers its subtree; a file's covers the file.
func TestHistoryPage(t *testing.T) {
	s, _ := repoTree(t)
	h := s.handler()
	rec := get(h, "/?log")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d:\n%s", rec.Code, body)
	}
	add, ren := strings.Index(body, "Add the docs"), strings.Index(body, "Rename the package &lt;b&gt;")
	if add < 0 || ren < 0 || ren > add {
		t.Errorf("want both commits, newest first:\n%s", body)
	}
	if !strings.Contains(body, "<p>The first line wraps here.<br>- a list item<br>- another</p>\n<p>A second paragraph.</p>") {
		t.Errorf("body not in paragraphs:\n%s", body)
	}
	if !strings.Contains(body, `<span>&lt;(git log)</span>`) {
		t.Errorf("no history crumb:\n%s", body)
	}
	if body := get(h, "/index.md?log").Body.String(); strings.Contains(body, "Rename") || !strings.Contains(body, "Add the docs") {
		t.Errorf("file history holds other files' commits:\n%s", body)
	}
	if body := get(h, "/a/b.go?log").Body.String(); !strings.Contains(body, "Rename") || !strings.Contains(body, `<a href="/a/b.go">b.go</a>`) {
		t.Errorf("file history:\n%s", body)
	}
	if body := get(h, "/untracked/?log").Body.String(); !strings.Contains(body, "No commit touches untracked/.") {
		t.Errorf("untracked history:\n%s", body)
	}
}

// A directory without its slash keeps ?log through the redirect.
func TestHistoryRedirectKeepsQuery(t *testing.T) {
	s, _ := repoTree(t)
	if rec := get(s.handler(), "/a?log"); rec.Header().Get("Location") != "/a/?log" {
		t.Errorf("redirect to %q", rec.Header().Get("Location"))
	}
}

// Doc and code pages name their last commit at the foot.
func TestPageFootNamesLastCommit(t *testing.T) {
	needPandoc(t)
	s, _ := repoTree(t)
	h := s.handler()
	for p, subject := range map[string]string{"/index.md": "Add the docs", "/a/b.go": "Rename the package &lt;b&gt;"} {
		body := get(h, p).Body.String()
		if !strings.Contains(body, `<p class="mdserve foot"><a href="?log">&lt;(git log)</a> <span class="hash"`) || !strings.Contains(body, "</span> "+subject+"</p>") {
			t.Errorf("%s: no foot line with %q:\n%s", p, subject, body)
		}
	}
	if body := get(h, "/untracked/x.txt").Body.String(); strings.Contains(body, "?log") {
		t.Errorf("untracked file has a foot line:\n%s", body)
	}
}

// Outside a repo nothing leads to history, and the page says why.
func TestNoRepoNoHistory(t *testing.T) {
	needGit(t)
	s, _, _ := serveTree(t, false)
	h := s.handler()
	if body := get(h, "/").Body.String(); strings.Contains(body, "?log") {
		t.Errorf("history entry outside a repo:\n%s", body)
	}
	if rec := get(h, "/?log"); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not a git repository") {
		t.Errorf("status %d:\n%s", rec.Code, rec.Body.String())
	}
}

// A share shows history without trailers, and only while other users
// could read the repo. Its errors name no path.
func TestShareShowsHistory(t *testing.T) {
	needGit(t)
	s := shareTree(t)
	dir := s.list()[0].dir
	if s.list()[0].secret {
		dir = s.list()[1].dir
	}
	exec.Command("git", "init", "-q", dir).Run()
	commitAll(t, dir, "Shared subject\n\nThe body.\n\nClaude-Session: https://claude.ai/code/session_x\nSigned-off-by: t <t@x>")
	gitDir := filepath.Join(dir, ".git")
	exec.Command("chmod", "-R", "go+rX", gitDir).Run()
	h := s.handler()
	if body := get(h, "/docs/").Body.String(); !strings.Contains(body, `href="?log"`) {
		t.Errorf("share listing leads to no history:\n%s", body)
	}
	body := get(h, "/docs/?log").Body.String()
	if !strings.Contains(body, "Shared subject") || !strings.Contains(body, "<p>The body.</p>") {
		t.Errorf("share history:\n%s", body)
	}
	if strings.Contains(body, "claude.ai") || strings.Contains(body, "t@x") {
		t.Errorf("share history shows trailers:\n%s", body)
	}
	os.Chmod(filepath.Join(gitDir, "objects"), 0o700)
	if body := get(h, "/docs/").Body.String(); strings.Contains(body, "?log") {
		t.Errorf("history of a closed repo:\n%s", body)
	}
	rec := get(h, "/docs/?log")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No git history here.") || strings.Contains(rec.Body.String(), dir) {
		t.Errorf("closed repo: %d\n%s", rec.Code, rec.Body.String())
	}
}

// Loopback keeps trailers.
func TestLoopbackKeepsTrailers(t *testing.T) {
	s, dir := repoTree(t)
	commitAll(t, dir, "Third\n\nSigned-off-by: t <t@x>")
	if body := get(s.handler(), "/?log").Body.String(); !strings.Contains(body, "Signed-off-by: t &lt;t@x&gt;") {
		t.Errorf("trailer gone on loopback:\n%s", body)
	}
}

func TestDropTrailers(t *testing.T) {
	for in, want := range map[string]string{
		"Body.\n\nKey: v\nOther-Key: w": "Body.",
		"Claude-Session: https://x":     "",
		"Body.\n\nNot a trailer line":   "Body.\n\nNot a trailer line",
		"Body.\n\nKey: v\nplain":        "Body.\n\nKey: v\nplain",
		"":                              "",
	} {
		if got := dropTrailers(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// A commit reloads an open history page.
func TestLiveReloadsOnCommit(t *testing.T) {
	needGit(t)
	s, _, dir, ts := liveTree(t)
	exec.Command("git", "init", "-q", dir).Run()
	commitAll(t, dir, "First")
	lines := stream(t, s, ts, "/a/")
	time.Sleep(50 * time.Millisecond)
	exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "--allow-empty", "-m", "Second").Run()
	if !reloads(lines, 2*time.Second) {
		t.Fatal("no reload after a commit")
	}
}
