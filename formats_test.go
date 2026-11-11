package www

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConvert(t *testing.T) {
	json, yaml, keyval := docFormats[0], docFormats[1], docFormats[2]
	const kvDoc = "# github.com/unixfile/keyval\nname Anna\nport 8080\ntags.0 a\ntags.1 b\n"
	const jsonDoc = "{\n  \"name\": \"Anna\",\n  \"port\": 8080,\n  \"tags\": [\n    \"a\",\n    \"b\"\n  ]\n}\n"
	const yamlDoc = "name: Anna\nport: 8080\ntags:\n  - a\n  - b\n"
	for _, c := range []struct {
		from, to *docFormat
		in, want string
	}{
		{json, yaml, `{"tags":["a","b"],"port":8080,"name":"Anna"}`, yamlDoc},
		{json, keyval, jsonDoc, kvDoc},
		{yaml, json, "# who\nport: 8080\nname: Anna\ntags: [a, b]\n", jsonDoc},
		{yaml, keyval, yamlDoc, kvDoc},
		{keyval, json, kvDoc, jsonDoc},
		{keyval, yaml, kvDoc, yamlDoc},
		// A keyval file whose lines are out of order still reads.
		{keyval, yaml, "port 8080\nname Anna\n", "name: Anna\nport: 8080\n"},
		// A string that looks like another type stays a string.
		{json, yaml, `{"a":"true","b":"8080","c":true}`, "a: \"true\"\nb: \"8080\"\nc: true\n"},
		{yaml, json, "a: 'true'\nb: 0x10\n", "{\n  \"a\": \"true\",\n  \"b\": 16\n}\n"},
		// A dot in a name is escaped in keyval and whole elsewhere.
		{json, keyval, `{"a.b":{"c":1}}`, "# github.com/unixfile/keyval\na\\.b.c 1\n"},
		{json, yaml, `{"a.b":{"c":1}}`, "a.b:\n  c: 1\n"},
		// Aliases are written out; an empty list becomes an empty map.
		{yaml, json, "a: &x {n: 1}\nb: *x\nc: []\n", "{\n  \"a\": {\n    \"n\": 1\n  },\n  \"b\": {\n    \"n\": 1\n  },\n  \"c\": {}\n}\n"},
		// A stream of documents is a list.
		{yaml, json, "a: 1\n---\na: 2\n", "[\n  {\n    \"a\": 1\n  },\n  {\n    \"a\": 2\n  }\n]\n"},
	} {
		out, err := convert([]byte(c.in), c.from, c.to)
		if err != nil {
			t.Errorf("%s to %s of %q: %v", c.from.name, c.to.name, c.in, err)
		} else if string(out) != c.want {
			t.Errorf("%s to %s of %q:\ngot\n%s\nwant\n%s", c.from.name, c.to.name, c.in, out, c.want)
		}
	}
}

func TestConvertErrors(t *testing.T) {
	json, yaml, keyval := docFormats[0], docFormats[1], docFormats[2]
	for _, c := range []struct {
		from     *docFormat
		in, want string
	}{
		{json, `{"a":}`, "invalid character"},
		{json, ``, "EOF"},
		{json, `3`, "must be an object or array"},
		{json, `{"a b":1}`, `"a b"`},
		{yaml, "a: [\n", "line 1"},
		{yaml, "just text\n", "must be a mapping or sequence"},
		{yaml, "? [1]\n: 2\n", "must be a scalar"},
		{keyval, "a\n  b\n", "line 2"},
		{json, `{"a":"` + strings.Repeat("x", formatMax) + `"}`, "over 1 MB"},
	} {
		to := yaml
		if c.from == yaml {
			to = json
		}
		_, err := convert([]byte(c.in), c.from, to)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %.20q: error %v, want %q", c.from.name, c.in, err, c.want)
		}
	}
}

