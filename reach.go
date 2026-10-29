package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// A public server fetches whatever host a stranger names, so it must not
// reach the machine or the network it sits in: a probe of
// /127.0.0.1:6379/x, or a forge whose name leads to 10.0.0.1, would
// otherwise make it a stranger's way in. publicNet holds git and the
// listings to hosts whose every address lies on the open internet. git
// connects only to the addresses checked, so a name that answers again
// otherwise is not asked twice, and follows no redirect, which could
// lead anywhere.
var publicNet atomic.Bool

// reachable reports whether a public server may connect to a. Tests,
// whose forges listen on loopback, replace it.
var reachable = func(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range unreachable {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// unreachable are the ranges that IsGlobalUnicast and IsPrivate pass but
// lead into a provider's or a site's own network.
var unreachable = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64, to any IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("fec0::/10"), // site-local
}

// reachWait is how long a host's check holds, so pages that run git do
// not look the host up each time.
const reachWait = time.Minute

type reachAnswer struct {
	args []string
	err  error
	at   time.Time
}

var (
	reachMu    sync.Mutex
	reachCache = map[string]reachAnswer{}
)

// reach returns git's options that hold it to host's addresses, or why a
// public server may not reach host. host is as a URL names it, with a
// port or without.
func reach(ctx context.Context, host string) ([]string, error) {
	reachMu.Lock()
	a, ok := reachCache[host]
	reachMu.Unlock()
	if ok && time.Since(a.at) < reachWait {
		return a.args, a.err
	}
	args, err := lookupReach(ctx, host)
	// A lookup cut short says nothing of the host, so it is not kept.
	if err != nil && ctx.Err() != nil {
		return nil, err
	}
	reachMu.Lock()
	if len(reachCache) > 1000 {
		clear(reachCache)
	}
	reachCache[host] = reachAnswer{args, err, time.Now()}
	reachMu.Unlock()
	return args, err
}

func lookupReach(ctx context.Context, host string) ([]string, error) {
	name, port := host, ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		name, port = h, p
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "["), "]")
	if port == "" {
		port = "443"
		if webScheme == "http" {
			port = "80"
		}
	}
	args := []string{"-c", "http.followRedirects=false"}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(name); err == nil {
		addrs = []netip.Addr{a}
	} else {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", name); err != nil {
			return nil, &errno{syscall.EHOSTUNREACH, "cannot look up " + name + ": " + err.Error()}
		}
		var list []string
		for _, a := range addrs {
			if a = a.Unmap(); a.Is6() {
				list = append(list, "["+a.String()+"]")
			} else {
				list = append(list, a.String())
			}
		}
		args = append(args, "-c", "http.curloptResolve="+name+":"+port+":"+strings.Join(list, ","))
	}
	for _, a := range addrs {
		if !reachable(a) {
			return nil, &errno{syscall.EACCES, host + " leads to " + a.Unmap().String() + ", which is not on the open internet, so a public server does not fetch from it"}
		}
	}
	return args, nil
}

// gitHost is the host git reaches when it runs in dir with args: the
// host of a repo in the cache, or of the URL a probe names.
func gitHost(dir string, args []string) string {
	if dir != cacheRoot {
		rel, err := filepath.Rel(cacheRoot, dir)
		if err != nil {
			return ""
		}
		host, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		return host
	}
	for _, a := range args {
		if strings.HasPrefix(a, webScheme+"://") {
			if u, err := url.Parse(a); err == nil {
				return u.Host
			}
		}
	}
	return ""
}

// publicClient is webClient for a public server: it connects only to
// addresses on the open internet, wherever a name or a redirect leads,
// and through no proxy, which would hide where.
var publicClient = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
	DialContext: (&net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		if ap, err := netip.ParseAddrPort(address); err != nil || !reachable(ap.Addr()) {
			return errors.New(address + " is not on the open internet")
		}
		return nil
	}}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second,
	ForceAttemptHTTP2:   true,
}}
