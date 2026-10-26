package main

import (
	"slices"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
)

// A scope is a block of code that spans lines, drawn as a sunken box
// behind its text. Monospace text is a grid, so a box is a rectangle of
// cells: from its first row to its last, from the first row's indent to
// past its widest line. A leaf is a bracket pair within one line, eg
// (x, y), whose box spans its brackets' cells. Boxes nest without limit.
type scope struct {
	first, last int // rows, counted from 0
	left, right int // cells; right is one past the widest line or the closing bracket
	depth       int // 1 for a scope in no other
	leaf        int // for a leaf, 1 plus the leaves around it on its line; 0 for a block
}

// tabStop is CSS's default tab-size, which code pages keep.
const tabStop = 8

// lineCells measures each line in cells: where its text starts and where
// it ends. A blank line's indent is -1.
func lineCells(lines [][]chroma.Token) (indent, width []int) {
	indent = make([]int, len(lines))
	width = make([]int, len(lines))
	for i, line := range lines {
		col, ind := 0, -1
		for _, tok := range line {
			for j := 0; j < len(tok.Value); j++ {
				switch c := tok.Value[j]; {
				case c == '\n':
				case c == '\t':
					col = (col/tabStop + 1) * tabStop
				case c == ' ':
					col++
				case c&0xC0 == 0x80: // a UTF-8 continuation byte
				default:
					if ind < 0 {
						ind = col
					}
					col++
				}
			}
		}
		indent[i] = ind
		width[i] = col
	}
	return indent, width
}

// findScopes finds the blocks of code: bracket pairs whose ends lie on
// different lines, strings that span lines, and in Python the lines
// indented under a line that ends with a colon. Bracket pairs and strings
// within a line, with something inside them, are leaves, the units vim's
// ci( and ci" act on. Brackets count only in punctuation and operator
// tokens, so a brace inside a string or comment opens nothing; a string
// is a run of string tokens, its quotes and escapes included. Blocks come
// first, outer before inner, then leaves by row, outer before inner.
func findScopes(lines [][]chroma.Token, python bool) []scope {
	indent, width := lineCells(lines)
	type open struct {
		c        byte
		row, col int
	}
	var stack []open
	var found, leaves []scope
	// level counts the brackets open on row, plus one: a leaf's level
	// among the leaves around it.
	level := func(open []open, row int) int {
		n := 1
		for _, e := range open {
			if e.row == row {
				n++
			}
		}
		return n
	}
	// str is the string being read: where it starts and where it has got
	// to, row and cell.
	var str struct {
		on                 bool
		row, col, end, top int
	}
	endString := func() {
		if !str.on {
			return
		}
		str.on = false
		switch {
		case str.end > str.row:
			found = append(found, scope{first: str.row, last: str.end})
		case str.top > str.col+2:
			leaves = append(leaves, scope{first: str.row, last: str.row, left: str.col, right: str.top, leaf: level(stack, str.row)})
		}
	}
	for row, line := range lines {
		col := 0
		for _, tok := range line {
			cat := tok.Type.Category()
			bracketed := cat == chroma.Punctuation || cat == chroma.Operator
			if tok.Type.SubCategory() == chroma.LiteralString {
				if !str.on {
					str.on, str.row, str.col = true, row, col
				}
			} else {
				endString()
			}
			for j := 0; j < len(tok.Value); j++ {
				c := tok.Value[j]
				switch {
				case c == '\n', c&0xC0 == 0x80: // a UTF-8 continuation byte
					continue
				case c == '\t':
					col = (col/tabStop + 1) * tabStop
					continue
				case !bracketed:
				case c == '(' || c == '[' || c == '{':
					stack = append(stack, open{c, row, col})
				case c == ')' || c == ']' || c == '}':
					// A stray closer ends the brackets it matches, if any.
					for k := len(stack) - 1; k >= 0; k-- {
						o := stack[k]
						if o.c != opener(c) {
							continue
						}
						if o.row < row {
							found = append(found, scope{first: o.row, last: row})
						} else if col > o.col+1 {
							leaves = append(leaves, scope{first: row, last: row, left: o.col, right: col + 1, leaf: level(stack[:k], row)})
						}
						stack = stack[:k]
						break
					}
				}
				col++
			}
			if str.on {
				str.end, str.top = row, col
			}
		}
		if python && len(stack) == 0 && endsWithColon(line) {
			last := row
			for r := row + 1; r < len(lines); r++ {
				if indent[r] < 0 {
					continue
				}
				if indent[r] <= indent[row] {
					break
				}
				last = r
			}
			if last > row {
				found = append(found, scope{first: row, last: last})
			}
		}
	}
	endString()

	// Outer scopes first; two brackets that open and close on the same
	// rows, as in foo(func() {, draw one box.
	slices.SortFunc(found, func(a, b scope) int {
		if a.first != b.first {
			return a.first - b.first
		}
		return b.last - a.last
	})
	found = slices.CompactFunc(found, func(a, b scope) bool { return a.first == b.first && a.last == b.last })

	parent := make([]int, len(found))
	var in []int
	for i := range found {
		s := &found[i]
		for len(in) > 0 && found[in[len(in)-1]].last < s.last {
			in = in[:len(in)-1]
		}
		parent[i] = -1
		s.left = max(indent[s.first], 0)
		if len(in) > 0 {
			parent[i] = in[len(in)-1]
			s.left = max(s.left, found[parent[i]].left)
		}
		s.depth = len(in) + 1
		for r := s.first; r <= s.last; r++ {
			s.right = max(s.right, width[r])
		}
		in = append(in, i)
	}
	// A box reaches one cell past each box inside it.
	for i := len(found) - 1; i >= 0; i-- {
		if p := parent[i]; p >= 0 {
			found[p].right = max(found[p].right, found[i].right+1)
		}
	}

	// A leaf lies one level below the blocks around its row and the
	// leaves around it.
	blocks := make([]int, len(lines)+1)
	for _, b := range found {
		blocks[b.first]++
		blocks[b.last+1]--
	}
	for r := 1; r < len(blocks); r++ {
		blocks[r] += blocks[r-1]
	}
	for i := range leaves {
		leaves[i].depth = blocks[leaves[i].first] + leaves[i].leaf
	}
	slices.SortFunc(leaves, func(a, b scope) int {
		if a.first != b.first {
			return a.first - b.first
		}
		if a.left != b.left {
			return a.left - b.left
		}
		return b.right - a.right
	})

	// Merge by row, a row's blocks before its leaves.
	all := make([]scope, 0, len(found)+len(leaves))
	for len(found) > 0 || len(leaves) > 0 {
		if len(leaves) == 0 || len(found) > 0 && found[0].first <= leaves[0].first {
			all, found = append(all, found[0]), found[1:]
		} else {
			all, leaves = append(all, leaves[0]), leaves[1:]
		}
	}
	return all
}

