package underlay

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

// The addresses follow the tables on the development Mac: Astrill on utun5
// (198.18.64.1), the system Tailscale app on utun3, Wi-Fi on en0.
var (
	lanGw    = netip.MustParseAddr("192.168.2.1")
	newLanGw = netip.MustParseAddr("10.0.0.1")
	vpnGw    = netip.MustParseAddr("198.18.64.1")
	ctl      = netip.MustParsePrefix("192.200.0.0/24")
	ctl2     = netip.MustParsePrefix("192.201.0.0/24")

	lanDefault = Route{Dst: pfx("0.0.0.0/0"), Iface: "en0", Via: lanGw}
	vpnLow     = Route{Dst: pfx("0.0.0.0/1"), Iface: "utun5", Via: vpnGw}
	vpnHigh    = Route{Dst: pfx("128.0.0.0/1"), Iface: "utun5", Via: vpnGw}
	// The system Tailscale app installs a scoped default on its own utun.
	tsScoped = Route{Dst: pfx("0.0.0.0/0"), Iface: "utun3", Scoped: true}
	// Kernel routes that are not part of the plan.
	vpnSubnet = Route{Dst: pfx("198.18.64.0/20"), Iface: "utun5"}
	lanSubnet = Route{Dst: pfx("192.168.2.0/24"), Iface: "en0"}

	r2       = Route{Dst: pfx("198.18.64.1/32"), Iface: "utun5", Owned: true}
	r1       = Route{Dst: ctl, Iface: "en0", Scoped: true, Via: lanGw, Owned: true}
	r1Second = Route{Dst: ctl2, Iface: "en0", Scoped: true, Via: lanGw, Owned: true}
)

var equateIP = cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func notOwned(r Route) Route {
	r.Owned = false
	return r
}

func astrill(extra ...Route) []Route {
	return append([]Route{lanDefault, lanSubnet, tsScoped, vpnLow, vpnHigh, vpnSubnet}, extra...)
}

