package underlay

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// More tunnel shapes, hand-written after the tables of the development Macs.
var (
	// The system Tailscale app: a scoped default, the CGNAT range, and the
	// Quad100 host route, all on its own utun.
	tsCGNAT   = Route{Dst: pfx("100.64.0.0/10"), Iface: "utun3"}
	tsQuad100 = Route{Dst: pfx("100.100.100.100/32"), Iface: "utun3"}
	// A work VPN that pushes one prefix as a link route.
	workSplit = Route{Dst: pfx("10.253.0.0/16"), Iface: "utun6"}
	workSelf  = Route{Dst: pfx("10.253.4.9/32"), Iface: "utun6", Via: netip.MustParseAddr("10.253.4.9")}
	// A second full-tunnel VPN beside Astrill. The kernel refused its 0/1
	// and 128.0/1, so only the routes of its own network are left.
	secondNet  = Route{Dst: pfx("198.18.96.0/20"), Iface: "utun7", Via: netip.MustParseAddr("198.18.96.2")}
	secondSelf = Route{Dst: pfx("198.18.96.2/32"), Iface: "utun7", Via: netip.MustParseAddr("198.18.96.2")}
)

func TestTunnels(t *testing.T) {
	tests := []struct {
		name  string
		table []Route
		want  []Tunnel
	}{
		{
			name:  "Astrill split default is followed; the system Tailscale scoped default is ignored",
			table: astrill(tsCGNAT, tsQuad100),
			want: []Tunnel{
				{Iface: "utun3", Class: ScopedDefault{}},
				{Iface: "utun5", Class: Followed{}},
			},
		},
		{
			name:  "a work VPN beside Astrill is a split tunnel with its pushed prefix",
			table: astrill(workSplit, workSelf),
			want: []Tunnel{
				{Iface: "utun3", Class: ScopedDefault{}},
				{Iface: "utun5", Class: Followed{}},
				{Iface: "utun6", Class: SplitTunnel{Prefixes: []netip.Prefix{workSplit.Dst}}},
			},
		},
		{
			name:  "a second full tunnel without 0/1 and 128.0/1 routes its own network only",
			table: astrill(secondNet, secondSelf),
			want: []Tunnel{
				{Iface: "utun3", Class: ScopedDefault{}},
				{Iface: "utun5", Class: Followed{}},
				{Iface: "utun7", Class: OwnNetwork{}},
			},
		},
		{
			name: "a tunnel that replaced the global default",
			table: []Route{
				{Dst: pfx("0.0.0.0/0"), Iface: "utun5", Via: vpnGw},
				{Dst: pfx("0.0.0.0/0"), Iface: "en0", Scoped: true, Via: lanGw},
				vpnSubnet,
			},
			want: []Tunnel{{Iface: "utun5", Class: GlobalDefault{}}},
		},
		{
			name: "both tunnels of an ambiguous split default are halves",
			table: []Route{
				lanDefault, vpnLow,
				{Dst: pfx("128.0.0.0/1"), Iface: "ipsec0", Via: netip.MustParseAddr("10.8.0.1")},
			},
			want: []Tunnel{
				{Iface: "utun5", Class: SplitHalf{}},
				{Iface: "ipsec0", Class: SplitHalf{}},
			},
		},
		{
			name: "a split tunnel lists every pushed prefix, but not scoped or cloned host routes",
			table: []Route{
				lanDefault,
				{Dst: pfx("10.253.0.0/16"), Iface: "ppp0", Via: netip.MustParseAddr("172.16.0.1")},
				{Dst: pfx("10.254.0.0/16"), Iface: "ppp0"},
				{Dst: pfx("10.253.1.7/32"), Iface: "ppp0"},
				{Dst: pfx("10.255.0.0/16"), Iface: "ppp0", Scoped: true},
			},
			want: []Tunnel{{Iface: "ppp0", Class: SplitTunnel{Prefixes: []netip.Prefix{pfx("10.253.0.0/16"), pfx("10.254.0.0/16")}}}},
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
	cgnatVPN := Route{Dst: pfx("100.64.0.0/10"), Iface: "utun6"}
	wideVPN := Route{Dst: pfx("64.0.0.0/2"), Iface: "utun6"}
	ctlVPN := Route{Dst: pfx("192.200.0.0/16"), Iface: "utun6"}
	ctlHost := Route{Dst: pfx("192.201.0.9/32"), Iface: "utun7", Via: netip.MustParseAddr("198.18.96.2")}
	tests := []struct {
		name    string
		table   []Route
		control Control
		want    []Overlap
	}{
		{
			name:    "no overlap with Astrill, the system Tailscale app, a work VPN, and a second full tunnel",
			table:   astrill(r2, r1, tsCGNAT, tsQuad100, workSplit, workSelf, secondNet, secondSelf),
			control: answered(ctl),
			want:    nil,
		},
		{
			name:    "a tunnel that routes the CGNAT range",
			table:   astrill(cgnatVPN),
			control: answered(ctl),
			want:    []Overlap{{Route: cgnatVPN, With: CGNAT()}},
		},
		{
			name:    "a wider tunnel route that covers the whole CGNAT range",
			table:   astrill(wideVPN),
			control: answered(ctl),
			want:    []Overlap{{Route: wideVPN, With: CGNAT()}},
		},
		{
			name:    "a tunnel route that covers a control prefix",
			table:   astrill(ctlVPN),
			control: answered(ctl, ctl2),
			want:    []Overlap{{Route: ctlVPN, With: ctl}},
		},
		{
			name:    "a host route in the second control prefix on another tunnel",
			table:   astrill(secondNet, secondSelf, ctlHost),
			control: answered(ctl, ctl2),
			want:    []Overlap{{Route: ctlHost, With: ctl2}},
		},
		{
			name:    "the system Tailscale app may route CGNAT, but not a control prefix",
			table:   astrill(tsCGNAT, Route{Dst: ctl, Iface: "utun3"}),
			control: answered(ctl),
			want:    []Overlap{{Route: Route{Dst: ctl, Iface: "utun3"}, With: ctl}},
		},
		{
			name:    "the followed tunnel and scoped routes do not overlap",
			table:   astrill(Route{Dst: pfx("192.200.0.104/32"), Iface: "utun5"}, Route{Dst: pfx("100.64.0.0/10"), Iface: "utun6", Scoped: true}),
			control: answered(ctl),
			want:    nil,
		},
		{
			name:    "a physical interface does not overlap",
			table:   astrill(Route{Dst: pfx("100.64.0.0/10"), Iface: "en0", Via: lanGw}),
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