// opener is the bracket that a closing bracket closes.
func opener(c byte) byte {
	switch c {
	case ')':
		return '('
	case ']':
		return '['
	}
	return '{'
}

// endsWithColon reports whether a line's last token, comments and space
// aside, is a colon.
func endsWithColon(line []chroma.Token) bool {
	for i := len(line) - 1; i >= 0; i-- {
		t := line[i]
		v := strings.TrimRight(t.Value, " \t\r\n")
		if v == "" || t.Type.Category() == chroma.Comment {
			continue
		}
		return t.Type.Category() == chroma.Punctuation && strings.HasSuffix(v, ":")
	}
	return false
}

// writeScopes writes the boxes that start on one row. Each is empty and
// positioned on its own, so it adds no text and moves none; the
// stylesheet turns the cells and rows into lengths. Boxes at odd depths
// are shaded, so shade and page alternate inward.
func writeScopes(b *strings.Builder, scopes []scope) {
	var num [20]byte
	cell := func(name string, n int) {
		b.WriteString(name)
		b.Write(strconv.AppendInt(num[:0], int64(n), 10))
	}
	for _, s := range scopes {
		b.WriteString(`<i class="scope`)
		if s.leaf > 0 {
			b.WriteString(" leaf")
		}
		if s.depth%2 == 1 {
			b.WriteString(" shade")
		}
		cell(`" style="--c:`, s.left)
		cell(`;--w:`, s.right-s.left)
		if s.leaf == 0 {
			cell(`;--r:`, s.last-s.first+1)
		}
		cell(`;--d:`, s.depth)
		b.WriteString(`"></i>`)
	}
}
