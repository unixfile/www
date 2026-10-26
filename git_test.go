package main

import (
	"fmt"
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
	s, _, dir := serveTree(t)
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
	s, dir := repoTree(t)
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
	// Each commit folds; its diff loads from the commit page, which the
	// fold also links in its place.
	c := head(t, dir, "HEAD")
	for _, want := range []string{
		`<li><details data-diff="?commit=` + c + `">` + "\n<summary><span class=\"own lead\"><span class=\"date\" title=\"" + c[:7],
		`</span></span> <span class="subject">Rename the package &lt;b&gt;</span></summary>` + "\n" + `<p class="own links">commit <a href="?commit=` + c + `">` + c[:7] + `</a> · <a class="then"`,
		`<div class="diffs"><p class="own"><a href="?commit=` + c + `">diff</a></p></div>` + "\n</details></li>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q:\n%s", want, body)
		}
	}
	if body := get(h, "/index.md?log").Body.String(); strings.Contains(body, "Rename") || !strings.Contains(body, "Add the docs") {
		t.Errorf("file history holds other files' commits:\n%s", body)
	}
	if body := get(h, "/.gitignore?log").Body.String(); !strings.Contains(body, `<a href="/.gitignore">.gitignore</a>`) {
		t.Errorf("dot file's crumb lost its dot:\n%s", body)
	}
	if body := get(h, "/a/b.go?log").Body.String(); !strings.Contains(body, "Rename") || !strings.Contains(body, `<a href="/a/b.go">b.go</a>`) {
		t.Errorf("file history:\n%s", body)
	}
	if body := get(h, "/untracked/?log").Body.String(); !strings.Contains(body, "No commit touches untracked/.") {
		t.Errorf("untracked history:\n%s", body)
	}
}

// A commit with no message still folds: its line holds the hash and date
// alone. A message without a blank line is all subject.
func TestHistoryOddMessages(t *testing.T) {
	s, dir := repoTree(t)
	os.WriteFile(filepath.Join(dir, "index.md"), []byte("# Changed\n"), 0o644)
	if out, err := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@x",
		"commit", "-qa", "--allow-empty-message", "-m", "").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "index.md"), []byte("# Again\n"), 0o644)
	commitAll(t, dir, "One paragraph\nthat runs on")
	body := get(s.handler(), "/index.md?log").Body.String()
	empty, long := head(t, dir, "HEAD~1"), head(t, dir, "HEAD")
	for _, want := range []string{
		`<details data-diff="?commit=` + empty + `">`,
		`</span></span> <span class="subject"></span></summary>`,
		`<details data-diff="?commit=` + long + `">`,
		`</span></span> <span class="subject">One paragraph that runs on</span></summary>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q:\n%s", want, body)
		}
	}
}

// A directory without its slash keeps ?log through the redirect.
func TestHistoryRedirectKeepsQuery(t *testing.T) {
	s, _ := repoTree(t)
	if rec := get(s.handler(), "/a?log"); rec.Header().Get("Location") != "/a/?log" {
		t.Errorf("redirect to %q", rec.Header().Get("Location"))
	}
}

// Doc and code pages say at the top whether they match their last
// commit, which they name on hover; an edit since makes them changed.
func TestPageTopNamesLastCommit(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	for p, subject := range map[string]string{"/index.md": "Add the docs", "/a/b.go": "Rename the package &lt;b&gt;"} {
		body := get(h, p).Body.String()
		if !strings.Contains(body, `<details class="own latest" data-timeline="?timeline">`) || !strings.Contains(body, ` · `+subject+` · `) || !strings.Contains(body, `">current</span></summary>`) ||
			!strings.Contains(body, `<div class="versions"><a href="?log">&lt;(git log)</a></div>`) || strings.Contains(body, `class="own now"`) {
			t.Errorf("%s: no top line with %q:\n%s", p, subject, body)
		}
	}
	if id := head(t, dir, "HEAD:a/b.go"); blobID([]byte("package b\n"), len(id)) != id {
		t.Errorf("blob id %s, git says %s", blobID([]byte("package b\n"), len(id)), id)
	}
	os.WriteFile(filepath.Join(dir, "a", "b.go"), []byte("package c\n"), 0o644)
	if body := get(h, "/a/b.go").Body.String(); !strings.Contains(body, `">changed</span></summary>`) {
		t.Errorf("edited page is not changed:\n%s", body)
	}
	if body := get(h, "/untracked/x.txt").Body.String(); strings.Contains(body, "?log") {
		t.Errorf("untracked file has a top line:\n%s", body)
	}
}

