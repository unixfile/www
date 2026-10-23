package main

import (
	"bytes"
	"net/url"
	"path"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// wikiLinkParser reads [[name]], [[name|label]] and [[name#heading]] as
// links, resolved as relative links are: name.md in the doc's directory,
// or the name as written when it has an extension. It runs before
// goldmark's link parser, which also starts at '[', and leaves anything
// else to it, so the links it makes are marked broken and counted as
// backlinks like any other.
type wikiLinkParser struct{}

func (wikiLinkParser) Trigger() []byte { return []byte{'['} }

func (wikiLinkParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) < 5 || line[1] != '[' {
		return nil
	}
	end := bytes.Index(line[2:], []byte("]]"))
	if end <= 0 {
		return nil
	}
	inner := line[2 : 2+end]
	// [[x]](y) is a link whose text is [x].
	if bytes.ContainsAny(inner, "[]\n") || bytes.HasPrefix(line[2+end+2:], []byte("(")) {
		return nil
	}
	target, label, labelled := bytes.Cut(inner, []byte("|"))
	name, heading, _ := bytes.Cut(target, []byte("#"))
	name, heading = bytes.TrimSpace(name), bytes.TrimSpace(heading)
	if len(name) == 0 && len(heading) == 0 {
		return nil
	}
	var dest string
	if len(name) > 0 {
		n := string(name)
		if path.Ext(n) == "" {
			n += ".md"
		}
		dest = (&url.URL{Path: n}).String()
	}
	if len(heading) > 0 {
		dest += "#" + headingID(heading)
	}
	link := ast.NewLink()
	link.Destination = []byte(dest)
	start, stop := seg.Start+2, seg.Start+2+len(target)
	if labelled {
		start, stop = stop+1, stop+1+len(label)
	}
	link.AppendChild(link, ast.NewTextSegment(text.NewSegment(start, stop)))
	block.Advance(2 + end + 2)
	return link
}

// headingID is the id goldmark gives a heading with this text, before
// any suffix that tells repeated headings apart.
func headingID(s []byte) string {
	var out []byte
	for i := 0; i < len(s); {
		c := s[i]
		n := util.UTF8Len(c)
		i += int(n)
		switch {
		case n != 1:
		case util.IsAlphaNumeric(c):
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			out = append(out, c)
		case util.IsSpace(c) || c == '-' || c == '_':
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "heading"
	}
	return string(out)
}
