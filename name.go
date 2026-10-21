package main

import (
	_ "embed"
	"strings"
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
