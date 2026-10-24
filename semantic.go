package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
)

// A commit page shows its plain diff at once and its semantic diff after:
// the declarations the commit added, removed, changed, renamed, moved or
// only reformatted, from the syntax helper, and within each changed run of
// lines the tokens that changed, from the lexers. A commit never changes,
// so the semantic diff is kept under the commit's full hash, and a later
// view renders it inline.

// decl is one declaration in one version of a file.
type decl struct {
	name   string // qualified, eg tree.serve or Parser.parse
	bare   string // its last part, eg serve
	sr, er int    // rows from 0, leading comments included
	raw    [32]byte
	sum    [32]byte // the tokens, whitespace dropped
	shape  [32]byte // the tokens with the bare name blanked, to find renames
}

// declChange is what a commit did to one declaration.
type declChange struct {
	what []string // eg added, or moved and changed
	name string
	from string // the old name of a rename, or the file a move came from
	file string // the file it lies in, after the commit or, if removed, before
	sr   int    // rows from 0 in that version, for the link
	er   int
	gone bool // removed: the rows are in the parent
}

// semantic is a commit's semantic diff, as the cache keeps it.
type semantic struct {
	changes []declChange
	marks   map[string]map[int][][2]int // by file, by diff line: changed byte ranges
	note    string                      // why declarations are missing, or ""
}

// maxDecls bounds the declarations compared by order in one file.
const maxDecls = 2000

// maxTokenGrid bounds the cells of one token comparison, so a large run
// of changed lines gets no marks rather than a slow page.
const maxTokenGrid = 1 << 20

// declKind reports whether a node of this kind declares something, if it
// also has a name.
func declKind(kind string) bool {
	for _, s := range []string{"_declaration", "_definition", "_item", "_spec", "_signature", "_declarator"} {
		if strings.HasSuffix(kind, s) {
			return true
		}
	}
	switch kind {
	case "method", "singleton_method", "class", "module":
		return true
	}
	return false
}

// containerKind reports whether declarations inside a node of this kind
// are members, eg a class's methods, rather than locals.
func containerKind(kind string) bool {
	for _, s := range []string{"class", "struct", "interface", "trait", "module", "namespace", "impl", "object", "enum"} {
		if strings.Contains(kind, s) {
			return true
		}
	}
	return false
}

// scopeKind reports whether a node of this kind is a function without a
// name, whose declarations are locals, eg a callback's variables.
func scopeKind(kind string) bool {
	return strings.Contains(kind, "function") || strings.Contains(kind, "lambda") || strings.Contains(kind, "closure") || kind == "func_literal"
}

