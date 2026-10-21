package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

// TestOwnLexersMatchChroma compares the program's own lexers with
// chroma's, byte by byte, by the class each byte gets on a page.
// WWW_LEXCHECK names the files and directories to compare, eg
// WWW_LEXCHECK=/usr/lib/python3.11; without it the test is skipped. It
// reports the share of bytes that agree per language and the first lines
// that differ.
func TestOwnLexersMatchChroma(t *testing.T) {
	roots := strings.Fields(os.Getenv("WWW_LEXCHECK"))
	if len(roots) == 0 {
		t.Skip("set WWW_LEXCHECK to files or directories to compare")
	}
	type tally struct{ files, bytes, wrong int }
	tallies := map[string]*tally{}
	var shown int
	for _, root := range roots {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			lexer := codeLexer(p)
			own := ownLexers[lexer]
			if own == nil {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil || len(b) > maxCode || !isText(b) {
				return nil
			}
			it, err := chroma.Coalesce(lexer).Tokenise(nil, string(b))
			code := ensureLF(string(b))
			if err != nil {
				return nil
			}
			mine := own(code)
			var text strings.Builder
			for _, tok := range mine {
				text.WriteString(tok.Value)
			}
			if text.String() != code {
				t.Errorf("%s: text not preserved", p)
				return nil
			}
			want, got := classesOf(it.Tokens(), len(code)), classesOf(mine, len(code))
			name := langName(lexer)
			tl := tallies[name]
			if tl == nil {
				tl = &tally{}
				tallies[name] = tl
			}
			tl.files++
			tl.bytes += len(code)
			lines := strings.SplitAfter(code, "\n")
			off := 0
			for n, line := range lines {
				bad := 0
				for k := off; k < off+len(line); k++ {
					if want[k] != got[k] && code[k] != '\n' {
						bad++
					}
				}
				tl.wrong += bad
				if bad > 0 && shown < 40 {
					shown++
					t.Logf("%s:%d\n  %s\n  want %s\n  got  %s", p, n+1, strings.TrimRight(line, "\n"),
						classLine(want[off:off+len(line)]), classLine(got[off:off+len(line)]))
				}
				off += len(line)
			}
			return nil
		})
	}
	names := make([]string, 0, len(tallies))
	for n := range tallies {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		tl := tallies[n]
		t.Logf("%s: %d files, %d bytes, %d differ (%.4f%%)", n, tl.files, tl.bytes, tl.wrong,
			100*float64(tl.wrong)/float64(max(tl.bytes, 1)))
	}
}

// classesOf gives each byte of the source the class its token gets.
func classesOf(toks []chroma.Token, n int) []string {
	out := make([]string, 0, n)
	for _, tok := range toks {
		c := pandocClass(tok.Type)
		for range len(tok.Value) {
			out = append(out, c)
		}
	}
	for len(out) < n {
		out = append(out, "")
	}
	return out[:n]
}

// classLine shows a line's classes, one letter a byte, . for none.
func classLine(cs []string) string {
	var b strings.Builder
	for _, c := range cs {
		l, ok := classLetters[c]
		if !ok {
			l = "?"
		}
		b.WriteString(l)
	}
	return b.String()
}

var classLetters = map[string]string{"": ".", "kw": "k", "cn": "C", "op": "o", "co": "c", "pp": "p", "st": "s",
	"sc": "e", "ch": "h", "do": "d", "dv": "n", "fl": "f", "bn": "b", "bu": "B", "fu": "F", "va": "v", "dt": "t",
	"at": "a", "er": "E"}

// The own lexers give every byte of these samples the class chroma gives
// it; TestOwnLexersMatchChroma checks whole trees when asked.
func TestOwnLexersSamples(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"a.py", "#!/usr/bin/env python3\nfrom . import x as y, z\nimport os.path\n\n@dataclass\nclass A(B):\n" +
			"    \"\"\"Doc.\"\"\"\n    def __init__(self, n=0x1F, f=1_0.5e3j):\n        yield from range(n)\n" +
			"        return f\"a{n!r:>3}b{{c}}\" % self.match + rb'\\d' + '\\x41\\q'  # c\n"},
		{"a.js", "#!/usr/bin/env node\nconst re = /a[/]b/g, n = 0x1F + 1.5e3;\nlet s = `t${a + `n${b}`}u` + \"q\\\"\" + 'x';\n" +
			"if (a) { y = a / 2 } // c\n/* m\n */ function f$_x(a) { return typeof a === 'undefined' ? NaN : this }\n"},
		{"a.sh", "#!/bin/sh\nfor f in *.md; do echo \"$f ${x:-y} $(ls -l) $((1 + 2#10))\"; done\nX+=1 cat <<EOF\nhi $USER\nEOF\n" +
			"if [ -n \"$1\" ]; then exit 2; fi # c\nlocal v=$'a\\n' w='b' `pwd`\n"},
	} {
		l := codeLexer(c.name)
		it, err := chroma.Coalesce(l).Tokenise(nil, c.src)
		if err != nil {
			t.Fatal(err)
		}
		want, got := classLine(classesOf(it.Tokens(), len(c.src))), classLine(classesOf(ownLexers[l](c.src), len(c.src)))
		if want != got {
			t.Errorf("%s:\nwant %s\ngot  %s", c.name, want, got)
		}
	}
}
