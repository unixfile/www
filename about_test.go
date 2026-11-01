package main

import (
	"regexp"
	"strings"
	"testing"
)

// The build stamp shows only with --debug, and never on a share. It names
// the commit and the render time.
func TestBuildStamp(t *testing.T) {
	s, tr, _ := serveTree(t)
	defer func() { debugOn = false }()
	for _, c := range []struct{ show, shared, want bool }{
		{false, false, false}, {true, false, true}, {true, true, false},
	} {
		debugOn, tr.shared = c.show, c.shared
		body := get(s.handler(), "/a/").Body.String()
		if got := strings.Contains(body, `<p class="built" data-built="`); got != c.want {
			t.Errorf("stamp=%v shared=%v: shown=%v", c.show, c.shared, got)
		}
		if c.want && !regexp.MustCompile(`s ago, from [^<]+, rendered in [0-9.]+ ms</p>`).MatchString(body) {
			t.Errorf("stamp lacks commit or render time: %s", body)
		}
	}
}
