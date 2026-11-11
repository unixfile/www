package www

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"path"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/unixfile/kv"
)

// A docFormat is one spelling of a structured document. A file in one
// format has a view in each of the others, converted through kv's tree.
type docFormat struct {
	name  string // the view's label and its query, eg ?yaml
	title string // the format's name in a sentence
	lexer chroma.Lexer
	parse func([]byte) (*kv.File, error)
	write func(*kv.File) ([]byte, error)
}

// docFormats lists the formats in the order the view row shows them.
// JSON and YAML names are wider than keyval's, so they parse under
// kv.JSONKeys, which escapes a dot in a name.
var docFormats = []*docFormat{
	{"json", "JSON", codeLexer("a.json"), kv.JSONKeys.FromJSON, kv.ToJSON},
	{"yaml", "YAML", codeLexer("a.yaml"), kv.JSONKeys.FromYAML, kv.ToYAML},
	{"keyval", "keyval", lexers.Fallback,
		func(b []byte) (*kv.File, error) { return kv.ParseLenient(string(b)) },
		func(f *kv.File) ([]byte, error) { return []byte(f.String()), nil }},
}

// formatOf is the format a file's name gives it, or nil.
func formatOf(name string) *docFormat {
	switch strings.ToLower(path.Ext(name)) {
	case ".json":
		return docFormats[0]
	case ".yaml", ".yml":
		return docFormats[1]
	case ".kv":
		return docFormats[2]
	}
	return nil
}

// formatMax bounds the file a format view converts. A conversion builds
// the whole tree in memory and takes about a second per MB with its
// colouring, so a larger file would hold a reader and a processor too long.
const formatMax = 1 << 20

var errFormatSize = errors.New("the file is over 1 MB")

// formatPage is a file's converted view as coloured, numbered HTML. The
// server keeps it by the file's hash, so only a first view converts.
func formatPage(b []byte, from, to *docFormat) (string, error) {
	sum := sha256.Sum256(b)
	key := "format\x00" + from.name + "\x00" + to.name + "\x00" + string(sum[:])
	if h, ok := codeCache.get(key); ok {
		return h.(string), nil
	}
	out, err := convert(b, from, to)
	if err != nil {
		return "", err
	}
	var hl strings.Builder
	highlight(&hl, to.lexer, string(out), true, false)
	h := hl.String()
	codeCache.put(key, h, int64(len(h)))
	return h, nil
}

// convert writes a document of one format in another.
func convert(b []byte, from, to *docFormat) (out []byte, err error) {
	if len(b) > formatMax {
		return nil, errFormatSize
	}
	// kv is another module, and the file is whatever a tree holds: a
	// panic in a conversion costs the view, not the request.
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("the converter failed: %v", r)
		}
	}()
	f, err := from.parse(b)
	if err != nil {
		return nil, err
	}
	return to.write(f)
}

// formatView finds the format a query asks for: one a file of format
// from can show and is not already in.
func formatView(from *docFormat, q map[string][]string) *docFormat {
	if from == nil {
		return nil
	}
	for _, f := range docFormats {
		if _, ok := q[f.name]; ok && f != from {
			return f
		}
	}
	return nil
}

// formatNote leads a converted view and says what the conversion changed,
// since the page no longer shows the file as written.
func formatNote(name string, from, to *docFormat) string {
	lost := "keys are sorted, and an empty list is no longer told from an empty map"
	if from.name == "yaml" {
		lost = "comments are dropped, aliases are written out, " + lost
	}
	return "<p class=\"own\">" + template.HTMLEscapeString(path.Base(name)) + " as " + to.title +
		", converted from " + from.title + ": " + lost + ".</p>\n"
}

// formatFail says why a format view shows the file as written instead.
func formatFail(from, to *docFormat, err error) string {
	return "<p class=\"own warn\">No " + to.title + " view, since this " + from.title +
		" does not convert: " + template.HTMLEscapeString(err.Error()) + "</p>\n"
}
