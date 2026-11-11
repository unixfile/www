package www

import (
	"os"
	"path/filepath"
	"regexp"
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
	if !strings.Contains(body, `<li class="cmds"><span>ls</span> <a href="?details">ls -l</a></li>`) {
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
		`<li class="cmds"><a href="./">ls</a> <span>ls -l</span></li>`,
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

// keyAttrs matches the key a sort flag's link names, which the tests of
// the links' order and targets leave out.
var keyAttrs = regexp.MustCompile(` title="Alt\+[^"]*" data-key="[^"]*"`)

// The flags over the details view's columns sort it as ls does: a first
// click sorts, a second reverses, a third returns to name order. The
// view's command names the flags, and directory links keep them.
func TestDetailsSort(t *testing.T) {
	s, _, dir := serveTree(t)
	os.MkdirAll(filepath.Join(dir, "s", "d"), 0o755)
	now := time.Now()
	for i, f := range []struct {
		name string
		size int
		age  time.Duration
	}{{"big", 300, 3 * time.Hour}, {"mid", 50, time.Hour}, {"small", 5, 2 * time.Hour}} {
		p := filepath.Join(dir, "s", f.name)
		if err := os.WriteFile(p, make([]byte, f.size), 0o644); err != nil {
			t.Fatal(i, err)
		}
		os.Chtimes(p, now.Add(-f.age), now.Add(-f.age))
	}
	old := now.Add(-5 * time.Hour)
	os.Chtimes(filepath.Join(dir, "s", "d"), old, old)
	h := s.handler()
	for q, want := range map[string]struct {
		cmd, size, age, name string
		order                []string
		dirHref              string
	}{
		"?details":     {"ls -l", `<a href="?details=S">-S</a>`, `<a href="?details=t">-t</a>`, `<a class="on" href="?details=r">-</a>`, []string{"d/", "big", "mid", "small"}, "d/?details"},
		"?details=S":   {"ls -lS", `<a class="on" href="?details=rS">-S</a>`, `<a href="?details=t">-t</a>`, `<a href="?details">-</a>`, []string{"big", "mid", "small", "d/"}, "d/?details=S"},
		"?details=rS":  {"ls -lrS", `<a class="on" href="?details">-rS</a>`, `<a href="?details=t">-t</a>`, `<a href="?details">-</a>`, []string{"d/", "small", "mid", "big"}, "d/?details=rS"},
		"?details=t":   {"ls -lt", `<a href="?details=S">-S</a>`, `<a class="on" href="?details=rt">-t</a>`, `<a href="?details">-</a>`, []string{"mid", "small", "big", "d/"}, "d/?details=t"},
		"?details=rt":  {"ls -lrt", `<a href="?details=S">-S</a>`, `<a class="on" href="?details">-rt</a>`, `<a href="?details">-</a>`, []string{"d/", "big", "small", "mid"}, "d/?details=rt"},
		"?details=r":   {"ls -lr", `<a href="?details=S">-S</a>`, `<a href="?details=t">-t</a>`, `<a class="on" href="?details">-r</a>`, []string{"small", "mid", "big", "d/"}, "d/?details=r"},
		"?details=xyz": {"ls -l", `<a href="?details=S">-S</a>`, `<a href="?details=t">-t</a>`, `<a class="on" href="?details=r">-</a>`, []string{"d/", "big", "mid", "small"}, "d/?details"},
	} {
		body := keyAttrs.ReplaceAllString(get(h, "/s/"+q).Body.String(), "")
		for _, w := range []string{"<span>" + want.cmd + "</span>", `<a href="./">ls</a>`,
			`<li class="flags"><span class="lead num size">` + want.size + `</span> <span class="lead num age">` + want.age + `</span> <span>` + want.name + `</span></li>`,
			`<a href="` + want.dirHref + `">d/</a>`} {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %s:\n%s", q, w, body)
			}
		}
		at := 0
		for _, n := range want.order {
			i := strings.Index(body, ">"+n+"</a></li>")
			if i < at {
				t.Errorf("%s: %s is out of order %v:\n%s", q, n, want.order, body)
			}
			at = i
		}
	}
	// Each flag names its key, which the script reads from data-key.
	body := get(h, "/s/?details=S").Body.String()
	for _, w := range []string{`<a class="on" href="?details=rS" title="Alt+S" data-key="s">-S</a>`,
		`<a href="?details=t" title="Alt+T" data-key="t">-t</a>`, `<a href="?details" title="Alt+-" data-key="-">-</a>`} {
		if !strings.Contains(body, w) {
			t.Errorf("?details=S lacks %s:\n%s", w, body)
		}
	}
	js, err := os.ReadFile("assets/live.js")
	if err != nil || !strings.Contains(string(js), `querySelectorAll("main li.flags a[data-key]")`) {
		t.Errorf("the script does not read the flags' keys: %v", err)
	}
	// The names view has no flags.
	if body := get(h, "/s/").Body.String(); strings.Contains(body, `class="flags"`) {
		t.Errorf("the names view has sort flags:\n%s", body)
	}
}

// A version has no ages, so no -t, and ?details=t there is name order.
func TestDetailsSortOfAVersion(t *testing.T) {
	s, dir := repoTree(t)
	h := s.handler()
	rec := get(h, "/@"+head(t, dir, "HEAD")[:7]+"/a/?details=t")
	for rec.Code == 301 || rec.Code == 302 || rec.Code == 308 {
		rec = get(h, rec.Header().Get("Location"))
	}
	body := keyAttrs.ReplaceAllString(rec.Body.String(), "")
	if !strings.Contains(body, `<li class="flags"><span class="lead num size"><a href="?details=S">-S</a></span> <span class="lead num age"></span> <span><a class="on" href="?details=r">-</a></span></li>`) ||
		!strings.Contains(body, "<span>ls -l</span>") {
		t.Errorf("version details sorted by time (%d):\n%s", rec.Code, body)
	}
}
