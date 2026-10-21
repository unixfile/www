package main

import (
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"
)

// The name lives in NAME alone. Go code takes it from progName, the Makefile
// and the package files from NAME, so a tracked file names the program
// only in prose: comments, README.md, doc/ and go.mod, which Go needs as
// a literal. A name such as www also occurs inside words that belong to
// others, which foreign strips first.
func TestNameHasOneSource(t *testing.T) {
	needGit(t)
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	lower := strings.ToLower(progName)
	for _, f := range strings.Fields(string(out)) {
		if f == "NAME" || f == "go.mod" || f == "README.md" || strings.HasPrefix(f, "doc/") || strings.HasPrefix(f, "assets/fonts/") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if path.Ext(f) == ".go" {
			var s scanner.Scanner
			fset := token.NewFileSet()
			s.Init(fset.AddFile(f, -1, len(src)), src, nil, 0)
			for {
				pos, tok, lit := s.Scan()
				if tok == token.EOF {
					break
				}
				if (tok == token.STRING || tok == token.CHAR) && strings.Contains(foreign.Replace(strings.ToLower(lit)), lower) {
					t.Errorf("%s: %s", fset.Position(pos), lit)
				}
			}
			continue
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(foreign.Replace(strings.ToLower(line)), lower) {
				t.Errorf("%s:%d: %s", f, i+1, line)
			}
		}
	}
}

// foreign removes the words that hold www without naming the program: web
// hosts and the form media type.
var foreign = strings.NewReplacer("://www.", "://", "x-www-form-urlencoded", "")
