package underlay

import (
	"fmt"
	"net/netip"
	"slices"
)

// Tunnel is a tunnel interface (utun, ipsec, ppp) of the routing table, with
// its class.
type Tunnel struct {
	Iface string
	Class TunnelClass
}

// TunnelClass tells what the routes of a tunnel do to the Tailscale
// underlay.
//
//sumtype:decl
type TunnelClass interface{ isTunnelClass() }

// Followed holds the global 0/1 and 128.0/1 (Facts.VPN is SplitDefault on
// it). Unbound sockets use it, and Plan adds R1 and R2 for it.
type Followed struct{}

// SplitHalf holds a global 0/1 or 128.0/1 of an Ambiguous state.
type SplitHalf struct{}

// GlobalDefault holds the global default route. Then the LAN has no global
// default (Facts.LAN is nil), and Plan adds no R1. macOS keeps a scoped
// default on the physical interface, so dials bound to it still work.
type GlobalDefault struct{}

// ScopedDefault holds an interface-scoped default and no global default.
// Only sockets bound to the tunnel use it. The system Tailscale app and
// other network-extension VPNs have this shape.
type ScopedDefault struct{}

// SplitTunnel routes specific prefixes. Peer endpoints in them go through
// the tunnel, as for any app. Control and DERP do not.
type SplitTunnel struct{ Prefixes []netip.Prefix }

// OwnNetwork routes only its own network. A second full-tunnel VPN has this
// shape: while another tunnel holds 0/1 and 128.0/1, the kernel refuses its
// adds of them ("File exists"), so its traffic follows the other tunnel. A
// split tunnel whose only prefix is its own network has the same shape.
type OwnNetwork struct{}

func (Followed) isTunnelClass()      {}
func (SplitHalf) isTunnelClass()     {}
func (GlobalDefault) isTunnelClass() {}
func (ScopedDefault) isTunnelClass() {}
func (SplitTunnel) isTunnelClass()   {}
func (OwnNetwork) isTunnelClass()    {}

// Tunnels returns each tunnel interface of the table with its class, in the
// order of its first route in the table.
func Tunnels(f Facts) []Tunnel {
	var ts []Tunnel
	for _, r := range f.Table {
		if isTunnel(r.Iface) && !slices.ContainsFunc(ts, func(t Tunnel) bool { return t.Iface == r.Iface }) {
			ts = append(ts, Tunnel{Iface: r.Iface, Class: classify(f, r.Iface)})
		}
	}
	return ts
}

func classify(f Facts, iface string) TunnelClass {
	switch v := f.VPN.(type) {
	case SplitDefault:
		if v.Tunnel.Iface == iface {
			return Followed{}
		}
	case Ambiguous:
		if v.Low.Iface == iface || v.High.Iface == iface {
			return SplitHalf{}
		}
	case NoVPN:
	default:
		panic(fmt.Sprintf("underlay: unknown VPN state %T", v))
	}

	var scoped bool
	var prefixes []netip.Prefix
	for _, r := range f.Table {
		switch {
		case r.Iface != iface:
		case r.Dst.Bits() == 0 && !r.Scoped:
			return GlobalDefault{}
		case r.Dst.Bits() == 0:
			scoped = true
		case !r.Scoped && !r.ownNetwork():
			prefixes = append(prefixes, r.Dst)
		}
	}
	switch {
	case scoped:
		return ScopedDefault{}
	case len(prefixes) > 0:
		return SplitTunnel{Prefixes: prefixes}
	default:
		return OwnNetwork{}
	}
}

// ownNetwork reports whether r routes the network of its own interface and
// not a prefix that a VPN pushed. A host route is the local address of a
// point-to-point link, or a host route that the kernel cloned for a
// connection. A network route through a next hop in the network itself is
// the route of the interface address (route add -net <net> <local>). A
// pushed prefix is a link route (-interface) or a route through a next hop
// outside the prefix.
func (r Route) ownNetwork() bool {
	return r.Dst.IsSingleIP() || (r.Via.IsValid() && r.Dst.Contains(r.Via))
}

// CGNAT is the address range of Tailscale nodes (RFC 6598).
func CGNAT() netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{100, 64, 0, 0}), 10)
}

// Overlap is a global route of a tunnel that tsunderlay does not follow,
// which takes traffic to some addresses of With. With is CGNAT or a control
// prefix. Plan does not fix an overlap: it is another conflict than the one
// that R1 and R2 fix.
type Overlap struct {
	Route Route
	With  netip.Prefix
}

// Overlaps returns the routes of other tunnels that overlap the CGNAT range
// or a control prefix. It skips scoped routes, which only bound sockets use,
// and the defaults and /1 routes, which Tunnels and Facts.VPN report. It
// skips CGNAT on a tunnel with a scoped default: the system Tailscale app
// routes CGNAT on its own tunnel.
func Overlaps(f Facts) []Overlap {
	tunnels := Tunnels(f)
	var overlaps []Overlap
	for _, r := range f.Table {
		i := slices.IndexFunc(tunnels, func(t Tunnel) bool { return t.Iface == r.Iface })
		if i < 0 || r.Scoped || r.Dst.Bits() <= 1 {
			continue
		}
		var with []netip.Prefix
		switch tunnels[i].Class.(type) {
		case Followed:
			continue
		case ScopedDefault:
		case SplitHalf, GlobalDefault, SplitTunnel, OwnNetwork:
			with = append(with, CGNAT())
		default:
			panic(fmt.Sprintf("underlay: unknown tunnel class %T", tunnels[i].Class))
		}
		for _, p := range append(with, f.Control.Prefixes...) {
			if r.Dst.Overlaps(p) {
				overlaps = append(overlaps, Overlap{Route: r, With: p})
			}
		}
	}
	return overlaps
}
