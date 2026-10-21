package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"html/template"
	"os"
	"os/exec"
	"strings"
	"time"
)

// graphCache keeps the SVG that dot drew for each fence, by the hash of
// its source, so a doc with diagrams renders again within the budget.
var graphCache = newCache(8 << 20)

// graphTimeout bounds one run of dot; a large graph can take seconds.
const graphTimeout = 5 * time.Second

// maxGraph bounds the SVG a fence may produce.
const maxGraph = 4 << 20

// isGraph reports whether a fence's language names Graphviz.
func isGraph(lang string) bool {
	return lang == "dot" || lang == "graphviz"
}

// drawGraph returns a fence's diagram as inline SVG, drawn by dot. dot
// reads the source as data: SERVER_NAME makes it refuse the image files a
// graph may name, so a diagram cannot show what lies outside the tree.
// Without dot, or when dot rejects the source, the error says why.
func drawGraph(src string) (template.HTML, error) {
	sum := sha256.Sum256([]byte(src))
	key := string(sum[:])
	if svg, ok := graphCache.get(key); ok {
		return svg.(template.HTML), nil
	}
	dot, err := exec.LookPath("dot")
	if err != nil {
		return "", errors.New("dot is not installed; install graphviz to draw this graph")
	}
	ctx, cancel := context.WithTimeout(context.Background(), graphTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, dot, "-Tsvg")
	// fontconfig keeps its font cache under home; without it every run
	// scans the fonts again.
	cmd.Env = []string{"SERVER_NAME=" + progName, "GV_FILE_PATH=/nonexistent"}
	for _, k := range []string{"HOME", "XDG_CACHE_HOME", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Stdin = strings.NewReader(src)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &limited{&out, maxGraph}, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			msg = "dot took over " + graphTimeout.String()
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	// dot writes an XML prolog and doctype before the svg element, which
	// HTML does not take.
	svg := out.String()
	if i := strings.Index(svg, "<svg"); i >= 0 {
		svg = svg[i:]
	}
	h := template.HTML(`<div class="graph">` + svg + `</div>`)
	graphCache.put(key, h, int64(len(h)))
	return h, nil
}

// limited fails a write past n bytes, which stops dot.
type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.w.Len()+len(p) > l.n {
		return 0, errors.New("graph larger than 4 MB")
	}
	return l.w.Write(p)
}
