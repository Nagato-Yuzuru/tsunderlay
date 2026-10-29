package underlay

import (
	"net/netip"
	"slices"
)

// Action is one change to the routing table.
//
//sumtype:decl
type Action interface{ isAction() }

// Add adds Route with RTF_PROTO1.
type Add struct{ Route Route }

// Delete deletes Route. Plan deletes only owned routes.
type Delete struct{ Route Route }

func (Add) isAction()    {}
func (Delete) isAction() {}

// Plan returns the actions that add the routes that the facts need, and that
// delete the owned routes that they do not need. All deletes come before all adds: the kernel keys a route
// by destination and scope, so a route with a new next hop must replace the
// old one. On a table that is already correct, Plan returns no actions.
func Plan(f Facts) []Action {
	if _, ok := f.VPN.(Ambiguous); ok {
		// A wrong guess sends Tailscale through the wrong tunnel, which is
		// worse than no change.
		return nil
	}
	want := desired(f)

	var actions []Action
	for _, r := range f.Table {
		if r.Owned && !slices.ContainsFunc(want, r.sameRoute) {
			actions = append(actions, Delete{Route: r})
		}
	}
	for _, r := range want {
		if !slices.ContainsFunc(f.Table, r.sameRoute) {
			actions = append(actions, Add{Route: r})
		}
	}
	return actions
}

func desired(f Facts) []Route {
	vpn, ok := f.VPN.(SplitDefault)
	if !ok {
		return nil
	}
	var want []Route

	// R2. The system app looks up the interface of the VPN next hop
	// (TS_BIND_TO_INTERFACE_BY_ROUTE). That lookup follows one more hop only
	// when it finds a link address, and a point-to-point VPN gives an IP
	// address at both hops. A host route to the link fixes the lookup.
	// Upstream: https://github.com/tailscale/tailscale/issues/21198
	if gw := vpn.Tunnel.Gateway; gw.IsValid() {
		want = append(want, Route{Dst: netip.PrefixFrom(gw, gw.BitLen()), Iface: vpn.Tunnel.Iface, Owned: true})
	}

	// R1. Rex's embedded node binds its sockets to the LAN interface until
	// its first netmap removes the binding, so its first control dial needs a
	// scoped route.
	// Upstream: Rex Beta Discord #feedback, "Tailscale for outbound
	// connections".
	if f.LAN != nil {
		for _, p := range controlPrefixes(f) {
			want = append(want, Route{Dst: p, Iface: f.LAN.Iface, Scoped: true, Via: f.LAN.Gateway, Owned: true})
		}
	}
	return want
}

// controlPrefixes returns f.Control. When DNS gave no answer, it returns the
// destinations of the owned scoped routes instead: absence of an answer is
// not evidence that they are stale.
func controlPrefixes(f Facts) []netip.Prefix {
	if len(f.Control) > 0 {
		return f.Control
	}
	var kept []netip.Prefix
	for _, r := range f.Table {
		if r.Owned && r.Scoped && !slices.Contains(kept, r.Dst) {
			kept = append(kept, r.Dst)
		}
	}
	return kept
}
