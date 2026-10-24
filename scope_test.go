package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

func scopesOf(name, code string) []scope {
	return findScopes(chroma.SplitTokensIntoLines(tokenise(codeLexer(name), code)), langName(codeLexer(name)) == "python")
}

// A block that spans lines is a scope; brackets on one line, or inside a
// string or comment, are not. A box reaches past each box inside it, and
// a call whose argument is a function draws one box.
func TestScopesGo(t *testing.T) {
	src := "func f() {\n" + // 0
		"\tif x { y() }\n" + // 1
		"\tfor {\n" + // 2
		"\t\ts := \"{\" // }\n" + // 3
		"\t\tgo(func() {\n" + // 4
		"\t\t}())\n" + // 5
		"\t}\n" + // 6
		"}\n" // 7
	got := scopesOf("a.go", src)
	want := []scope{
		{first: 0, last: 7, left: 0, right: 30, depth: 1},
		{first: 2, last: 6, left: 8, right: 29, depth: 2},
		{first: 4, last: 5, left: 16, right: 27, depth: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scopes\n%+v, want\n%+v", got, want)
	}
}

// In Python a line ending with a colon opens the lines indented under it,
// blank lines included, up to the last of them.
func TestScopesPython(t *testing.T) {
	src := "class A:\n" + // 0
		"    def f(self):\n" + // 1
		"        x = {'a': 1}\n" + // 2
		"\n" + // 3
		"        return x\n" + // 4
		"y = 1\n" // 5
	got := scopesOf("a.py", src)
	if len(got) != 2 || got[0].first != 0 || got[0].last != 4 || got[1].first != 1 || got[1].last != 4 || got[1].left != 4 {
		t.Errorf("scopes %+v, want class 0-4 and def 1-4 at 4", got)
	}
}

// A code page draws each scope as an empty box in its first line, after
// the line number; ?flat and fences draw none.
func TestScopeBoxes(t *testing.T) {
	src := "func f() {\n\treturn\n}\n"
	var b strings.Builder
	highlight(&b, codeLexer("a.go"), src, true, true)
	out := b.String()
	if !strings.Contains(out, `<pre class="sourceCode numberSource go numberLines scopes">`) ||
		!strings.Contains(out, `<span id="L1"><a href="#L1"></a><i class="scope" style="--c:0;--w:14;--r:3;--d:1"></i><span class="kw">func</span>`) {
		t.Errorf("no box in the first line: %s", out)
	}
	b.Reset()
	highlight(&b, codeLexer("a.go"), src, true, false)
	if strings.Contains(b.String(), "scope") {
		t.Errorf("flat page has scopes: %s", b.String())
	}
}
