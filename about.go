package www

import (
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// about describes the server for its index: where it listens, since when,
// and the binary it runs, so the index can tell when a rebuild has left it
// behind. Tests leave it zero.
type about struct {
	ep      netip.AddrPort
	started time.Time
	exe     string
	exeInfo os.FileInfo
}

func newAbout(ep netip.AddrPort) about {
	a := about{ep: ep, started: time.Now()}
	if exe, err := os.Executable(); err == nil {
		if info, err := os.Stat(exe); err == nil {
			a.exe, a.exeInfo = exe, info
		}
	}
	return a
}

// stale reports whether the binary on disk is no longer the one running.
func (a about) stale() bool {
	if a.exeInfo == nil {
		return false
	}
	info, err := os.Stat(a.exe)
	return err != nil || !os.SameFile(info, a.exeInfo) || !info.ModTime().Equal(a.exeInfo.ModTime())
}

// restartCmd is the command that moves this server's trees to the new
// binary; with no address it moves every server its binary started. It
// names the binary by its path when the name on PATH leads to another,
// eg the package's, which would leave this server alone.
func (a about) restartCmd() string {
	if a.exe != "" {
		p, err := exec.LookPath(progName)
		if err == nil {
			p, err = filepath.EvalSymlinks(p)
		}
		if err != nil || p != a.exe {
			return tildePath(a.exe) + " --restart"
		}
	}
	return progName + " --restart"
}

// status is the index header's account of the server's age. The build
// time is on every page, above the header.
func (a about) status() string {
	if a.started.IsZero() {
		return ""
	}
	return "up since " + clock(a.started)
}

// built is when the running binary was written, read once at start, so
// each page can say which build served it. debugOn, set by --debug,
// puts it above the header, and on the index, each server's build and
// start and when each lock was written. Without it they stay hidden, as
// they serve debugging alone.
var debugOn bool

var buildCommit = buildRev()

var built = func() time.Time {
	exe, err := os.Executable()
	if err != nil {
		return time.Time{}
	}
	info, err := os.Stat(exe)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}()

// versionLine is --version's account of the build, in the words of the
// stamp, with the full date since a build may be old.
func versionLine(t time.Time, commit string) string {
	when := "at an unknown time"
	if !t.IsZero() {
		when = t.Format("2006-01-02 15:04")
	}
	return progName + " built " + when + ", from " + commit
}

// stamp is the build as a page shows it: the time of day, the seconds
// since, which live.js counts on from Millis, the commit, and how long
// the page took to render.
type stamp struct {
	Clock    string
	Millis   int64
	Ago      int
	Commit   string
	Rendered string
}

type startKey struct{}

// rendered says how long the request has taken so far, in milliseconds
// with a decimal below 10, since most docs render in under one.
func rendered(r *http.Request) string {
	start, ok := r.Context().Value(startKey{}).(time.Time)
	if !ok {
		return ""
	}
	ms := float64(time.Since(start).Microseconds()) / 1000
	if ms < 10 {
		return fmt.Sprintf("%.1f ms", ms)
	}
	return fmt.Sprintf("%.0f ms", ms)
}

// clock gives the time of day, with the date when it is not today.
func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	if y, m, d := t.Date(); y == time.Now().Year() && m == time.Now().Month() && d == time.Now().Day() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
