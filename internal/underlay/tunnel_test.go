package underlay

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Tunnel routes in the shapes that rib.Parse gives: the local address of a
// tunnel is a global /32 with no next hop (UH), a VPN network goes through
// the local address, and a cloned host route is scoped with a next hop.
var (
	addr = netip.MustParseAddr

	// The full-tunnel VPN on utun5: its local address, a VPN network through
	// it, and a cloned host route.
	vpnLocal  = Route{Dst: pfx("198.18.64.142/32"), Iface: "utun5"}
	vpnNet    = Route{Dst: pfx("198.18.64.0/20"), Iface: "utun5", Via: addr("198.18.64.142")}
	vpnCloned = Route{Dst: pfx("203.0.113.80/32"), Iface: "utun5", Scoped: true, Via: vpnGw}

	// The system Tailscale app on utun3: a scoped default, the CGNAT range,
	// Quad100, its local address, and a cloned route to a peer.
	tsCGNAT   = Route{Dst: pfx("100.64.0.0/10"), Iface: "utun3"}
	tsQuad100 = Route{Dst: pfx("100.100.100.100/32"), Iface: "utun3"}
	tsLocal   = Route{Dst: pfx("100.101.2.3/32"), Iface: "utun3"}
	tsCloned  = Route{Dst: pfx("100.101.9.9/32"), Iface: "utun3", Scoped: true, Via: addr("100.101.2.3")}
)

func withVPN(extra ...Route) []Route {
	return append([]Route{lanDefault, lanSubnet, vpnLow, vpnHigh, vpnNet, vpnLocal, vpnCloned}, extra...)
}

func systemTailscale() []Route { return []Route{tsScoped, tsCGNAT, tsQuad100, tsLocal, tsCloned} }

// vpnTunnel is the facts of utun5 in withVPN.
var vpnTunnel = Tunnel{Iface: "utun5", Followed: true, Low: true, High: true, Prefixes: []netip.Prefix{vpnNet.Dst}}