func TestDeriveFacts(t *testing.T) {
	tests := []struct {
		name    string
		table   []Route
		wantLAN *Link
		wantVPN VPN
	}{
		{
			name:    "split default on one tunnel is a VPN; the scoped default of another tunnel is ignored",
			table:   astrill(),
			wantLAN: &Link{Iface: "en0", Gateway: lanGw},
			wantVPN: SplitDefault{Tunnel: Link{Iface: "utun5", Gateway: vpnGw}},
		},
		{
			name:    "no VPN when the split default is absent",
			table:   []Route{lanDefault, lanSubnet, tsScoped},
			wantLAN: &Link{Iface: "en0", Gateway: lanGw},
			wantVPN: NoVPN{},
		},
		{
			name:    "no VPN when only one half is present",
			table:   []Route{lanDefault, vpnLow},
			wantLAN: &Link{Iface: "en0", Gateway: lanGw},
			wantVPN: NoVPN{},
		},
		{
			name: "scoped halves are not a full tunnel",
			table: []Route{
				lanDefault,
				{Dst: pfx("0.0.0.0/1"), Iface: "utun7", Scoped: true, Via: vpnGw},
				{Dst: pfx("128.0.0.0/1"), Iface: "utun7", Scoped: true, Via: vpnGw},
			},
			wantLAN: &Link{Iface: "en0", Gateway: lanGw},
			wantVPN: NoVPN{},
		},
		{
			name: "halves on two tunnels are ambiguous",
			table: []Route{
				lanDefault, vpnLow,
				{Dst: pfx("128.0.0.0/1"), Iface: "utun7", Via: netip.MustParseAddr("10.8.0.1")},
			},
			wantLAN: &Link{Iface: "en0", Gateway: lanGw},
			wantVPN: Ambiguous{Low: vpnLow, High: Route{Dst: pfx("128.0.0.0/1"), Iface: "utun7", Via: netip.MustParseAddr("10.8.0.1")}},
		},
		{
			name:    "no LAN when the global default is on a tunnel",
			table:   []Route{{Dst: pfx("0.0.0.0/0"), Iface: "utun5", Via: vpnGw}, {Dst: pfx("0.0.0.0/0"), Iface: "en0", Scoped: true, Via: lanGw}},
			wantLAN: nil,
			wantVPN: NoVPN{},
		},
		{
			name:    "no LAN and no VPN on an empty table",
			table:   nil,
			wantLAN: nil,
			wantVPN: NoVPN{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveFacts(tt.table, []netip.Prefix{ctl})
			if diff := cmp.Diff(tt.wantLAN, got.LAN, equateIP); diff != "" {
				t.Errorf("LAN (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantVPN, got.VPN, equateIP); diff != "" {
				t.Errorf("VPN (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPlan(t *testing.T) {
	one := []netip.Prefix{ctl}
	tests := []struct {
		name    string
		table   []Route
		control []netip.Prefix
		want    []Action
	}{
		{
			name:    "adds R2 and R1 when the VPN comes up",
			table:   astrill(),
			control: []netip.Prefix{ctl, ctl2},
			want:    []Action{Add{r2}, Add{r1}, Add{r1Second}},
		},
		{
			name:    "no change on a correct table",
			table:   astrill(r2, r1),
			control: one,
			want:    nil,
		},
		{
			name:    "routes that the operator added count, and are not deleted",
			table:   astrill(notOwned(r2), notOwned(r1)),
			control: one,
			want:    nil,
		},
		{
			name:    "adds R2 again after a VPN reconnect deleted it",
			table:   astrill(r1),
			control: one,
			want:    []Action{Add{r2}},
		},
		{
			name:    "replaces R1 after a LAN gateway change, deletes first",
			table:   append([]Route{{Dst: pfx("0.0.0.0/0"), Iface: "en0", Via: newLanGw}, tsScoped, vpnLow, vpnHigh}, r2, r1),
			control: one,
			want: []Action{
				Delete{r1},
				Add{Route{Dst: ctl, Iface: "en0", Scoped: true, Via: newLanGw, Owned: true}},
			},
		},
		{
			name:    "keeps owned R1 when DNS gave no answer",
			table:   astrill(r2, r1, r1Second),
			control: nil,
			want:    nil,
		},
		{
			name:    "moves kept R1 to the new LAN gateway when DNS gave no answer",
			table:   append([]Route{{Dst: pfx("0.0.0.0/0"), Iface: "en0", Via: newLanGw}, vpnLow, vpnHigh}, r2, r1),
			control: nil,
			want: []Action{
				Delete{r1},
				Add{Route{Dst: ctl, Iface: "en0", Scoped: true, Via: newLanGw, Owned: true}},
			},
		},
		{
			name:    "does not keep R1 that the operator added when DNS gave no answer",
			table:   astrill(r2, notOwned(r1)),
			control: nil,
			want:    nil,
		},
		{
			name:    "follows a control plane that moved to a new /24",
			table:   astrill(r2, r1),
			control: []netip.Prefix{ctl2},
			want:    []Action{Delete{r1}, Add{r1Second}},
		},
		{
			name:    "deletes only owned routes when the VPN goes down",
			table:   []Route{lanDefault, lanSubnet, tsScoped, r2, r1, notOwned(r1Second)},
			control: one,
			want:    []Action{Delete{r2}, Delete{r1}},
		},
		{
			name:    "adds only R2 and deletes R1 when there is no LAN",
			table:   []Route{tsScoped, vpnLow, vpnHigh, vpnSubnet, r1},
			control: one,
			want:    []Action{Delete{r1}, Add{r2}},
		},
		{
			name: "no R2 when the VPN next hop is already a link",
			table: []Route{
				lanDefault,
				{Dst: pfx("0.0.0.0/1"), Iface: "utun5"},
				{Dst: pfx("128.0.0.0/1"), Iface: "utun5"},
			},
			control: one,
			want:    []Action{Add{r1}},
		},
		{
			name: "moves R2 to the tunnel that now holds the split default",
			table: []Route{
				lanDefault,
				{Dst: pfx("0.0.0.0/1"), Iface: "utun8", Via: vpnGw},
				{Dst: pfx("128.0.0.0/1"), Iface: "utun8", Via: vpnGw},
				r2, r1,
			},
			control: one,
			want:    []Action{Delete{r2}, Add{Route{Dst: r2.Dst, Iface: "utun8", Owned: true}}},
		},
		{
			name: "no change, not even a delete, when the split default is ambiguous",
			table: []Route{
				lanDefault, vpnLow,
				{Dst: pfx("128.0.0.0/1"), Iface: "utun7", Via: netip.MustParseAddr("10.8.0.1")},
				r2, r1,
			},
			control: one,
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Plan(DeriveFacts(tt.table, tt.control))
			if diff := cmp.Diff(tt.want, got, equateIP); diff != "" {
				t.Errorf("Plan (-want +got):\n%s", diff)
			}
		})
	}
}
