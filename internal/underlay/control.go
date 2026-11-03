package underlay

import (
	"net/netip"
	"slices"
)

// ControlPrefixes maps the resolved control-plane addresses to their /24s,
// sorted and without duplicates. DNS answers rotate through the addresses of
// a /24, and Tailscale's own resolver can pick any of them, so a /32 route
// would miss the next rotation.
func ControlPrefixes(addrs []netip.Addr) []netip.Prefix {
	var ps []netip.Prefix
	for _, a := range addrs {
		a = a.Unmap()
		if !a.Is4() {
			continue
		}
		ps = append(ps, netip.PrefixFrom(a, 24).Masked())
	}
	slices.SortFunc(ps, func(a, b netip.Prefix) int { return a.Addr().Compare(b.Addr()) })
	return slices.Compact(ps)
}

// FirewallDocURL is the page of the Tailscale firewall documentation that
// lists the control prefixes of tailscale.com for IP-based rules.
const FirewallDocURL = "https://tailscale.com/kb/1082/firewall-ports"

// UndocumentedControl returns the prefixes that are not in the list of
// FirewallDocURL. The list is only a check for the control plane of
// tailscale.com: the routes come from DNS. A prefix that is not in the list
// means that Tailscale moved the control plane, or that DNS gave a wrong
// answer.
func UndocumentedControl(ps []netip.Prefix) []netip.Prefix {
	documented := []netip.Prefix{netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 200, 0, 0}), 24)}
	var out []netip.Prefix
	for _, p := range ps {
		if !slices.Contains(documented, p) {
			out = append(out, p)
		}
	}
	return out
}