// A YAML file whose aliases multiply, nine levels of nine, must fail
// instead of growing into gigabytes.
func TestConvertAliasBomb(t *testing.T) {
	var b strings.Builder
	b.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x]\n")
	for i, c := 1, "abcdefghi"; i < 9; i++ {
		prev := "*" + string(c[0]) + string(rune('0'+i-1))
		b.WriteString("a" + string(rune('0'+i)) + ": &a" + string(rune('0'+i)) + " [" + strings.TrimSuffix(strings.Repeat(prev+", ", 9), ", ") + "]\n")
	}
	start := time.Now()
	_, err := convert([]byte(b.String()), docFormats[1], docFormats[0])
	if err == nil {
		t.Error("an alias bomb converted")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("an alias bomb took %v to refuse", d)
	}
}

func TestFormatViews(t *testing.T) {
	s, _, dir := serveTree(t)
	write := func(name, content string) {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	write("a.json", `{"b":[1],"a":"x"}`)
	write("a.yaml", "b: 1 # why\n")
	write("A.YML", "b: 1\n")
	write("a.kv", "b 1\n")
	write("bad.json", `{"a":}`)
	body := func(p string) string { return get(s.handler(), p).Body.String() }

	// A file's own format is plain text; the others are links.
	for p, want := range map[string][]string{
		"/a.json": {`<a class="left" href="?gron">gron</a>`, `<span class="left">json</span>`, `<a class="left" href="?yaml">yaml</a>`, `<a class="left" href="?keyval">keyval</a>`, `<span>flat</span>`},
		"/a.yaml": {`<a class="left" href="?json">json</a>`, `<span class="left">yaml</span>`, `<a class="left" href="?keyval">keyval</a>`},
		"/A.YML":  {`<a class="left" href="?json">json</a>`, `<span class="left">yaml</span>`},
		"/a.kv":   {`<a class="left" href="?json">json</a>`, `<a class="left" href="?yaml">yaml</a>`, `<span class="left">keyval</span>`},
		// A query for the file's own format shows the file.
		"/a.json?json":   {`<span class="left">json</span>`, `<span>flat</span>`, `<span id="L1">`},
		"/a.json?scopes": {`<span class="left">json</span>`, `<span>scopes</span>`},
		// Gron is not the file as written, so json leads back to it.
		"/a.json?gron": {`<span class="left">gron</span>`, `<a class="left" href="?">json</a>`},
	} {
		b := body(p)
		for _, w := range want {
			if !strings.Contains(b, w) {
				t.Errorf("%s lacks %s: %s", p, w, b)
			}
		}
	}

	// A converted view shows the other format, numbered, under a note,
	// with every other view a link.
	b := body("/a.json?yaml")
	for _, w := range []string{"a.json as YAML, converted from JSON: keys are sorted", `<span class="left">yaml</span>`,
		`<a class="left" href="?">json</a>`, `<a href="?">flat</a>`, `<a href="?raw">raw</a>`, `<span id="L3">`} {
		if !strings.Contains(b, w) {
			t.Errorf("/a.json?yaml lacks %s: %s", w, b)
		}
	}
	if strings.Contains(b, `<span id="L4">`) || strings.Contains(b, "comments") {
		t.Errorf("/a.json?yaml has a fourth line or speaks of comments: %s", b)
	}
	if b := body("/a.yaml?keyval"); !strings.Contains(b, "a.yaml as keyval, converted from YAML: comments are dropped") ||
		!strings.Contains(b, "b 1") || strings.Contains(b, "why") {
		t.Errorf("/a.yaml?keyval: %s", b)
	}
	if b := body("/a.kv?json"); !strings.Contains(b, `<span class="left">json</span>`) || !strings.Contains(b, `<span id="L3">`) {
		t.Errorf("/a.kv?json: %s", b)
	}

	// A file that does not convert says why above the file as written.
	if b := body("/bad.json?yaml"); !strings.Contains(b, "No YAML view, since this JSON does not convert: ") ||
		!strings.Contains(b, `<span>flat</span>`) || !strings.Contains(b, `<span class="left">json</span>`) {
		t.Errorf("/bad.json?yaml: %s", b)
	}

	// Other files have no formats.
	for _, p := range []string{"/a/b.go", "/a/b.go?yaml"} {
		if b := body(p); strings.Contains(b, "yaml") || strings.Contains(b, "keyval") {
			t.Errorf("%s offers formats: %s", p, b)
		}
	}
}
