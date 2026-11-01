package main

import (
	"net/http"
	"strings"
	"testing"
)

// A path breaks after its slashes, and a URL keeps // with its scheme.
func TestSegments(t *testing.T) {
	for in, want := range map[string]string{
		"~/a b/c":        `<span class="seg">~/</span><wbr><span class="seg">a b/</span><wbr><span class="seg">c</span>`,
		"/tmp/x/":        `<span class="seg">/</span><wbr><span class="seg">tmp/</span><wbr><span class="seg">x/</span>`,
		"http://h:80/a/": `<span class="seg">http://</span><wbr><span class="seg">h:80/</span><wbr><span class="seg">a/</span>`,
		"<x>/y":          `<span class="seg">&lt;x&gt;/</span><wbr><span class="seg">y</span>`,
	} {
		if got := segments(in); got != want {
			t.Errorf("segments(%q) = %s, want %s", in, got, want)
		}
	}
}

// A missing path's 404 turns a coin above its message, hidden from screen
// readers. A refusal says why without one; TestLinksRefusedSayWhy checks.
func TestNotFoundCoin(t *testing.T) {
	s, _, _ := serveTree(t)
	rec := get(s.handler(), "/nowhere.md")
	body := rec.Body.String()
	if rec.Code != http.StatusNotFound || !strings.Contains(body, `<p class="coin" aria-hidden="true">`) || !strings.Contains(body, `</p>
<p class="reason">Not Found</p>`) || !strings.Contains(body, `<th>not found</th><td><span class="seg">http://</span><wbr><span class="seg">127.31.1.1:8080/</span><wbr><span class="seg">nowhere.md</span></td>`) {
		t.Errorf("status %d, want 404 with the coin and the message: %s", rec.Code, body)
	}
}
