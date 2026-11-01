package main

import (
	"slices"
	"testing"
)

func TestOpenArgs(t *testing.T) {
	const lo, lan = "http://127.31.1.1/x/", "http://192.168.1.5/x/"
	for _, c := range []struct {
		url, open string
		want      []string
	}{
		{lo, "", []string{"xdg-open", lo}},
		{lo, "brave-browser --app=%s", []string{"brave-browser", "--app=" + lo}},
		{lo, "firefox", []string{"firefox", lo}},
		{lan, "brave-browser --app=%s", []string{"xdg-open", lan}},
	} {
		if got := openArgs(c.url, c.open); !slices.Equal(got, c.want) {
			t.Errorf("openArgs(%q, %q) = %q, want %q", c.url, c.open, got, c.want)
		}
	}
}
