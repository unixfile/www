package www

import (
	"strings"
	"testing"
)

// CAP_NET_BIND_SERVICE is bit 10 of the effective set.
func TestHasCap(t *testing.T) {
	for status, want := range map[string]bool{
		"Name:\t" + progName + "\nCapInh:\t0000000000000000\nCapEff:\t0000000000000400\n": true,
		"CapEff:\t000001ffffffffff\n":                            true,
		"CapPrm:\t0000000000000400\nCapEff:\t0000000000000000\n": false,
		"Name:\t" + progName + "\n":                              false,
	} {
		if got := hasCap(status, capNetBindService); got != want {
			t.Errorf("%q: %v, want %v", status, got, want)
		}
	}
}

// A chosen token comes from WWW_SECRET, never the command line, and a bad
// one's error names the variable without echoing the secret.
func TestSecretToken(t *testing.T) {
	t.Setenv(secretEnv, "k3x9")
	if got, err := secretToken(); err != nil || got != "k3x9" {
		t.Errorf("set: %q, %v; want k3x9", got, err)
	}
	t.Setenv(secretEnv, "a/b")
	if _, err := secretToken(); err == nil || !strings.Contains(err.Error(), secretEnv) || strings.Contains(err.Error(), "a/b") {
		t.Errorf("bad token: %v; want an error naming %s without the token", err, secretEnv)
	}
	t.Setenv(secretEnv, "")
	a, errA := secretToken()
	b, errB := secretToken()
	if errA != nil || errB != nil || a == "" || a == b {
		t.Errorf("unset: %q, %q, %v, %v; want two different random tokens", a, b, errA, errB)
	}
}