// declarations finds the declarations of src, named in name, with the
// helper's syntax tree. Locals, such as a function's variables, are left
// out; a member is qualified by its container, eg Parser.parse.
func declarations(ctx context.Context, name string, src []byte) ([]decl, error) {
	nodes, err := parseSyntax(ctx, name, src)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(src), "\n")
	text := func(n synNode) string {
		if n.sr != n.er || n.sr >= len(lines) || n.ec > len(lines[n.sr]) || n.sc > n.ec {
			return ""
		}
		return strings.TrimSpace(lines[n.sr][n.sc:n.ec])
	}
	parent := make([]int, len(nodes))
	children := make([][]int, len(nodes))
	var stack []int
	for i, n := range nodes {
		stack = stack[:min(n.depth, len(stack))]
		parent[i] = -1
		if len(stack) > 0 {
			parent[i] = stack[len(stack)-1]
			children[parent[i]] = append(children[parent[i]], i)
		}
		stack = append(stack, i)
	}
	child := func(i int, field string) int {
		for _, c := range children[i] {
			if nodes[c].field == field {
				return c
			}
		}
		return -1
	}
	first := func(i int, kind string) int {
		for j := i + 1; j < len(nodes) && nodes[j].depth > nodes[i].depth; j++ {
			if nodes[j].kind == kind {
				return j
			}
		}
		return -1
	}
	// nameOf is a declaration's own name, or "" when the node names
	// nothing. C's functions name themselves through declarators.
	nameOf := func(i int) string {
		if !declKind(nodes[i].kind) {
			return ""
		}
		if c := child(i, "name"); c >= 0 {
			return text(nodes[c])
		}
		if nodes[i].kind != "function_definition" {
			return ""
		}
		for c := child(i, "declarator"); c >= 0; c = child(c, "declarator") {
			switch nodes[c].kind {
			case "identifier", "field_identifier", "qualified_identifier", "destructor_name", "operator_name":
				return text(nodes[c])
			}
		}
		return ""
	}
	var out []decl
	seen := map[string]int{}
	for i, n := range nodes {
		bare := nameOf(i)
		if bare == "" {
			continue
		}
		// A member takes its containers' names; a declaration inside
		// anything else that declares is a local.
		var quals []string
		local := false
		for p := parent[i]; p >= 0 && !local; p = parent[p] {
			switch {
			case nameOf(p) == "" && scopeKind(nodes[p].kind):
				// A function called where it is written, eg the
				// (() => { ... })() around a whole script, scopes
				// nothing a reader would call local.
				q := parent[p]
				iife := q >= 0 && nodes[q].kind == "parenthesized_expression" && parent[q] >= 0 && nodes[parent[q]].kind == "call_expression"
				local = !iife
			case nodes[p].kind == "impl_item":
				if c := child(p, "type"); c >= 0 {
					quals = append(quals, text(nodes[c]))
				}
			case nameOf(p) != "":
				if containerKind(nodes[p].kind) {
					quals = append(quals, nameOf(p))
				} else {
					local = true
				}
			}
		}
		if local {
			continue
		}
		if n.kind == "method_declaration" {
			if c := child(i, "receiver"); c >= 0 {
				if t := first(c, "type_identifier"); t >= 0 {
					quals = append(quals, text(nodes[t]))
				}
			}
		}
		slices.Reverse(quals)
		qual := strings.Join(append(quals, bare), ".")
		if seen[qual]++; seen[qual] > 1 {
			qual += "#" + strconv.Itoa(seen[qual])
		}
		// A decorated definition starts at its decorators, and comments
		// right above a declaration belong to it.
		top := i
		if p := parent[i]; p >= 0 && nodes[p].kind == "decorated_definition" {
			top = p
		}
		sr, er := nodes[top].sr, n.er
		if n.ec == 0 && er > n.sr {
			er--
		}
		if p := parent[top]; p >= 0 {
			sib := children[p]
			for k := slices.Index(sib, top) - 1; k >= 0; k-- {
				c := nodes[sib[k]]
				if !strings.Contains(c.kind, "comment") || c.er != sr-1 && c.er != sr {
					break
				}
				sr = c.sr
			}
		}
		er = min(er, len(lines)-1)
		body := strings.Join(lines[sr:er+1], "\n")
		toks := codeTokens(name, body)
		d := decl{name: qual, bare: bare, sr: sr, er: er, raw: sha256.Sum256([]byte(body))}
		d.sum = tokenSum(toks, "")
		d.shape = tokenSum(toks, bare)
		out = append(out, d)
	}
	return out, nil
}

// codeToken is one token of code that is not whitespace, and where it
// lies in the text lexed.
type codeToken struct {
	text       string
	start, end int
}

// codeTokens lexes text as the file name says and drops whitespace, so
// layout alone changes nothing.
func codeTokens(name, text string) []codeToken {
	var out []codeToken
	at := 0
	for _, t := range tokenise(codeLexer(name), text) {
		v := t.Value
		if strings.TrimSpace(v) != "" && t.Type != chroma.TextWhitespace {
			out = append(out, codeToken{v, at, at + len(v)})
		}
		at += len(v)
	}
	return out
}

// tokenSum hashes tokens, with blank, when given, in place of every
// token equal to it.
func tokenSum(toks []codeToken, blank string) [32]byte {
	h := sha256.New()
	for _, t := range toks {
		if blank != "" && t.text == blank {
			h.Write([]byte{1})
		} else {
			h.Write([]byte(t.text))
		}
		h.Write([]byte{0})
	}
	var s [32]byte
	h.Sum(s[:0])
	return s
}

