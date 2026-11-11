package www

import (
	"os"
	"strings"
	"testing"
)

// A page's header offers the three colour modes in a menu, and the stylesheet and
// the script hold what makes them work: every colour has both sides, the
// root's data-theme picks one, and the script keeps the reader's choice.
func TestColourMode(t *testing.T) {
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/a/").Body.String()
	want := `<details class="theme"><summary title="light or dark pages"><span data-theme="light">light</span><span data-theme="system">system</span><span data-theme="dark">dark</span></summary><span><button data-theme="light" title="light pages">light</button><button data-theme="system" title="pages follow the system">system</button><button data-theme="dark" title="dark pages">dark</button></span></details>`
	if !strings.Contains(body, want) {
		t.Errorf("the header lacks the colour modes:\n%s", body)
	}
	css, err := os.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`:root[data-theme="dark"] { color-scheme: dark; }`, `:root[data-theme="light"] { color-scheme: light; }`,
		`--page: light-dark(#f6f6f6, #1c1c1c);`, `:root:not([data-theme]) summary > span[data-theme="system"]`, `button[data-theme="light"] { color-scheme: light; }`,
		`button[data-theme="system"] { color-scheme: light dark; }`, `button[data-theme="dark"] { color-scheme: dark; }`} {
		if !strings.Contains(string(css), w) {
			t.Errorf("the stylesheet lacks %s", w)
		}
	}
	// A colour bound to the system's mode alone would not follow the
	// reader's choice.
	if strings.Contains(string(css), "prefers-color-scheme") {
		t.Error("the stylesheet has a rule under prefers-color-scheme, which the header's modes cannot switch")
	}
	js, err := os.ReadFile("assets/live.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`localStorage.getItem(themeKey)`, `document.documentElement.dataset.theme = t`, `closest("details.theme button")`} {
		if !strings.Contains(string(js), w) {
			t.Errorf("the script lacks %s", w)
		}
	}
}
