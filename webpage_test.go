package main

import (
	"strings"
	"testing"
)

// The web address's index links back to the trees on the default address.
func TestWebLinksHome(t *testing.T) {
	w := testWeb(t, defaultDisk)
	if strings.Contains(webGet(w, "/").Body.String(), "Trees:") {
		t.Errorf("a link home without a home")
	}
	w.srv.homeURL = "http://127.31.1.1:8080"
	if body := webGet(w, "/").Body.String(); !strings.Contains(body, `Trees: <a href="http://127.31.1.1:8080/">127.31.1.1:8080/</a>`) {
		t.Errorf("no link home: %s", body)
	}
}
