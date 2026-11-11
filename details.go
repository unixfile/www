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
// the command each view stands for, as the history entry does.
const (
	namesLabel   = "<(ls)"
	detailsLabel = "<(ls -l)"
)

// A details view sorts by name, or by the flag in its query, eg
// ?details=t or ?details=Sr: S for size, largest first, t for time,
// newest first, and r after either to reverse it. detailSort reads the
// query's value; any other value, or t in a version, which has no times,
// is name order.
func detailSort(v string, version bool) (key string, rev bool) {
	switch v {
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
	if rev && key != "" {
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
// sizes and -t over the ages, each a link. A first click sorts by the
// column as ls does, a second adds r and reverses, a third returns to
// name order. The flag in force shows as the view's command has it, eg
// -tr, in reverse video. A version has no ages and so no -t.
func detailHeader(key string, rev, version bool) string {
	cell := func(class, k string) string {
		label, next, on := "-"+k, k, ""
		if k == key {
			label, on = "-"+sortFlags(key, rev), ` class="on"`
			next = sortFlags(k, true)
			if rev {
				next = ""
			}
		}
		return `<span class="lead num ` + class + `"><a` + on + ` href="` + detailsQuery(next) + `">` + label + `</a></span> `
	}
	age := `<span class="lead num age"></span> `
	if !version {
		age = cell("age", "t")
	}
	return `<li class="flags">` + cell("size", "S") + age + "</li>\n"
}

// detailsCmd is the label of the details view's entry: the command with
// the sort's flags when that view is on screen, eg <(ls -ltr).
func detailsCmd(flags string, here bool) string {
	if !here || flags == "" {
		return detailsLabel
	}
	return "<(ls -l" + flags + ")"
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
