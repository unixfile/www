package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// defaultPort is 8080 until main finds that this process may bind port 80.
var defaultPort uint16 = 8080

// capNetBindService is the capability bit that lets a process bind ports
// below 1024.
const capNetBindService = 10

// lowPortOK reports whether this process may bind port 80: it holds
// CAP_NET_BIND_SERVICE, as the Debian package grants, or the machine lets
// every user bind low ports.
func lowPortOK() bool {
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_unprivileged_port_start"); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n <= 80 {
			return true
		}
	}
	b, err := os.ReadFile("/proc/self/status")
	return err == nil && hasCap(string(b), capNetBindService)
}

// hasCap reads the effective capability set from a /proc status file.
func hasCap(status string, bit uint) bool {
	for _, line := range strings.Split(status, "\n") {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			caps, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return err == nil && caps&(1<<bit) != 0
		}
	}
	return false
}

// Environment variables that set the default addresses, the command that
// opens a loopback URL, and a share's chosen token.
var (
	addrEnv   = envPrefix + "ADDR"
	shareEnv  = envPrefix + "SHARE"
	openEnv   = envPrefix + "OPEN"
	secretEnv = envPrefix + "SECRET"
)

// secretToken returns the token in WWW_SECRET, or a random one when it is
// unset. A bad token's error names the variable but not its value, which
// is a secret.
func secretToken() (string, error) {
	v := os.Getenv(secretEnv)
	switch {
	case v == "":
		return newToken(), nil
	case !validToken(v):
		return "", &errno{syscall.EINVAL, secretEnv + " holds a bad token: want letters, digits, - and _"}
	}
	return v, nil
}

// endpointOf returns where c serves. A bare --share takes WWW_SHARE,
// and a start without -a takes WWW_ADDR; --list, --kill and --restart
// without an address cover every server, so WWW_ADDR leaves them be.
// An error from a variable's value names the variable.
func endpointOf(c config, uid int) (netip.AddrPort, error) {
	if c.sharing {
		if c.share == "" && os.Getenv(shareEnv) != "" {
			ep, err := shareEndpoint(os.Getenv(shareEnv))
			return ep, fromEnv(shareEnv, err)
		}
		return shareEndpoint(c.share)
	}
	if c.addr == "" && !c.list && !c.kill && !c.restart && os.Getenv(addrEnv) != "" {
		ep, err := endpointFor(os.Getenv(addrEnv), uid)
		return ep, fromEnv(addrEnv, err)
	}
	return endpointFor(c.addr, uid)
}

func fromEnv(name string, err error) error {
	if err == nil {
		return nil
	}
	return &errno{errnoOf(err), name + "=" + os.Getenv(name) + ": " + err.Error()}
}

// environment is the help's account of the default addresses: each value
// and where it comes from.
func environment(uid int) string {
	addr := "built in"
	if ep, err := endpointFor("", uid); err == nil {
		addr = ep.String() + ", built in"
	}
	if v := os.Getenv(addrEnv); v != "" {
		addr = v + ", from " + addrEnv
	}
	share := "the default route's address"
	if a, err := routeAddr(); err == nil {
		share = a.String() + ", the default route's address"
	}
	if v := os.Getenv(shareEnv); v != "" {
		share = v + ", from " + shareEnv
	}
	open := "xdg-open, built in"
	if v := os.Getenv(openEnv); v != "" {
		open = v + ", from " + openEnv
	}
	secret := "unset, so a random token"
	if os.Getenv(secretEnv) != "" {
		secret = "set"
	}
	return "\nenvironment:\n" +
		"  " + addrEnv + "    default for a start without -a; now " + addr + "\n" +
		"  " + shareEnv + "   default for a bare --share; now " + share + "\n" +
		"  " + openEnv + "    command that opens a loopback URL, %s for the URL or\n" +
		"              appended, eg 'brave-browser --app=%s'; now " + open + "\n" +
		"  " + secretEnv + "  the token for --secret; now " + secret + "\n"
}

// endpointFor returns where to serve: addr when given, else the user's own
// 127.31.N.1 with N = uid - 1000, so users on one machine never collide.
// The port is defaultPort unless addr names one. -a takes only loopback, so
// a typo cannot share a tree; --share goes through shareEndpoint.
func endpointFor(addr string, uid int) (netip.AddrPort, error) {
	if addr == "" {
		n := uid - 1000
		if n < 0 || n > 255 {
			return netip.AddrPort{}, &errno{syscall.EINVAL, fmt.Sprintf("uid %d has no default address; pass --addr", uid)}
		}
		return netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 31, byte(n), 1}), defaultPort), nil
	}
	ep, err := parseEndpoint(addr, "127.31.1.2")
	if err != nil {
		return ep, err
	}
	if !ep.Addr().IsLoopback() {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": -a takes a loopback address; --share takes the others"}
	}
	return ep, nil
}