// compareDecls says what changed between the declarations of one file
// before and after a commit. It returns what was added and removed apart,
// so moves between files can be matched once every file is read.
func compareDecls(file string, old, cur []decl) (changes []declChange, added, removed []decl) {
	oldBy := map[string]decl{}
	for _, d := range old {
		oldBy[d.name] = d
	}
	curBy := map[string]bool{}
	var common []string
	for _, d := range cur {
		curBy[d.name] = true
		o, ok := oldBy[d.name]
		switch {
		case !ok:
			added = append(added, d)
			continue
		case o.raw == d.raw:
		case o.sum == d.sum:
			changes = append(changes, declChange{what: []string{"reformatted"}, name: d.name, file: file, sr: d.sr, er: d.er})
		default:
			changes = append(changes, declChange{what: []string{"changed"}, name: d.name, file: file, sr: d.sr, er: d.er})
		}
		common = append(common, d.name)
	}
	for _, d := range old {
		if !curBy[d.name] {
			removed = append(removed, d)
		}
	}
	// A declaration whose place among the others changed moved, eg a
	// function cut and pasted lower down.
	if len(common) <= maxDecls {
		var before []string
		for _, d := range old {
			if curBy[d.name] {
				before = append(before, d.name)
			}
		}
		stay := map[string]bool{}
		for _, p := range lcs(before, common) {
			stay[before[p[0]]] = true
		}
		for _, n := range common {
			if stay[n] {
				continue
			}
			i := slices.IndexFunc(changes, func(c declChange) bool { return c.name == n })
			if i >= 0 {
				changes[i].what = append([]string{"moved"}, changes[i].what...)
				continue
			}
			d := cur[slices.IndexFunc(cur, func(d decl) bool { return d.name == n })]
			changes = append(changes, declChange{what: []string{"moved"}, name: n, file: file, sr: d.sr, er: d.er})
		}
	}
	// A declaration gone under one name and new under another with the
	// same tokens was renamed.
	for i := 0; i < len(added); i++ {
		a := added[i]
		j := slices.IndexFunc(removed, func(r decl) bool { return r.shape == a.shape })
		if j < 0 {
			continue
		}
		changes = append(changes, declChange{what: []string{"renamed"}, name: a.name, from: removed[j].name, file: file, sr: a.sr, er: a.er})
		removed = slices.Delete(removed, j, j+1)
		added = slices.Delete(added, i, i+1)
		i--
	}
	return changes, added, removed
}

// lcs returns the index pairs of a longest common subsequence of a and
// b, each an index into a and one into b.
func lcs(a, b []string) [][2]int {
	n, m := len(a), len(b)
	t := make([][]int32, n+1)
	for i := range t {
		t[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else {
				t[i][j] = max(t[i+1][j], t[i][j+1])
			}
		}
	}
	var out [][2]int
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j]:
			out = append(out, [2]int{i, j})
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

// tokenMarks finds, in a run of removed lines followed by a run of added
// ones, the tokens that changed, as byte ranges per line without the
// diff's leading sign. Runs that share under half their tokens get no
// marks, since marking nearly everything says nothing.
func tokenMarks(name string, del, add []string) (dm, am [][][2]int) {
	strip := func(ls []string) string {
		var b strings.Builder
		for _, l := range ls {
			b.WriteString(l[1:] + "\n")
		}
		return b.String()
	}
	dt, at := strip(del), strip(add)
	dk, ak := codeTokens(name, dt), codeTokens(name, at)
	if len(dk) == 0 || len(ak) == 0 || len(dk)*len(ak) > maxTokenGrid {
		return nil, nil
	}
	dv, av := make([]string, len(dk)), make([]string, len(ak))
	for i, t := range dk {
		dv[i] = t.text
	}
	for i, t := range ak {
		av[i] = t.text
	}
	common := lcs(dv, av)
	if 2*len(common) < max(len(dk), len(ak)) {
		return nil, nil
	}
	keptD, keptA := map[int]bool{}, map[int]bool{}
	for _, p := range common {
		keptD[p[0]], keptA[p[1]] = true, true
	}
	return lineRanges(dt, dk, keptD, len(del)), lineRanges(at, ak, keptA, len(add))
}

// lineRanges turns the tokens not kept into byte ranges per line of text,
// joining neighbours on one line that only spaces part.
func lineRanges(text string, toks []codeToken, kept map[int]bool, n int) [][][2]int {
	out := make([][][2]int, n)
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	for i, t := range toks {
		if kept[i] {
			continue
		}
		// A token may span lines, eg a long string; each line gets its
		// part.
		for s := t.start; s < t.end; {
			line := lineOf(starts, s)
			if line >= n {
				break
			}
			lineEnd := starts[line+1] - 1
			e := min(t.end, lineEnd)
			if e > s {
				from, to := s-starts[line], e-starts[line]
				rs := out[line]
				if k := len(rs); k > 0 && strings.TrimSpace(text[starts[line]+rs[k-1][1]:s]) == "" {
					rs[k-1][1] = to
				} else {
					rs = append(rs, [2]int{from, to})
				}
				out[line] = rs
			}
			s = lineEnd + 1
		}
	}
	return out
}