func TestTunnels(t *testing.T) {
	tests := []struct {
		name  string
		table []Route
		want  []Tunnel
	}{
		{
			name:  "the full-tunnel VPN is followed; the system Tailscale tunnel is labeled by Quad100",
			table: withVPN(systemTailscale()...),
			want: []Tunnel{
				vpnTunnel,
				{Iface: "utun3", Tailscale: true, ScopedDefault: true, Prefixes: []netip.Prefix{tsCGNAT.Dst}},
			},
		},
		{
			name: "a network-extension work VPN has a scoped default and routes its prefixes",
			table: withVPN(
				Route{Dst: pfx("0.0.0.0/0"), Iface: "utun6", Scoped: true},
				Route{Dst: pfx("10.253.0.0/16"), Iface: "utun6"},
				Route{Dst: pfx("100.64.0.0/16"), Iface: "utun6"},
			),
			want: []Tunnel{
				vpnTunnel,
				{Iface: "utun6", ScopedDefault: true, Prefixes: []netip.Prefix{pfx("10.253.0.0/16"), pfx("100.64.0.0/16")}},
			},
		},
		{
			name: "the system Tailscale app with an exit node holds the global default",
			table: []Route{
				{Dst: pfx("0.0.0.0/0"), Iface: "utun3"},
				{Dst: pfx("0.0.0.0/0"), Iface: "en0", Scoped: true, Via: lanGw},
				tsCGNAT, tsQuad100, tsLocal,
			},
			want: []Tunnel{{Iface: "utun3", Tailscale: true, GlobalDefault: true, Prefixes: []netip.Prefix{tsCGNAT.Dst}}},
		},
		{
			name:  "the system Tailscale tunnel without a scoped default is still Tailscale",
			table: withVPN(tsCGNAT, tsQuad100, tsLocal),
			want: []Tunnel{
				vpnTunnel,
				{Iface: "utun3", Tailscale: true, Prefixes: []netip.Prefix{tsCGNAT.Dst}},
			},
		},
		{
			name: "an OpenVPN subnet tunnel routes its network and a pushed prefix through a gateway inside it",
			table: withVPN(
				Route{Dst: pfx("10.8.0.0/24"), Iface: "utun7", Via: addr("10.8.0.2")},
				Route{Dst: pfx("10.8.0.2/32"), Iface: "utun7"},
				Route{Dst: pfx("10.0.0.0/8"), Iface: "utun7", Via: addr("10.8.0.1")},
			),
			want: []Tunnel{vpnTunnel, {Iface: "utun7", Prefixes: []netip.Prefix{pfx("10.8.0.0/24"), pfx("10.0.0.0/8")}}},
		},
		{
			name: "pushed host routes through a gateway are prefixes",
			table: withVPN(
				Route{Dst: pfx("203.0.113.10/32"), Iface: "utun6", Via: addr("10.9.0.1")},
				Route{Dst: pfx("192.200.0.104/32"), Iface: "utun6", Via: addr("10.9.0.1")},
			),
			want: []Tunnel{vpnTunnel, {Iface: "utun6", Prefixes: []netip.Prefix{pfx("203.0.113.10/32"), pfx("192.200.0.104/32")}}},
		},
		{
			name: "a second full tunnel without 0/1 and 128.0/1 shows only its network",
			table: withVPN(
				Route{Dst: pfx("198.18.96.0/20"), Iface: "utun7", Via: addr("198.18.96.2")},
				Route{Dst: pfx("198.18.96.2/32"), Iface: "utun7"},
			),
			want: []Tunnel{vpnTunnel, {Iface: "utun7", Prefixes: []netip.Prefix{pfx("198.18.96.0/20")}}},
		},
		{
			name:  "a lone 128.0/1 is not followed",
			table: []Route{lanDefault, {Dst: pfx("128.0.0.0/1"), Iface: "utun5", Via: vpnGw}, vpnNet, vpnLocal},
			want:  []Tunnel{{Iface: "utun5", High: true, Prefixes: []netip.Prefix{vpnNet.Dst}}},
		},
		{
			name:  "the halves of an ambiguous split default are on two tunnels",
			table: []Route{lanDefault, vpnLow, {Dst: pfx("128.0.0.0/1"), Iface: "ipsec0", Via: addr("10.8.0.1")}},
			want:  []Tunnel{{Iface: "utun5", Low: true}, {Iface: "ipsec0", High: true}},
		},
		{
			name:  "a tunnel with only scoped routes and its local address has no fact",
			table: []Route{lanDefault, {Dst: pfx("10.0.0.0/8"), Iface: "ppp0", Scoped: true}, {Dst: pfx("10.3.0.2/32"), Iface: "ppp0"}},
			want:  []Tunnel{{Iface: "ppp0"}},
		},
		{
			name:  "no tunnel on a table without one",
			table: []Route{lanDefault, lanSubnet},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Tunnels(DeriveFacts(tt.table, answered(ctl)))
			if diff := cmp.Diff(tt.want, got, equateIP); diff != "" {
				t.Errorf("Tunnels (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOverlaps(t *testing.T) {
	tests := []struct {
		name    string
		table   []Route
		control Control
		want    []Overlap
	}{
		{
			name: "no overlap with the full-tunnel VPN, the system Tailscale app, a work VPN, and a second full tunnel",
			table: withVPN(append(systemTailscale(),
				r2, r1,
				Route{Dst: pfx("10.253.0.0/16"), Iface: "utun6"},
				Route{Dst: pfx("198.18.96.0/20"), Iface: "utun7", Via: addr("198.18.96.2")},
				Route{Dst: pfx("198.18.96.2/32"), Iface: "utun7"},
			)...),
			control: answered(ctl),
			want:    nil,
		},
		{
			name: "a network-extension work VPN that routes a part of CGNAT",
			table: withVPN(append(systemTailscale(),
				Route{Dst: pfx("0.0.0.0/0"), Iface: "utun6", Scoped: true},
				Route{Dst: pfx("10.253.0.0/16"), Iface: "utun6"},
				Route{Dst: pfx("100.64.0.0/16"), Iface: "utun6"},
			)...),
			control: answered(ctl),
			want:    []Overlap{{Iface: "utun6", With: CGNAT(), Routes: []netip.Prefix{pfx("100.64.0.0/16")}}},
		},
		{
			name: "one warning for all CGNAT routes of a tunnel; its local address is not one",
			table: withVPN(
				Route{Dst: pfx("100.96.0.0/11"), Iface: "utun7", Via: addr("100.96.0.1")},
				Route{Dst: pfx("100.96.0.7/32"), Iface: "utun7"},
				Route{Dst: pfx("100.96.4.5/32"), Iface: "utun7", Via: addr("100.96.0.1")},
				Route{Dst: pfx("100.96.4.6/32"), Iface: "utun7", Scoped: true, Via: addr("100.96.0.1")},
			),
			control: answered(ctl),
			want:    []Overlap{{Iface: "utun7", With: CGNAT(), Routes: []netip.Prefix{pfx("100.96.0.0/11"), pfx("100.96.4.5/32")}}},
		},
		{
			name:    "a route exactly on CGNAT wins on a tie",
			table:   withVPN(Route{Dst: pfx("100.64.0.0/10"), Iface: "utun6"}),
			control: answered(ctl),
			want:    []Overlap{{Iface: "utun6", With: CGNAT(), Routes: []netip.Prefix{CGNAT()}}},
		},
		{
			name:    "a route less specific than CGNAT loses to the Tailscale route",
			table:   withVPN(Route{Dst: pfx("100.0.0.0/8"), Iface: "utun6"}, Route{Dst: pfx("64.0.0.0/2"), Iface: "utun7"}),
			control: answered(ctl),
			want:    nil,
		},
		{
			name:    "the Tailscale tunnel without a scoped default may route CGNAT",
			table:   withVPN(tsCGNAT, tsQuad100, tsLocal),
			control: answered(ctl),
			want:    nil,
		},
		{
			name:    "the Tailscale tunnel may not route a control prefix",
			table:   withVPN(append(systemTailscale(), Route{Dst: ctl, Iface: "utun3"})...),
			control: answered(ctl),
			want:    []Overlap{{Iface: "utun3", With: ctl, Routes: []netip.Prefix{ctl}}},
		},
		{
			name: "pushed routes that cover or sit in a control prefix",
			table: withVPN(
				Route{Dst: pfx("192.200.0.0/16"), Iface: "utun6"},
				Route{Dst: pfx("203.0.113.10/32"), Iface: "utun7", Via: addr("10.9.0.1")},
				Route{Dst: pfx("192.201.0.104/32"), Iface: "utun7", Via: addr("10.9.0.1")},
			),
			control: answered(ctl, ctl2),
			want: []Overlap{
				{Iface: "utun6", With: ctl, Routes: []netip.Prefix{pfx("192.200.0.0/16")}},
				{Iface: "utun7", With: ctl2, Routes: []netip.Prefix{pfx("192.201.0.104/32")}},
			},
		},
		{
			name:    "the routes of the followed tunnel and scoped routes do not overlap",
			table:   withVPN(Route{Dst: pfx("192.200.0.0/20"), Iface: "utun5", Via: vpnGw}, Route{Dst: pfx("100.64.0.0/16"), Iface: "utun6", Scoped: true}),
			control: answered(ctl),
			want:    nil,
		},
		{
			name:    "a /1 of an ambiguous split default is not an overlap; a /2 is",
			table:   []Route{lanDefault, vpnLow, {Dst: pfx("128.0.0.0/1"), Iface: "utun7", Via: addr("10.8.0.1")}, {Dst: pfx("192.0.0.0/2"), Iface: "utun7"}},
			control: answered(ctl),
			want:    []Overlap{{Iface: "utun7", With: ctl, Routes: []netip.Prefix{pfx("192.0.0.0/2")}}},
		},
		{
			name:    "a physical interface does not overlap",
			table:   withVPN(Route{Dst: pfx("100.64.0.0/16"), Iface: "en0", Via: lanGw}),
			control: answered(ctl),
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Overlaps(DeriveFacts(tt.table, tt.control))
			if diff := cmp.Diff(tt.want, got, equateIP); diff != "" {
				t.Errorf("Overlaps (-want +got):\n%s", diff)
			}
		})
	}
}
