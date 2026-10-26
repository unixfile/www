package main

import (
	"strings"
	"testing"
)

// --list and --kill print keyval: indices share a width, so the lines sort
// in key order, and values escape what keyval cannot hold raw.
func TestWriteTrees(t *testing.T) {
	var b strings.Builder
	writeTrees(&b, nil)
	if b.String() != "" {
		t.Errorf("no trees: %q, want nothing", b.String())
	}

	trees := make([]servedTree, 11)
	for i := range trees {
		trees[i] = servedTree{dir: "/d", url: "http://u/"}
	}
	trees[3].dir = `/a\b`
	trees[4].dir = "/tab\there"
	trees[5].dir = "/ends in space "
	trees[7].binary = "/bin/sh"
	b.Reset()
	writeTrees(&b, trees)
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 23 {
		t.Fatalf("%d lines, want 23", len(lines))
	}
	for i := 1; i < len(lines); i++ {
		if lines[i-1] >= lines[i] {
			t.Errorf("line %d %q sorts after line %d %q", i-1, lines[i-1], i, lines[i])
		}
	}
	for _, want := range []string{
		"tree.00.dir /d",
		"tree.00.url http://u/",
		`tree.03.dir /a\\b`,
		`tree.04.dir /tab\there`,
		`tree.05.dir /ends in space \0`,
		"tree.07.binary /bin/sh",
		"tree.10.url http://u/",
	} {
		if !strings.Contains(b.String(), want+"\n") {
			t.Errorf("no line %q in\n%s", want, b.String())
		}
	}
}

func TestKvValue(t *testing.T) {
	for in, want := range map[string]string{
		"":        `\0`,
		"plain":   "plain",
		"a\nb":    `a\nb`,
		"a\rb":    `a\rb`,
		`a\b`:     `a\\b`,
		"  lead":  "  lead",
		"trail  ": `trail  \0`,
	} {
		if got := kvValue(in); got != want {
			t.Errorf("kvValue(%q) = %q, want %q", in, got, want)
		}
	}
}
