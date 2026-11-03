package underlay

import (
	"net/netip"
	"slices"
)

// Tunnel holds the facts that the routes of one tunnel interface (utun,
// ipsec, ppp) show. The facts are independent: a tunnel can have several of
// them, or none. check prints them and does not guess the VPN product from
// them.
type Tunnel struct {
	Iface string
	// Followed is true when Facts.VPN is SplitDefault on this tunnel: Plan
	// adds R1 and R2 for it.
	Followed bool
	// Tailscale is true when the tunnel holds the route to Quad100
	// (100.100.100.100/32). Only the system Tailscale app adds that route.
	Tailscale bool
	// ScopedDefault is an interface-scoped default. Only sockets bound to
	// the tunnel use it.
	ScopedDefault bool
	// GlobalDefault is a global default. Then the LAN has no global default
	// (Facts.LAN is nil), and Plan adds no R1.
	GlobalDefault bool
	// Low and High are the global 0/1 and 128.0/1.
	Low, High bool
	// Prefixes are the other global routes, in table order. They do not
	// include the local address of the tunnel: rib.Parse gives a global /32
	// with no next hop for it.
	Prefixes []netip.Prefix
}

// quad100 is the address of the Tailscale DNS server.
func quad100() netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 100, 100, 100}), 32)
}

// CGNAT is the address range of Tailscale nodes (RFC 6598).
func CGNAT() netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, 0}), 10)
}

// Tunnels returns the facts of each tunnel interface of the table, in the
// order of its first route in the table.
func Tunnels(f Facts) []Tunnel {
	var ts []Tunnel
	for _, r := range f.Table {
		if !isTunnel(r.Iface) {
			continue
		}
		i := slices.IndexFunc(ts, func(t Tunnel) bool { return t.Iface == r.Iface })
		if i < 0 {
			ts = append(ts, Tunnel{Iface: r.Iface, Followed: followed(f.VPN, r.Iface)})
			i = len(ts) - 1
		}
		ts[i].add(r)
	}
	return ts
}

func followed(v VPN, iface string) bool {
	s, ok := v.(SplitDefault)
	return ok && s.Tunnel.Iface == iface
}

func (t *Tunnel) add(r Route) {
	if r.Dst == quad100() {
		t.Tailscale = true
	}
	switch {
	case r.Scoped:
		t.ScopedDefault = t.ScopedDefault || r.Dst.Bits() == 0
	case r.Dst.Bits() == 0:
		t.GlobalDefault = true
	case r.Dst.Bits() == 1 && r.Dst.Addr().As4()[0] == 0:
		t.Low = true
	case r.Dst.Bits() == 1:
		t.High = true
	case r.Dst.IsSingleIP() && !r.Via.IsValid():
		// The local address, or Quad100 on the Tailscale tunnel.
	default:
		t.Prefixes = append(t.Prefixes, r.Dst)
	}
}

// Overlap is a set of global routes of a tunnel that tsunderlay does not
// follow, which take traffic to some addresses of With away from the route
// that should carry it. With is CGNAT or a control prefix. Plan does not fix
// an overlap: it is another conflict than the one that R1 and R2 fix.
type Overlap struct {
	Iface  string
	With   netip.Prefix
	Routes []netip.Prefix
}

// Overlaps returns, for each tunnel and target, the routes that win over
// the route that should carry the traffic:
//
//   - CGNAT: a route that overlaps it and is not less specific (/10 or
//     longer) wins over the CGNAT route of the Tailscale tunnel. The
//     Tailscale tunnel itself is the reference, so it is skipped.
//   - A control prefix: any route more specific than /1 wins over the 0/1
//     or 128.0/1 of the followed tunnel, which unbound control traffic
//     takes otherwise.
//
// Scoped routes only take bound sockets, and Tunnel.Prefixes holds no
// scoped route, no default, and no /1.
func Overlaps(f Facts) []Overlap {
	var overlaps []Overlap
	for _, t := range Tunnels(f) {
		if t.Followed {
			continue
		}
		if !t.Tailscale {
			overlaps = appendOverlap(overlaps, t, CGNAT(), func(p netip.Prefix) bool {
				return p.Bits() >= CGNAT().Bits() && p.Overlaps(CGNAT())
			})
		}
		for _, c := range f.Control.Prefixes {
			overlaps = appendOverlap(overlaps, t, c, c.Overlaps)
		}
	}
	return overlaps
}

func appendOverlap(overlaps []Overlap, t Tunnel, with netip.Prefix, wins func(netip.Prefix) bool) []Overlap {
	var routes []netip.Prefix
	for _, p := range t.Prefixes {
		if wins(p) {
			routes = append(routes, p)
		}
	}
	if len(routes) == 0 {
		return overlaps
	}
	return append(overlaps, Overlap{Iface: t.Iface, With: with, Routes: routes})
}
