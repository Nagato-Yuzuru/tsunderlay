// Package rib reads the IPv4 routing table of the kernel.
package rib

import (
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/net/route"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// Fetch reads the IPv4 routing table. It reads the interface names again on
// each call, because a VPN reconnect gives a utun with the same name a new
// index.
func Fetch() ([]underlay.Route, error) {
	b, err := route.FetchRIB(syscall.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, fmt.Errorf("reading the routing table: %w", err)
	}
	msgs, err := route.ParseRIB(route.RIBTypeRoute, b)
	if err != nil {
		return nil, fmt.Errorf("parsing the routing table: %w", err)
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("reading the interfaces: %w", err)
	}
	names := make(map[int]string, len(ifs))
	for _, i := range ifs {
		names[i.Index] = i.Name
	}
	return Parse(msgs, names), nil
}

// Parse converts route messages to routes. names maps interface indexes to
// names. Parse skips a route on an index that is not in names: its interface
// went away after the kernel wrote the table, and the next run reads a new
// table.
func Parse(msgs []route.Message, names map[int]string) []underlay.Route {
	var routes []underlay.Route
	for _, m := range msgs {
		rm, ok := m.(*route.RouteMessage)
		if !ok {
			continue
		}
		if r, ok := parseRoute(rm, names); ok {
			routes = append(routes, r)
		}
	}
	return routes
}

func parseRoute(rm *route.RouteMessage, names map[int]string) (underlay.Route, bool) {
	iface, ok := names[rm.Index]
	if !ok {
		return underlay.Route{}, false
	}
	dst, ok := addrAt(rm, syscall.RTAX_DST)
	if !ok {
		return underlay.Route{}, false
	}
	bits, ok := prefixLen(rm, dst)
	if !ok {
		return underlay.Route{}, false
	}
	r := underlay.Route{
		Dst:    netip.PrefixFrom(dst, bits),
		Iface:  iface,
		Scoped: rm.Flags&syscall.RTF_IFSCOPE != 0,
		Owned:  rm.Flags&syscall.RTF_PROTO1 != 0,
	}
	// Without RTF_GATEWAY, the gateway slot holds the link or the address
	// of the local interface, and the destination is on the link.
	if rm.Flags&syscall.RTF_GATEWAY != 0 {
		r.Via, _ = addrAt(rm, syscall.RTAX_GATEWAY)
	}
	return r, true
}

func addrAt(rm *route.RouteMessage, i int) (netip.Addr, bool) {
	if i >= len(rm.Addrs) {
		return netip.Addr{}, false
	}
	a, ok := rm.Addrs[i].(*route.Inet4Addr)
	if !ok {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4(a.IP), true
}

// prefixLen returns the length of the netmask. A host route has no netmask.
// The kernel also omits the netmask of the default route.
func prefixLen(rm *route.RouteMessage, dst netip.Addr) (int, bool) {
	if rm.Flags&syscall.RTF_HOST != 0 {
		return 32, true
	}
	mask, ok := addrAt(rm, syscall.RTAX_NETMASK)
	if !ok {
		if dst.IsUnspecified() {
			return 0, true
		}
		return 32, true
	}
	m := mask.As4()
	ones, bits := net.IPMask(m[:]).Size()
	// Size returns 0, 0 for a mask that is not contiguous.
	return ones, bits == 32
}
