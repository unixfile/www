package main

import (
	"io"
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
// different lines, and in Python the lines indented under a line that
// ends with a colon. Bracket pairs within a line, with something between
// them, are leaves. Brackets count only in punctuation and operator
// tokens, so a brace inside a string or comment opens nothing. Blocks
// come first, outer before inner, then leaves by row, outer before inner.
func findScopes(lines [][]chroma.Token, python bool) []scope {
	indent, width := lineCells(lines)
	type open struct {
		c        byte
		row, col int
	}
	var stack []open
	var found, leaves []scope
	for row, line := range lines {
		col := 0
		for _, tok := range line {
			cat := tok.Type.Category()
			bracketed := cat == chroma.Punctuation || cat == chroma.Operator
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
							level := 1
							for _, e := range stack[:k] {
								if e.row == row {
									level++
								}
							}
							leaves = append(leaves, scope{first: row, last: row, left: o.col, right: col + 1, leaf: level})
						}
						stack = stack[:k]
						break
					}
				}
				col++
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
// stylesheet turns the cells and rows into lengths.
func writeScopes(w io.Writer, scopes []scope) {
	for _, s := range scopes {
		if s.leaf > 0 {
			io.WriteString(w, `<i class="scope leaf" style="--c:`+strconv.Itoa(s.left)+`;--w:`+strconv.Itoa(s.right-s.left)+
				`;--k:`+strconv.Itoa(s.leaf)+`;--d:`+strconv.Itoa(s.depth)+`"></i>`)
			continue
		}
		io.WriteString(w, `<i class="scope" style="--c:`+strconv.Itoa(s.left)+`;--w:`+strconv.Itoa(s.right-s.left)+
			`;--r:`+strconv.Itoa(s.last-s.first+1)+`;--d:`+strconv.Itoa(s.depth)+`"></i>`)
	}
}
