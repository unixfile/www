package www

import (
	"os"
	"strings"
	"testing"
)

func sample(t *testing.T, lang string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/proselang/" + lang + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func detect(text string) string {
	var c langCount
	c.count([]byte(text))
	if l := c.lang(); l >= 0 {
		return proseLangs[l]
	}
	return ""
}

// Two sentences are enough to tell each language, its near neighbours
// included: Danish from Norwegian, Spanish from Portuguese.
func TestProseLangSamples(t *testing.T) {
	for _, lang := range proseLangs {
		if got := detect(sample(t, lang)); got != lang {
			t.Errorf("%s sample told as %q", lang, got)
		}
	}
}

// Text with too little to go on names no language.
func TestProseLangUnsure(t *testing.T) {
	for name, text := range map[string]string{
		"empty":            "",
		"two common words": "Det är",
		"names of several": "Supported locales: en, de, fr, es, it, pt, nl, sv, da",
		"few common words": "det är och " + strings.Repeat("zzz ", 20),
		"another language": "Serwer tworzy każdą stronę wtedy, gdy ktoś o nią poprosi, więc nie ma etapu budowania i nie ma czego publikować.",
		"identifiers":      "renderMarkdown markLang countProse langCount proseLangs wordLangs",
		"single letters":   "a i o y e à è å",
	} {
		if got := detect(text); got != "" {
			t.Errorf("%s: told as %q, want none", name, got)
		}
	}
}

// Words count whatever their case, and an apostrophe or hyphen ends one.
func TestProseLangWords(t *testing.T) {
	var c langCount
	c.count([]byte("DET ÄR Inte l'homme self-made x " + strings.Repeat("ö", 40)))
	if c.words != 7 {
		t.Errorf("%d words, want 7: det är inte homme self made and the long one, not l or x", c.words)
	}
	if got := c.hits[1]; got != 3 {
		t.Errorf("%d Swedish words, want 3", got)
	}
}

func markdownHTML(t *testing.T, src string) string {
	t.Helper()
	h, err := renderMarkdown([]byte(src), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// An English doc is written as before: the page already says English.
func TestLangEnglishDocUnmarked(t *testing.T) {
	body := markdownHTML(t, "# Title\n\n"+sample(t, "en")+"\n\n- one\n- two\n")
	if strings.Contains(body, "lang=") {
		t.Errorf("English doc carries a lang: %s", body)
	}
}

// In a Swedish doc every block says Swedish, a heading and a short line
// too, since they take the doc's language.
func TestLangSwedishDoc(t *testing.T) {
	body := markdownHTML(t, "# Rubrik\n\n"+sample(t, "sv")+"\n\nKort rad.\n\n- punkt\n\n> citat\n\n| a | b |\n|---|---|\n| c | d |\n")
	for _, want := range []string{`<h1 id="rubrik" lang="sv">`, `<p lang="sv">Servern`, `<p lang="sv">Kort rad.</p>`, `<ul lang="sv">`, `<blockquote lang="sv">`, `<table lang="sv">`} {
		if !strings.Contains(body, want) {
			t.Errorf("no %s in: %s", want, body)
		}
	}
}

// A block in another language than its doc says its own, and in a doc
// that is not English an English block says nothing, like the page.
func TestLangMixedDoc(t *testing.T) {
	body := markdownHTML(t, sample(t, "en")+"\n\n"+sample(t, "en")+"\n\n"+sample(t, "sv")+"\n\nShort.\n")
	if !strings.Contains(body, `<p lang="sv">Servern`) || strings.Count(body, "lang=") != 1 {
		t.Errorf("want only the Swedish paragraph marked: %s", body)
	}
	body = markdownHTML(t, sample(t, "sv")+"\n\n"+sample(t, "sv")+"\n\n"+sample(t, "en")+"\n\n"+sample(t, "de")+"\n")
	if !strings.Contains(body, "<p>The server") || !strings.Contains(body, `<p lang="de">Der Server`) || strings.Count(body, `lang="sv"`) != 2 {
		t.Errorf("want Swedish, Swedish, unmarked English, German: %s", body)
	}
}

// Code is not prose: Swedish in code spans and fences does not make an
// English doc Swedish, and a fence is never marked.
func TestLangSkipsCode(t *testing.T) {
	sv := sample(t, "sv")
	body := markdownHTML(t, "Run `"+sv+"` and `"+sv+"` now.\n\n```\n"+sv+"\n```\n\n    "+sv+"\n")
	if strings.Contains(body, "lang=") {
		t.Errorf("code made the doc Swedish: %s", body)
	}
	body = markdownHTML(t, sv+"\n\n```go\nx := 1\n```\n")
	if strings.Count(body, "lang=") != 1 {
		t.Errorf("want the paragraph marked and the fence not: %s", body)
	}
}

// A footnote's text is prose of the doc, and its paragraph is marked.
func TestLangFootnote(t *testing.T) {
	body := markdownHTML(t, sample(t, "sv")+"[^1]\n\n[^1]: "+sample(t, "sv")+"\n")
	if strings.Count(body, `<p lang="sv">`) != 2 {
		t.Errorf("want the paragraph and its footnote marked: %s", body)
	}
}

// Alerts keep their class beside the language.
func TestLangAlert(t *testing.T) {
	body := markdownHTML(t, "> [!NOTE]\n> "+sample(t, "sv")+"\n")
	if !strings.Contains(body, `class="alert note"`) || !strings.Contains(body, `lang="sv"`) {
		t.Errorf("alert lost its class or language: %s", body)
	}
}

func BenchmarkMarkLang(b *testing.B) {
	src, err := os.ReadFile("README.md")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("render", func(b *testing.B) {
		for b.Loop() {
			renderMarkdown(src, nil, nil)
		}
	})
	b.Run("count", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		for b.Loop() {
			var c langCount
			c.count(src)
		}
	})
}
