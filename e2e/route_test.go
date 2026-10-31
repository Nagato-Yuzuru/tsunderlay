//go:build e2e && darwin

package e2e

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// TestFetchReadsRoutes adds routes with /sbin/route and checks that
// rib.Fetch reads each one with its flags and interface name. The routes do
// not come from routecmd, so a bug in the write path cannot hide a bug in
// the read path.
func TestFetchReadsRoutes(t *testing.T) {
	requireE2E(t)
	u := newUTUN(t, "198.18.10.1", "198.18.10.2")
	feth := newFeth(t, "198.18.11.1/24")

	tests := []struct {
		name string
		args []string
		want underlay.Route
	}{
		{
			name: "owned host route on the link",
			args: []string{"-n", "add", "-proto1", "-host", "198.18.10.10", "-interface", u.Name},
			want: underlay.Route{
				Dst: netip.MustParsePrefix("198.18.10.10/32"), Iface: u.Name, Owned: true,
			},
		},
		{
			name: "host route of the operator",
			args: []string{"-n", "add", "-host", "198.18.10.11", "-interface", u.Name},
			want: underlay.Route{Dst: netip.MustParsePrefix("198.18.10.11/32"), Iface: u.Name},
		},
		{
			name: "owned scoped network route via a gateway",
			args: []string{
				"-n", "add", "-proto1", "-ifscope", feth.A, "-net", "198.18.12.0/24", "198.18.11.2",
			},
			want: underlay.Route{
				Dst: netip.MustParsePrefix("198.18.12.0/24"), Iface: feth.A, Scoped: true,
				Via: netip.MustParseAddr("198.18.11.2"), Owned: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routeCmd(t, tt.args...)
			got, ok := lookup(t, tt.want)
			if !ok {
				t.Fatalf("rib.Fetch has no route %s on %s (scoped %v)", tt.want.Dst, tt.want.Iface, tt.want.Scoped)
			}
			if diff := cmp.Diff(tt.want, got, equateIP); diff != "" {
				t.Errorf("rib.Fetch route mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRunAppliesActions adds and deletes routes with routecmd.Run. A second
// add finds the route there ("File exists") and a second delete finds it
// gone ("not in table"); both are success.
func TestRunAppliesActions(t *testing.T) {
	requireE2E(t)
	u := newUTUN(t, "198.18.20.1", "198.18.20.2")
	feth := newFeth(t, "198.18.21.1/24")

	tests := []struct {
		name  string
		route underlay.Route
	}{
		{
			name:  "host route on the link",
			route: underlay.Route{Dst: netip.MustParsePrefix("198.18.20.10/32"), Iface: u.Name, Owned: true},
		},
		{
			name: "scoped network route via a gateway",
			route: underlay.Route{
				Dst: netip.MustParsePrefix("198.18.22.0/24"), Iface: feth.A, Scoped: true,
				Via: netip.MustParseAddr("198.18.21.2"), Owned: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			add, del := underlay.Add{Route: tt.route}, underlay.Delete{Route: tt.route}

			// Each action runs twice: the second run finds it in effect.
			for _, step := range []string{"add", "add again"} {
				run(t, step, add)
				got, ok := lookup(t, tt.route)
				if !ok {
					t.Fatalf("%s: rib.Fetch has no route after %s", step, routecmd.Line(add))
				}
				if diff := cmp.Diff(tt.route, got, equateIP); diff != "" {
					t.Fatalf("%s: route mismatch (-want +got):\n%s", step, diff)
				}
			}
			for _, step := range []string{"delete", "delete again"} {
				run(t, step, del)
				if got, ok := lookup(t, tt.route); ok {
					t.Fatalf("%s: rib.Fetch still has %+v after %s", step, got, routecmd.Line(del))
				}
			}
		})
	}
}

func run(t *testing.T, step string, a underlay.Action) {
	t.Helper()
	if err := routecmd.Run(t.Context(), a); err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

// netip types have unexported fields, so cmp needs to compare them with ==.
var equateIP = cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})