// shareEndpoint returns where to share: any IPv4 address beyond loopback,
// 0.0.0.0 included, on defaultPort unless addr names one. With no addr it
// is the default route's address, so a share needs no address remembered.
func shareEndpoint(addr string) (netip.AddrPort, error) {
	if addr == "" {
		a, err := routeAddr()
		if err != nil {
			return netip.AddrPort{}, err
		}
		return netip.AddrPortFrom(a, defaultPort), nil
	}
	ep, err := parseEndpoint(addr, "192.168.1.5")
	if err != nil {
		if ep, err = resolveEndpoint(addr); err != nil {
			return ep, err
		}
	}
	if ep.Addr().IsLoopback() {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": --share takes an address beyond loopback; -a takes loopback"}
	}
	return ep, nil
}

// resolveEndpoint reads name[:port] and resolves the name to an IPv4
// address that an interface here holds; only such an address can be bound.
func resolveEndpoint(addr string) (netip.AddrPort, error) {
	name, port := addr, defaultPort
	if h, p, err := net.SplitHostPort(addr); err == nil {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return netip.AddrPort{}, &errno{syscall.EINVAL, "bad port in " + addr + ": want 1 to 65535"}
		}
		name, port = h, uint16(n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", name)
	if err != nil {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want an IPv4 address or a name, eg 192.168.1.5 or mate.peer.home (" + err.Error() + ")"}
	}
	for _, ip := range ips {
		if ifaceOf(ip.Unmap()) != "any" {
			return netip.AddrPortFrom(ip.Unmap(), port), nil
		}
	}
	return netip.AddrPort{}, &errno{syscall.EADDRNOTAVAIL, fmt.Sprintf("%s resolves to %v, which no interface here holds", name, ips)}
}

// spacedShare catches --share 192.168.1.5, where the address became the
// directory: a bare --share with a directory that does not exist but reads
// or resolves as an address. It returns the hint, or "".
func spacedShare(c config) string {
	if !c.sharing || c.share != "" || c.dir == "" {
		return ""
	}
	if _, err := os.Stat(c.dir); !errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if _, err := parseEndpoint(c.dir, ""); err != nil {
		if _, err := resolveEndpoint(c.dir); errors.Is(err, syscall.EINVAL) {
			return ""
		}
	}
	return "no directory " + c.dir + "; to share on that address, write --share=" + c.dir
}

// routeAddr asks the kernel which source address it would use to reach
// beyond this machine. Connecting a UDP socket picks the route and sends
// nothing. 192.0.2.1 is reserved for documentation, so only the default
// route leads there.
func routeAddr() (netip.Addr, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return netip.Addr{}, &errno{errnoOf(err), "no default route to share on (" + err.Error() + "); name an address, eg --share=192.168.1.5"}
	}
	defer conn.Close()
	a := conn.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap()
	if a.IsLoopback() {
		return netip.Addr{}, &errno{syscall.ENETUNREACH, "the default route leaves from loopback; name an address, eg --share=192.168.1.5"}
	}
	return a, nil
}

// parseEndpoint reads ip[:port] as an IPv4 address and a port above 0.
func parseEndpoint(addr, example string) (netip.AddrPort, error) {
	ep, err := netip.ParseAddrPort(addr)
	if err != nil {
		a, aerr := netip.ParseAddr(addr)
		if aerr != nil {
			return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want eg " + example + " or " + example + ":9000"}
		}
		ep = netip.AddrPortFrom(a, defaultPort)
	}
	if !ep.Addr().Is4() || ep.Port() == 0 {
		return netip.AddrPort{}, &errno{syscall.EINVAL, "bad address " + addr + ": want an IPv4 address and a port above 0"}
	}
	return ep, nil
}

// shared reports whether ep serves beyond this machine.
func shared(ep netip.AddrPort) bool {
	return !ep.Addr().IsLoopback()
}

// newToken draws 128 bits for a secret path, in lower-case base32 so the
// link survives being read aloud or typed.
func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

// validToken keeps a token to one URL path segment that needs no escaping.
func validToken(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
	})
}
