package main

import (
	"os/exec"
	"strings"
	"testing"
)

// A fence in a known language gets pandoc's markup without line numbers;
// any other fence is plain.
func TestFences(t *testing.T) {
	h, err := renderMarkdown([]byte("```go\nx := 1\n```\n\n```nosuch\n<b>\n```\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	body := string(h)
	if !strings.Contains(body, `<div class="sourceCode"><pre class="sourceCode go"><code class="sourceCode go"><span>x <span class="op">:=</span> <span class="dv">1</span></span></code></pre></div>`) {
		t.Errorf("go fence not highlighted as pandoc does: %s", body)
	}
	if !strings.Contains(body, "<pre><code>&lt;b&gt;\n</code></pre>") {
		t.Errorf("unknown fence not plain: %s", body)
	}
}

// Every line of a code page is numbered, a blank last line included, and
// file content cannot forge a line id.
func TestCodeLines(t *testing.T) {
	var b strings.Builder
	highlight(&b, codeLexer("x.txt"), "a\n\"<span id=\"L9\">\n\n", true)
	out := b.String()
	if n := strings.Count(out, `<span id="L`); n != 3 {
		t.Errorf("%d numbered lines, want 3: %s", n, out)
	}
	if !strings.Contains(out, `<span id="L2"><a href="#L2"></a>&#34;&lt;span id=&#34;L9&#34;&gt;</span>`) {
		t.Errorf("content not escaped: %s", out)
	}
}

func TestCodeLexerByName(t *testing.T) {
	for name, want := range map[string]string{"main.go": "go", "Makefile": "make", "Dockerfile": "docker", "notes.unknownext": ""} {
		if got := langName(codeLexer(name)); got != want {
			t.Errorf("%s: language %q, want %q", name, got, want)
		}
	}
}

// Task list boxes are not disabled, so they keep the browser's colour.
func TestTaskBoxes(t *testing.T) {
	h, err := renderMarkdown([]byte("- [x] done\n- [ ] open\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if body := string(h); !strings.Contains(body, `<li><input type="checkbox" checked=""> done</li>`) || !strings.Contains(body, `<li><input type="checkbox"> open</li>`) || strings.Contains(body, "disabled") {
		t.Errorf("task boxes not rendered as pandoc's: %s", body)
	}
}

// Go is lexed by the standard library's scanner: every byte comes back,
// and tokens get the classes chroma's Go lexer gives them.
func TestGoTokens(t *testing.T) {
	src := "package a\r\n\n//go:embed x\n/* block\n */\nfunc F(s string) int { return len(s) + 0x1F + 0o7 }\nvar r = `raw\nstring` // c\n"
	var text strings.Builder
	for _, tok := range goTokens(src) {
		text.WriteString(tok.Value)
	}
	if text.String() != src {
		t.Fatalf("text not preserved:\n%q\n%q", text.String(), src)
	}
	var b strings.Builder
	highlight(&b, codeLexer("a.go"), src, false)
	for _, want := range []string{`<span class="pp">//go:embed x</span>`, `<span class="kw">func</span> <span class="fu">F</span>`,
		`<span class="dt">string</span>`, `<span class="bu">len</span>`, `<span class="bn">0x1F</span>`, `<span class="bn">0o7</span>`,
		`<span class="st">` + "`raw</span>", `<span class="co">// c</span>`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("no %s in %s", want, b.String())
		}
	}
}

// A relative link to a missing path is marked; others are left alone.
func TestBrokenLinks(t *testing.T) {
	missing := func(dest string) bool { return dest == "gone.md" || dest == "a b.md" }
	h, err := renderMarkdown([]byte("[x](gone.md#top) [y](here.md) [z](https://example.com/gone.md) [w](a%20b.md) [v](#gone.md) [u](/gone.md)\n"), missing)
	if err != nil {
		t.Fatal(err)
	}
	s := string(h)
	if n := strings.Count(s, `class="broken"`); n != 2 {
		t.Errorf("%d broken links, want 2: %s", n, s)
	}
	if !strings.Contains(s, `<a href="gone.md#top" class="broken" title="missing: gone.md">`) {
		t.Errorf("gone.md not marked: %s", s)
	}
}

// A dot fence becomes inline SVG when dot is installed, and code with the
// reason otherwise.
func TestGraphFence(t *testing.T) {
	h, err := renderMarkdown([]byte("```dot\ndigraph { a -> b }\n```\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := string(h)
	if _, err := exec.LookPath("dot"); err != nil {
		if !strings.Contains(s, "dot is not installed") {
			t.Errorf("no reason given: %s", s)
		}
		return
	}
	if !strings.Contains(s, `<div class="graph"><svg`) || strings.Contains(s, "<?xml") {
		t.Errorf("not inline SVG: %.200s", s)
	}
	h, _ = renderMarkdown([]byte("```dot\ndigraph { a -> }\n```\n"), nil)
	if s := string(h); !strings.Contains(s, `class="own warn"`) || !strings.Contains(s, "<pre") {
		t.Errorf("bad graph not shown as code with a reason: %s", s)
	}
}
