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
