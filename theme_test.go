package www

import (
	"os"
	"strings"
	"testing"
)

// A page's header offers the three colour modes, and the stylesheet and
// the script hold what makes them work: every colour has both sides, the
// root's data-theme picks one, and the script keeps the reader's choice.
func TestColourMode(t *testing.T) {
	s, _, _ := serveTree(t)
	body := get(s.handler(), "/a/").Body.String()
	want := `<span class="theme"><button data-theme="dark" title="dark pages">dark</button><button data-theme="light" title="light pages">light</button><button data-theme="system" title="pages follow the system">system</button></span>`
	if !strings.Contains(body, want) {
		t.Errorf("the header lacks the colour modes:\n%s", body)
	}
	css, err := os.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`:root[data-theme="dark"] { color-scheme: dark; }`, `:root[data-theme="light"] { color-scheme: light; }`,
		`--page: light-dark(#f6f6f6, #1c1c1c);`, `:root:not([data-theme]) button[data-theme="system"]`} {
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
	for _, w := range []string{`localStorage.getItem(themeKey)`, `document.documentElement.dataset.theme = t`, `closest("span.theme button")`} {
		if !strings.Contains(string(js), w) {
			t.Errorf("the script lacks %s", w)
		}
	}
}
