package main

import (
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// taken explains why ep cannot be bound when no mdserve of this user serves
// there: it names the listeners that hold the port, marking the ones that
// are mdserve servers on other addresses.
func taken(ep netip.AddrPort) string {
	var names []string
	for _, l := range holders(ep) {
		name := l.String()
		if len(peekTrees(l)) > 0 {
			name = "the " + progName + " on " + name
		}
		names = append(names, name)
	}
	msg := ep.String() + " is taken"
	if len(names) > 0 {
		msg += " by " + strings.Join(names, ", ")
	} else {
		msg += ", and no " + progName + " of yours serves there"
	}
	if ep.Addr().IsUnspecified() {
		msg += ". 0.0.0.0 claims the port on every address; share on one address, eg --share, or on another port, eg --share=0.0.0.0:8081"
	}
	return msg
}

// holders returns the TCP listeners whose address keeps ep from binding:
// one on the same address, or on either side a wildcard on the same port.
func holders(ep netip.AddrPort) []netip.AddrPort {
	var out []netip.AddrPort
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, l := range listeners(string(b)) {
			if l.Port() == ep.Port() && (ep.Addr().IsUnspecified() || l.Addr().IsUnspecified() || l.Addr() == ep.Addr()) {
				out = append(out, l)
			}
		}
	}
	return out
}

// listeners reads the sockets in LISTEN state from a /proc/net/tcp or tcp6
// table. The kernel prints each 32-bit word of an address in host order.
func listeners(table string) []netip.AddrPort {
	var out []netip.AddrPort
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		addr, port, ok := strings.Cut(f[1], ":")
		raw, err := hex.DecodeString(addr)
		n, perr := strconv.ParseUint(port, 16, 16)
		if !ok || err != nil || perr != nil || (len(raw) != 4 && len(raw) != 16) {
			continue
		}
		for i := 0; i < len(raw); i += 4 {
			binary.NativeEndian.PutUint32(raw[i:], binary.BigEndian.Uint32(raw[i:]))
		}
		a, _ := netip.AddrFromSlice(raw)
		out = append(out, netip.AddrPortFrom(a.Unmap(), uint16(n)))
	}
	return out
}
