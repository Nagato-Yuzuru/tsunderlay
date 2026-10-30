// Package underlay decides which routes the Tailscale underlay needs while a
// full-tunnel VPN is up. It does no I/O: callers read the routing table, and
// they apply the actions that Plan returns.
package underlay

import "net/netip"

// Route is one IPv4 entry of the kernel routing table.
type Route struct {
	Dst   netip.Prefix // host routes are /32
	Iface string       // the interface that the kernel sends on
	// Scoped is RTF_IFSCOPE: only sockets bound to Iface use the route.
	Scoped bool
	// Via is the next hop. The zero Addr means that Dst is on the link
	// Iface (route -interface).
	Via netip.Addr
	// Owned is RTF_PROTO1. tsunderlay adds every route with this flag and
	// deletes only routes that carry it.
	Owned bool
}

// sameRoute reports whether r and o are the same kernel route. Ownership is
// not part of the identity: a route that the operator added by hand counts.
func (r Route) sameRoute(o Route) bool {
	r.Owned, o.Owned = false, false
	return r == o
}

// sameKey reports whether the kernel keys r and o as one route: the same
// destination and the same scope. A global route has no scope interface.
func (r Route) sameKey(o Route) bool {
	return r.Dst == o.Dst && r.Scoped == o.Scoped && (!r.Scoped || r.Iface == o.Iface)
}

// Link is an interface with its next hop.
type Link struct {
	Iface   string
	Gateway netip.Addr // the zero Addr means a link route (route -interface)
}
