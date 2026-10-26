package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGron(t *testing.T) {
	in := `{"name":"Anna","n":-1.5e3,"ok":true,"x":null,"tags":["a",[1,{}]],"meta":{},
"createdAt":"2026","Content-Type":"x","class":1,"$v":2,"åke":"ö","escA":"v","1st":0}`
	want := `json = {};
json.name = "Anna";
json.n = -1.5e3;
json.ok = true;
json.x = null;
json.tags = [];
json.tags[0] = "a";
json.tags[1] = [];
json.tags[1][0] = 1;
json.tags[1][1] = {};
json.meta = {};
json.createdAt = "2026";
json["Content-Type"] = "x";
json["class"] = 1;
json.$v = 2;
json.åke = "ö";
json.escA = "v";
json["1st"] = 0;
`
	out, err := gron([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestGronRejectsBadJSON(t *testing.T) {
	for in, want := range map[string]string{
		"":                                  "line 1, column 1: no value",
		"{\n\"a\" 1}":                       "line 2, column 5: expected ':'",
		`[1 2]`:                             "line 1, column 4: expected ',' or ']'",
		`"a\x"`:                             "line 1, column 4: bad escape",
		`01`:                                "line 1, column 2: data after the value",
		`tru`:                               "line 1, column 1: bad literal",
		`{"a":1,}`:                          "line 1, column 8: expected a string key",
		strings.Repeat("[", gronMaxDepth+1): "nested deeper than",
	} {
		_, err := gron([]byte(in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.20q: error %v, want %q", in, err, want)
		}
	}
}

func TestGronView(t *testing.T) {
	s, _, dir := serveTree(t)
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"a":[1]}`), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"a":}`), 0o644)

	if body := get(s.handler(), "/a.json").Body.String(); !strings.Contains(body, `<a class="left" href="?gron">gron</a>`) {
		t.Errorf("a JSON page has no gron link: %s", body)
	}
	body := get(s.handler(), "/a.json?gron").Body.String()
	if !strings.Contains(body, `<span class="left">gron</span>`) || !strings.Contains(body, `<span id="L3">`) ||
		!strings.Contains(body, `<a href="?">flat</a>`) {
		t.Errorf("no gron view with three numbered lines: %s", body)
	}
	if body := get(s.handler(), "/bad.json?gron").Body.String(); !strings.Contains(body, "not valid JSON: line 1, column 6") ||
		!strings.Contains(body, `<span>flat</span>`) {
		t.Errorf("bad JSON does not say why and show flat: %s", body)
	}
	for _, p := range []string{"/a/b.go", "/a/b.go?gron"} {
		if body := get(s.handler(), p).Body.String(); strings.Contains(body, "gron") {
			t.Errorf("%s offers gron: %s", p, body)
		}
	}
}
