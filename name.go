package www

import (
	_ "embed"
	"strings"
	"syscall"
)

//go:embed WORKING_NAME
var nameFile string

// progName is the program's name, from the file WORKING_NAME, which the
// Makefile and the package files read too. Every path, variable, URL and
// message that names the program derives from it, so a rename changes one
// word.
var progName = strings.TrimSpace(nameFile)

// envPrefix starts the name of every environment variable the program
// reads, eg WWW_ADDR.
var envPrefix = strings.ToUpper(progName) + "_"

// ownPath is the URL path of the program's own resources, eg the
// stylesheet and the live reload stream. Trees mount at paths relative to
// home, which this one, starting with an underscore, does not shadow.
var ownPath = "/_" + progName + "/"

// basePath is the path every URL of the server starts with, eg /www, set
// by --base for a server that shares its domain with other sites behind a
// proxy; "" serves at the root. It has no trailing slash. Tree prefixes,
// ownPath and the stylesheet's URL include it, so most code never names
// it; code that writes or reads a path from the root does, through rooted
// and unrooted.
var basePath string

// setBase sets basePath and what derives from it. It runs once, before
// any server starts.
func setBase(b string) {
	basePath = b
	ownPath = b + "/_" + progName + "/"
	styleURL = ownPath + styleFile
	indexCrumb.Href = b + "/"
}

// rooted is the URL path of p, a path from the server's root, eg
// /github.com/junegunn/fzf/.
func rooted(p string) string { return basePath + p }

// unrooted is urlPath as a path from the server's root, and whether it
// lies below basePath at all.
func unrooted(urlPath string) (string, bool) {
	rest, ok := strings.CutPrefix(urlPath, basePath)
	if !ok || rest != "" && rest[0] != '/' {
		return "", false
	}
	if rest == "" {
		rest = "/"
	}
	return rest, true
}

// checkBase is --base's value as basePath holds it, or an error. A base
// is one or more segments of letters, digits, -, . and _, since it goes
// into every URL unescaped. A segment that starts with _ could shadow
// ownPath, and . or .. is no name.
func checkBase(v string) (string, error) {
	b := strings.TrimSuffix(v, "/")
	bad := &errno{syscall.EINVAL, "--base wants a path such as /" + progName + ": segments of letters, digits, -, . and _, got " + v}
	if !strings.HasPrefix(b, "/") {
		return "", bad
	}
	for _, seg := range strings.Split(b[1:], "/") {
		if seg == "" || seg == "." || seg == ".." || seg[0] == '_' || strings.Trim(seg, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._") != "" {
			return "", bad
		}
	}
	return b, nil
}

// publicName is the executable that serves online repos to the open web.
var publicName = progName + "-public"
