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

// Delete deletes Route. Plan deletes owned routes, and routes of the
// operator that hold the key of a desired route and cannot work (Route.staleIn).
type Delete struct{ Route Route }

func (Add) isAction()    {}
func (Delete) isAction() {}

// Plan returns the actions that add the routes that the facts need, and that
// delete the owned routes that they do not need. All deletes come before all
// adds: the kernel keys a route by destination and scope, so a route with a
// new next hop must replace the old one. On a table that is already correct,
// Plan returns no actions. Plan does not add a route that Blocks reports.
func Plan(f Facts) []Action {
	actions, _ := plan(f)
	return actions
}

// Blocked is a route that the facts need, but a route of the operator holds
// its key with another next hop that can work. The kernel refuses the add
// ("File exists"), and tsunderlay does not delete a working route that it
// does not own, so the operator must delete By.
type Blocked struct{ Want, By Route }

// Blocks returns the routes that Plan cannot add.
func Blocks(f Facts) []Blocked {
	_, blocked := plan(f)
	return blocked
}

func plan(f Facts) ([]Action, []Blocked) {
	if _, ok := f.VPN.(Ambiguous); ok {
		// A wrong guess sends Tailscale through the wrong tunnel, which is
		// worse than no change.
		return nil, nil
	}
	want := desired(f)

	var deletes, adds []Action
	for _, r := range f.Table {
		if r.Owned && !slices.ContainsFunc(want, r.sameRoute) {
			deletes = append(deletes, Delete{Route: r})
		}
	}
	var blocked []Blocked
	for _, r := range want {
		if slices.ContainsFunc(f.Table, r.sameRoute) {
			continue
		}
		// An owned route with the same key is deleted above.
		i := slices.IndexFunc(f.Table, func(o Route) bool { return !o.Owned && r.sameKey(o) })
		switch {
		case i < 0:
		case f.Table[i].staleIn(f.Table):
			// A manual repair from another network leaves such a route. It
			// cannot work, so replacing it removes nothing that the operator
			// uses.
			deletes = append(deletes, Delete{Route: f.Table[i]})
		default:
			blocked = append(blocked, Blocked{Want: r, By: f.Table[i]})
			continue
		}
		adds = append(adds, Add{Route: r})
	}
	actions := append(deletes, adds...)
	return actions, blocked
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

// controlPrefixes returns the resolved prefixes. When a host gave no answer,
// it adds the destinations of the owned scoped routes: absence of an answer
// is not evidence that they are stale. A route records no host, so it keeps
// all of them. The hosts that resolved still get their routes, so a wrong
// URL does not block the others.
func controlPrefixes(f Facts) []netip.Prefix {
	ps := slices.Clone(f.Control.Prefixes)
	if f.Control.Complete {
		return ps
	}
	for _, r := range f.Table {
		if r.Owned && r.Scoped && !slices.Contains(ps, r.Dst) {
			ps = append(ps, r.Dst)
		}
	}
	return ps
}