// lineOf returns the line whose start is the last at or before offset.
func lineOf(starts []int, offset int) int {
	i, _ := slices.BinarySearch(starts, offset+1)
	return i - 1
}

// diffMarks finds the changed tokens of one file's diff: in every hunk,
// each run of removed lines and the run of added lines after it. The
// result maps a line's index in lines to its ranges.
func diffMarks(name string, lines []string) map[int][][2]int {
	marks := map[int][][2]int{}
	for i := 0; i < len(lines); {
		if !strings.HasPrefix(lines[i], "-") {
			i++
			continue
		}
		d := i
		for i < len(lines) && strings.HasPrefix(lines[i], "-") {
			i++
		}
		a := i
		for i < len(lines) && strings.HasPrefix(lines[i], "+") {
			i++
		}
		if a == i {
			continue
		}
		dm, am := tokenMarks(name, lines[d:a], lines[a:i])
		for k, r := range dm {
			if len(r) > 0 {
				marks[d+k] = r
			}
		}
		for k, r := range am {
			if len(r) > 0 {
				marks[a+k] = r
			}
		}
	}
	return marks
}

// semanticCache keeps semantic diffs by commit, as gitCache keeps the
// plain ones; a parse costs a process start per file and version.
var semanticCache = newCache(8 << 20)

// semanticKey is where the semantic diff of the commit hash, limited to
// name, is kept, or "" when hash is not a full one.
func (t *tree) semanticKey(name, hash string) string {
	if !fullHash(hash) {
		return ""
	}
	return t.dir + "\x00" + t.prefix + "\x00" + name + "\x00" + hash + fmt.Sprintf("\x00%t", t.shared)
}

// semanticDiff reads each changed file before and after the commit and
// compares them. Files are read and parsed four at a time.
func (t *tree) semanticDiff(ctx context.Context, name, hash string, diffs []fileDiff) *semantic {
	key := t.semanticKey(name, hash)
	if key != "" {
		if v, ok := semanticCache.get(key); ok {
			return v.(*semantic)
		}
	}
	sem := &semantic{marks: map[string]map[int][][2]int{}}
	dir, _, err := t.gitPlace(name)
	if err != nil {
		return sem
	}
	type side struct {
		decls []decl
		err   error
	}
	type result struct{ old, cur side }
	results := make([]result, len(diffs))
	read := func(rev, rel, file string) side {
		if rel == "" {
			return side{}
		}
		b, err := git(ctx, dir, "cat-file", "blob", rev+":./"+rel)
		if err != nil || len(b) > maxCode || !isText(b) {
			return side{}
		}
		ds, err := declarations(ctx, file, b)
		return side{ds, err}
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, d := range diffs {
		sem.marks[d.name] = diffMarks(d.name, d.lines)
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			results[i] = result{read(hash+"^", d.rel, d.name), read(hash, d.rel, d.name)}
		}()
	}
	wg.Wait()
	var added, removed []declChange
	var addedDecls, removedDecls []decl
	for i, d := range diffs {
		r := results[i]
		for _, e := range []error{r.old.err, r.cur.err} {
			switch {
			case e == nil, errors.Is(e, errNoGrammar):
			case errors.Is(e, errNoHelper):
				sem.note = "Declarations need " + progName + "-syntax, which is not installed; `make syntax` builds it."
			default:
				logErr("no declarations for %s at %s: %v", d.name, hash, e)
			}
		}
		ch, add, rem := compareDecls(d.name, r.old.decls, r.cur.decls)
		sem.changes = append(sem.changes, ch...)
		for _, a := range add {
			added = append(added, declChange{what: []string{"added"}, name: a.name, file: d.name, sr: a.sr, er: a.er})
			addedDecls = append(addedDecls, a)
		}
		for _, x := range rem {
			removed = append(removed, declChange{what: []string{"removed"}, name: x.name, file: d.name, sr: x.sr, er: x.er, gone: true})
			removedDecls = append(removedDecls, x)
		}
	}
	// A declaration removed from one file and added to another under the
	// same name, or with the same tokens, moved there.
	for i := 0; i < len(added); i++ {
		a := addedDecls[i]
		j := -1
		for k, x := range removedDecls {
			if removed[k].file != added[i].file && (x.name == a.name || x.shape == a.shape) {
				j = k
				break
			}
		}
		if j < 0 {
			continue
		}
		x := removedDecls[j]
		c := added[i]
		c.what, c.from = []string{"moved"}, removed[j].file
		switch {
		case x.name != a.name:
			c.what = append(c.what, "renamed")
			c.from += ", was " + x.name
		case x.raw == a.raw:
		case x.sum == a.sum:
			c.what = append(c.what, "reformatted")
		default:
			c.what = append(c.what, "changed")
		}
		sem.changes = append(sem.changes, c)
		added = slices.Delete(added, i, i+1)
		addedDecls = slices.Delete(addedDecls, i, i+1)
		removed = slices.Delete(removed, j, j+1)
		removedDecls = slices.Delete(removedDecls, j, j+1)
		i--
	}
	sem.changes = append(append(sem.changes, added...), removed...)
	// Each file's changes keep the order of the diff, and within a file,
	// the order of the rows.
	order := map[string]int{}
	for i, d := range diffs {
		order[d.name] = i
	}
	slices.SortStableFunc(sem.changes, func(a, b declChange) int {
		if a.file != b.file {
			return order[a.file] - order[b.file]
		}
		return a.sr - b.sr
	})
	if key != "" && ctx.Err() == nil {
		size := int64(256)
		for _, c := range sem.changes {
			size += int64(len(c.name)+len(c.from)+len(c.file)) + 96
		}
		for _, m := range sem.marks {
			size += int64(len(m)) * 48
		}
		semanticCache.put(key, sem, size)
	}
	return sem
}