// Outside a repo nothing leads to history, and the page says why.
func TestNoRepoNoHistory(t *testing.T) {
	needGit(t)
	s, _, _ := serveTree(t)
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
		"Body.\n\nKey: v\nOther-Key: w":                                    "Body.",
		"Claude-Session: https://x":                                        "",
		"Body.\n\nNot a trailer line":                                      "Body.\n\nNot a trailer line",
		"Body.\n\nKey: v\nplain":                                           "Body.",
		"Body.\n\n(cherry picked from commit abc)\nSigned-off-by: t <t@x>": "Body.",
		"Body.\n\nCo-authored-by: a\n  <a@x>\nKey: v":                      "Body.",
		"Body.\n\nOne\ntwo\nthree\nfour\nNote: five":                       "Body.\n\nOne\ntwo\nthree\nfour\nNote: five",
		"": "",
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

// head is the hash of HEAD in dir.
func head(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", rev).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

// A commit page shows the message and the diff of the path it came from,
// and links to the commit across the tree.
func TestCommitPage(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	c := head(t, dir, "HEAD")
	body := get(h, "/a/b.go?commit="+c).Body.String()
	for _, want := range []string{
		"Rename the package &lt;b&gt;",
		`<span class="del">-func F() {}</span><span class="add">+package b</span>`,
		`<a href="/?commit=` + c + `">All files</a>`,
		`<p class="own file"><a href="/a/b.go">a/b.go</a></p>`,
		`<a href="/a/b.go?log">&lt;(git log)</a>`,
		"</div>\n<div class=\"diffs\">\n<p class=\"own\">Changes to a/b.go.",
		"</pre>\n</div>\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("no %q:\n%s", want, body)
		}
	}
	if body := get(h, "/index.md?commit="+c).Body.String(); !strings.Contains(body, "It does not change index.md.") {
		t.Errorf("commit that skips the file:\n%s", body)
	}
	first := get(h, "/?commit="+head(t, dir, "HEAD~1")).Body.String()
	if !strings.Contains(first, `<a href="/index.md">index.md</a>`) || !strings.Contains(first, `<a href="/a/b.go">a/b.go</a>`) || strings.Contains(first, "All files") {
		t.Errorf("whole commit:\n%s", first)
	}
	for _, bad := range []string{"zz", "-p", head(t, dir, "HEAD:a/b.go")} {
		if rec := get(h, "/?commit="+bad); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d:\n%s", bad, rec.Code, rec.Body.String())
		}
	}
}

func TestDiffName(t *testing.T) {
	for in, want := range map[string]string{
		"a/x.md b/x.md":                     "x.md",
		"a/two words b/two words":           "two words",
		`"a/q\"uote" "b/q\"uote"`:           `q"uote`,
		`"a/tab\there.md" "b/tab\there.md"`: "tab\there.md",
	} {
		if got, ok := diffName(in); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
}

// A share's diffs leave out dot files.
func TestShareDiffSkipsDots(t *testing.T) {
	needGit(t)
	s := shareTree(t)
	dir := s.list()[0].dir
	if s.list()[0].secret {
		dir = s.list()[1].dir
	}
	exec.Command("git", "init", "-q", dir).Run()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=hidden\n"), 0o644)
	commitAll(t, dir, "Shared")
	exec.Command("chmod", "-R", "go+rX", filepath.Join(dir, ".git")).Run()
	body := get(s.handler(), "/docs/?commit="+head(t, dir, "HEAD")).Body.String()
	if !strings.Contains(body, "+docs/pub.txt") || strings.Contains(body, "hidden") {
		t.Errorf("share diff:\n%s", body)
	}
}

// A history that git was slow to give names the command that writes
// changed-path filters, until the repo has them. A share never names it.
func TestSlowHistoryNamesCommitGraph(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	defer func(d time.Duration) { slowGit = d }(slowGit)
	slowGit = -1
	hint := "git commit-graph write --reachable --changed-paths"
	if body := get(h, "/?log").Body.String(); !strings.Contains(body, hint) {
		t.Errorf("no hint without filters:\n%s", body)
	}
	if out, err := exec.Command("git", "-C", dir, "commit-graph", "write", "--reachable", "--changed-paths").CombinedOutput(); err != nil {
		t.Fatalf("commit-graph: %v\n%s", err, out)
	}
	if body := get(h, "/index.md?log").Body.String(); strings.Contains(body, hint) {
		t.Errorf("hint with filters:\n%s", body)
	}
	os.Remove(filepath.Join(dir, ".git", "objects", "info", "commit-graph"))
	if out, err := exec.Command("git", "-C", dir, "commit-graph", "write", "--reachable", "--split").CombinedOutput(); err != nil {
		t.Fatalf("commit-graph: %v\n%s", err, out)
	}
	if body := get(h, "/a/?log").Body.String(); !strings.Contains(body, hint) {
		t.Errorf("no hint for a split graph without filters:\n%s", body)
	}
	if out, err := exec.Command("git", "-C", dir, "commit-graph", "write", "--reachable", "--split=replace", "--changed-paths").CombinedOutput(); err != nil {
		t.Fatalf("commit-graph: %v\n%s", err, out)
	}
	if body := get(h, "/a/b.go?log").Body.String(); strings.Contains(body, hint) {
		t.Errorf("hint for a split graph with filters:\n%s", body)
	}
}

// A new tree's listing and README have their top lines cached before the
// first visit.
func TestWarmCachesFirstPages(t *testing.T) {
	_, dir := repoTree(t)
	os.WriteFile(filepath.Join(dir, "ReadMe.md"), []byte("# Hi\n"), 0o644)
	commitAll(t, dir, "Add a readme")
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	tr := newTree(root, dir, "/", "w")
	tr.warm()
	r := repoAt(t.Context(), dir)
	for _, name := range []string{".", "ReadMe.md"} {
		d, spec, _ := tr.gitPlace(name)
		key := fmt.Sprintf("log\x00%s\x00%s\x00%s\x00%s\x00%d\x00%t", r.gitDir, r.head(t.Context()), d, spec, 2, false)
		if _, ok := gitCache.get(key); !ok {
			t.Errorf("%s: top line not cached", name)
		}
	}
}

// A repo's fsmonitor program never runs, even for a command that reads
// the index.
func TestGitRunsNoFsmonitor(t *testing.T) {
	_, dir := repoTree(t)
	ran := filepath.Join(t.TempDir(), "ran")
	mon := filepath.Join(dir, ".git", "mon.sh")
	os.WriteFile(mon, []byte("#!/bin/sh\ntouch "+ran+"\n"), 0o755)
	if out, err := exec.Command("git", "-C", dir, "config", "core.fsmonitor", mon).CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	git(t.Context(), dir, "status")
	if _, err := os.Stat(ran); err == nil {
		t.Error("git ran the repo's fsmonitor")
	}
}
