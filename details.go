package www

import (
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"strconv"
	"time"
)

// The labels of the entries that switch a listing between its views name
// the command each view stands for. They are bare commands: a listing is
// the directory itself, seen through ls. The history entry alone is
// written as a process substitution, <(git log), since it is a page of
// its own, made from a command's output, and also stands in the crumbs.
const (
	namesLabel   = "ls"
	detailsLabel = "ls -l"
)

// A details view sorts by name, or by the flags in its query, eg
// ?details=t or ?details=Sr: S for size, largest first, t for time,
// newest first, and r, alone or after either, to reverse the order.
// detailSort reads the query's value; any other value, or t in a version,
// which has no times, is name order.
func detailSort(v string, version bool) (key string, rev bool) {
	switch v {
	case "r":
		return "", true
	case "S", "Sr", "t", "tr":
		if v[0] == 't' && version {
			return "", false
		}
		return v[:1], len(v) == 2
	}
	return "", false
}

// sortFlags writes a sort as its query value, eg "tr".
func sortFlags(key string, rev bool) string {
	if rev {
		return key + "r"
	}
	return key
}

// detailsQuery is the query of a details view in a sort.
func detailsQuery(flags string) string {
	if flags == "" {
		return "?details"
	}
	return "?details=" + flags
}

// detailHeader is the row over the details view's columns: -S over the
// sizes, -t over the ages and a bare dash over the names, each a link. A first
// click sorts by the column as ls does, a second adds r and reverses, a
// third returns to name order; on name, a click reverses and the next
// returns. The order in force is in reverse video and shows its flags as
// the view's command has them, eg -tr. ls has no flag for name order, so
// that header is a dash with no flag after it, and -r once reversed. A version has no ages
// and so no -t.
func detailHeader(key string, rev, version bool) string {
	link := func(k, off string) string {
		label, next, on := off, k, ""
		if k == key {
			on = ` class="on"`
			next = sortFlags(k, !rev)
			if rev {
				label = "-" + sortFlags(k, true)
				if k != "" {
					next = ""
				}
			}
		}
		return `<a` + on + ` href="` + detailsQuery(next) + `">` + label + `</a>`
	}
	age := ""
	if !version {
		age = link("t", "-t")
	}
	// The name's link sits in a span, so the listing's rule that
	// stretches a row's first link over the row leaves this row alone.
	return `<li class="flags"><span class="lead num size">` + link("S", "-S") + `</span> <span class="lead num age">` + age +
		`</span> <span>` + link("", "-") + "</span></li>\n"
}

// detailsCmd is the label of the details view's entry: the command with
// the sort's flags when that view is on screen, eg ls -ltr.
func detailsCmd(flags string, here bool) string {
	if !here || flags == "" {
		return detailsLabel
	}
	return detailsLabel + flags
}

// detailFields is the lead of a row in the details view: size, then age,
// each right-aligned in a column of fixed width. A directory has no size,
// and a version has no age, since git keeps no modification times; a
// field without a value stays empty, so the names still line up.
func detailFields(info fs.FileInfo, dir, version bool, now time.Time) string {
	size, age := "", ""
	if info != nil && !dir {
		size = humanSize(info.Size())
	}
	if info != nil && !version {
		mod := info.ModTime()
		age = fmt.Sprintf("<span class=\"lead num age\" title=\"%s\">%s</span>", exact(mod), ageOf(now.Sub(mod)))
	} else {
		age = "<span class=\"lead num age\"></span>"
	}
	return "<span class=\"lead num size\">" + template.HTMLEscapeString(size) + "</span> " + age + " "
}

// humanSize writes n bytes as ls -h does: bytes below 1024, then one
// decimal below 10 of a unit and whole units above, rounded up, in powers
// of 1024. It is never wider than four characters.
func humanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10)
	}
	f := float64(n)
	for _, u := range "KMGTPE" {
		f /= 1024
		if t := math.Ceil(f*10) / 10; t < 10 {
			return strconv.FormatFloat(t, 'f', 1, 64) + string(u)
		}
		if c := math.Ceil(f); c < 1024 {
			return strconv.FormatFloat(c, 'f', 0, 64) + string(u)
		}
	}
	return strconv.FormatInt(n, 10)
}

// ageOf writes how long ago a file changed in its largest whole unit:
// minutes, hours, days, weeks, then years, eg 5m, 3d, 2y. A time in the
// future, from a skewed clock, reads 0m.
func ageOf(d time.Duration) string {
	const day = 24 * time.Hour
	switch {
	case d < time.Hour:
		return strconv.Itoa(int(max(d, 0)/time.Minute)) + "m"
	case d < day:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	case d < 7*day:
		return strconv.Itoa(int(d/day)) + "d"
	case d < 365*day:
		return strconv.Itoa(int(d/(7*day))) + "w"
	default:
		return strconv.Itoa(int(d/(365*day))) + "y"
	}
}
