package www

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0", 1023: "1023", 1024: "1.0K", 4097: "4.1K", 10239: "10K",
		12 << 10: "12K", 1<<20 - 1: "1.0M", 1 << 20: "1.0M", 5 << 30: "5.0G",
	} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestAgeOf(t *testing.T) {
	const day = 24 * time.Hour
	for d, want := range map[time.Duration]string{
		-time.Minute: "0m", 0: "0m", 59 * time.Minute: "59m", time.Hour: "1h",
		23 * time.Hour: "23h", day: "1d", 6 * day: "6d", 7 * day: "1w",
		364 * day: "52w", 365 * day: "1y", 3 * 365 * day: "3y",
	} {
		if got := ageOf(d); got != want {
			t.Errorf("ageOf(%v) = %q, want %q", d, got, want)
		}
	}
}

// The names view shows its own command as text, leads to the details
// view and shows no fields.
func TestListingLeadsToDetails(t *testing.T) {
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/a/").Body.String()
	if !strings.Contains(body, `<li class="cmds"><span>&lt;(ls)</span> <a href="?details">&lt;(ls -l)</a></li>`) {
		t.Errorf("no details entry:\n%s", body)
	}
	if strings.Contains(body, `class="lead`) || !strings.Contains(body, `<li><a href="../">../</a></li>`) {
		t.Errorf("names view shows fields:\n%s", body)
	}
}

// The details view leads each row with size and age, leads back to the
// names view, and stays on in the directories it links.
func TestDetailsView(t *testing.T) {
	s, _, dir := serveTree(t)
	old := time.Now().Add(-3 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a", "b.go"), old, old); err != nil {
		t.Fatal(err)
	}
	h := s.handler()
	body := get(h, "/a/?details").Body.String()
	for _, want := range []string{
		`<li class="cmds"><a href="./">&lt;(ls)</a> <span>&lt;(ls -l)</span></li>`,
		`<li><span class="lead num size"></span> <span class="lead num age"></span> <a href="../?details">../</a></li>`,
		`<li><span class="lead num size">23</span> <span class="lead num age" title="` + exact(old) + `">3d</span> <a href="b.go">b.go</a></li>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("details of a/ lacks %s:\n%s", want, body)
		}
	}
	body = get(h, "/?details").Body.String()
	if !strings.Contains(body, `<span class="lead num size"></span> <span class="lead num age" title=`) || !strings.Contains(body, `<a href="a/?details">a/</a>`) {
		t.Errorf("a directory shows a size or leaves the details view:\n%s", body)
	}
	if !strings.Contains(body, `<a href="escape/?details" title=`) || !strings.Contains(body, `<span class="lead num size"></span> <span class="lead num age"></span> <a href="escape/?details"`) {
		t.Errorf("a refused entry shows fields:\n%s", body)
	}
}

// A version has sizes but no ages, since git keeps no modification times.
func TestDetailsOfAVersion(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	at := "/@" + head(t, dir, "HEAD")[:7] + "/a/?details"
	rec := get(h, at)
	for rec.Code == 301 || rec.Code == 302 || rec.Code == 308 {
		rec = get(h, rec.Header().Get("Location"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<span class="lead num age"></span> <a href="b.go">`) || strings.Contains(body, `<span class="lead num size"></span> <span class="lead num age"></span> <a href="b.go">`) {
		t.Errorf("version details of a/ (%d):\n%s", rec.Code, body)
	}
}