// semanticHTML renders the declarations a commit changed, one row each
// under its file's name. A row links its lines in the commit's version of
// the file, where the tree has versions.
func (t *tree) semanticHTML(ctx context.Context, name string, c *commit, sem *semantic) string {
	esc := template.HTMLEscapeString
	var b strings.Builder
	b.WriteString("<div class=\"semantic\">\n")
	if sem.note != "" {
		b.WriteString("<p class=\"own\">" + esc(sem.note) + "</p>\n")
	}
	versions := t.historyInVersions(ctx, name)
	stars := t.starsFor(ctx, name)
	file := ""
	for _, ch := range sem.changes {
		if ch.file != file {
			if file != "" {
				b.WriteString("</ul>\n")
			}
			file = ch.file
			b.WriteString("<p class=\"own file\">" + esc(file) + "</p>\n<ul class=\"decls\">\n")
		}
		label := esc(ch.name)
		if versions && !ch.gone {
			lines := "L" + strconv.Itoa(ch.sr+1)
			if ch.er > ch.sr {
				lines += "-L" + strconv.Itoa(ch.er+1)
			}
			label = "<a href=\"" + esc(t.versionURL(stars, c, ch.file)+"#"+lines) + "\">" + label + "</a>"
		}
		what := strings.Join(ch.what, ", ")
		if ch.from != "" {
			if ch.what[0] == "moved" {
				label += " from " + esc(ch.from)
			} else {
				label += ", was " + esc(ch.from)
			}
		}
		fmt.Fprintf(&b, "<li><span class=\"what\">%s</span> %s</li>\n", esc(what), label)
	}
	if file != "" {
		b.WriteString("</ul>\n")
	}
	b.WriteString("</div>\n")
	return b.String()
}

// markLine renders one line of a diff with its changed ranges marked.
func markLine(line string, ranges [][2]int) string {
	esc := template.HTMLEscapeString
	if len(ranges) == 0 || line == "" {
		return esc(line)
	}
	var b bytes.Buffer
	b.WriteString(esc(line[:1]))
	body := line[1:]
	at := 0
	for _, r := range ranges {
		if r[0] < at || r[1] > len(body) || !utf8.ValidString(body[r[0]:r[1]]) {
			continue
		}
		b.WriteString(esc(body[at:r[0]]) + "<mark>" + esc(body[r[0]:r[1]]) + "</mark>")
		at = r[1]
	}
	b.WriteString(esc(body[at:]))
	return b.String()
}
