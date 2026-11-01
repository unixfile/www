package main

import (
	"fmt"
	"net/netip"
	neturl "net/url"
	"os"
	"os/exec"
	"strings"
)

// openBrowser opens url when a display exists. Over SSH there is none, and
// the printed URL has to do.
func openBrowser(url string) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	args := openArgs(url, os.Getenv(openEnv))
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "E%d: cannot open %s with %s: %v\n", errnoOf(err), url, args[0], err)
		return
	}
	go cmd.Wait()
}

// openArgs is the command that opens url. A loopback URL takes WWW_OPEN,
// split at spaces, with each %s replaced by the URL, or the URL appended
// when none holds %s; eg 'brave-browser --app=%s' opens an app window. A
// shared URL, and any URL without the variable, goes to xdg-open.
func openArgs(url, open string) []string {
	fields := strings.Fields(open)
	if len(fields) == 0 || !loopbackURL(url) {
		return []string{"xdg-open", url}
	}
	placed := false
	for i, f := range fields {
		if strings.Contains(f, "%s") {
			fields[i] = strings.ReplaceAll(f, "%s", url)
			placed = true
		}
	}
	if !placed {
		fields = append(fields, url)
	}
	return fields
}

// loopbackURL reports whether u names a loopback host.
func loopbackURL(u string) bool {
	p, err := neturl.Parse(u)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(p.Hostname())
	return err == nil && a.IsLoopback() || p.Hostname() == "localhost"
}
