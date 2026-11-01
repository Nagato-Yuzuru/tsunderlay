//go:build e2e && e2e_guest && darwin

package e2e

import (
	"encoding/binary"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/testnet"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// The tests in this file run `tsunderlay run` against the routing table of
// the guest. Each test restores what it changed, so the order of the tests
// does not matter.

const (
	controlNet = "192.0.2.0/24"
	movedNet   = "198.51.100.0/24"
	// changeWithin is the bound for a change after a route message: the
	// daemon waits 1 s after the first message, then reconciles.
	changeWithin = 2 * time.Second
	// hostsWithin bounds a change that follows an edit of /etc/hosts.
	hostsWithin = 20 * time.Second
)

// upScenario maps daemonHost to controlAddr, starts the daemon, and
// connects the fake VPN. It returns when R1 and R2 are in place.
func upScenario(t *testing.T) (underlay.Link, *hostsFile, *daemonProc, *testnet.FakeVPN) {
	t.Helper()
	lan := guestScenario(t)
	hosts := editHosts(t)
	hosts.Map(t, daemonHost, controlAddr)
	d := startDaemon(t)
	d.waitWatching(t)
	vpn := testnet.NewFakeVPN(t)
	waitTable(t, changeWithin, "owned R1 and R2", hasAll(wantR1(lan, controlNet), wantR2(vpn)))
	return lan, hosts, d, vpn
}

func TestDaemonAddsOwnedRoutesWhenVPNComesUp(t *testing.T) {
	lan, _, d, vpn := upScenario(t)
	requireR1Path(t, lan, controlAddr)
	requireR2Path(t, vpn)
	if code, out := d.check(t); code != 0 {
		t.Errorf("tsunderlay check exit %d, want 0:\n%s", code, out)
	}
}

func TestDaemonMovesR2ToTheNewUTUNOnReconnect(t *testing.T) {
	_, _, _, vpn := upScenario(t)
	vpn.Reconnect(t)
	waitTable(t, changeWithin, "owned R2 on "+vpn.Iface(), hasAll(wantR2(vpn)))
	requireR2Path(t, vpn)
}

func TestDaemonMovesR1ToTheNewLANGateway(t *testing.T) {
	lan, _, _, _ := upScenario(t)
	gw := spareGateway(t, lan)
	// The test binary runs from the shared directory, and tart exec uses
	// no guest network, so a gateway without a host does not stop the test.
	routeCmd(t, "-n", "change", "default", gw.String())
	routeCleanup(t, "-n", "change", "default", lan.Gateway.String())

	moved := wantR1(lan, controlNet)
	moved.Via = gw
	waitTable(t, changeWithin, "owned R1 via "+gw.String(), hasAll(moved))
}

// spareGateway returns an address on the LAN link that is neither the
// gateway nor an address of the guest: the last host of the subnet.
func spareGateway(t *testing.T, lan underlay.Link) netip.Addr {
	t.Helper()
	ifc, err := net.InterfaceByName(lan.Iface)
	if err != nil {
		t.Fatalf("interface %s: %v", lan.Iface, err)
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		t.Fatalf("addresses of %s: %v", lan.Iface, err)
	}
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err != nil || !p.Addr().Is4() || !p.Masked().Contains(lan.Gateway) || p.Bits() > 30 {
			continue
		}
		base := p.Masked().Addr().As4()
		var bcast [4]byte
		binary.BigEndian.PutUint32(bcast[:], binary.BigEndian.Uint32(base[:])|(1<<(32-p.Bits())-1))
		last := netip.AddrFrom4(bcast).Prev()
		if last != p.Addr() && last != lan.Gateway {
			return last
		}
	}
	t.Fatalf("no spare gateway address on %s (%v)", lan.Iface, addrs)
	return netip.Addr{}
}

func TestDaemonMovesR1WhenTheControlAddressMoves(t *testing.T) {
	lan, hosts, d, _ := upScenario(t)
	hosts.Map(t, daemonHost, movedAddr)
	moved := wantR1(lan, movedNet)
	d.pokeUntil(t, hostsWithin, "owned R1 for "+movedNet+" only", func() bool {
		table := fetch(t)
		return hasAll(moved)(table) && !hasDst(table, controlNet, lan.Iface)
	})
}

func TestDaemonKeepsR1WhenTheControlHostDoesNotResolve(t *testing.T) {
	lan, hosts, d, _ := upScenario(t)
	hosts.Unmap(t)
	d.pokeUntil(t, hostsWithin, "a degraded event with a DNS error", func() bool {
		for _, e := range d.events() {
			if e.Msg == "degraded" && e.DNSError != "" {
				return true
			}
		}
		return false
	})
	// One more reconcile after the event: a delete of R1 would be done now.
	d.poke(t)
	if r1 := wantR1(lan, controlNet); !hasAll(r1)(fetch(t)) {
		t.Errorf("owned R1 %+v is gone after the DNS failure", r1)
	}
	code, out := d.check(t)
	if code != 1 || !strings.Contains(out, "no answer") {
		t.Errorf("tsunderlay check exit %d, want 1 with \"no answer\":\n%s", code, out)
	}
}

func TestDaemonLeavesOperatorRoutes(t *testing.T) {
	lan := guestScenario(t)
	editHosts(t).Map(t, daemonHost, controlAddr)
	operator := wantR1(lan, controlNet)
	operator.Owned = false
	addScopedRoute(t, operator)

	// The literal is a second control host that needs no DNS. Its owned R1
	// shows when the daemon reconciled.
	d := startDaemon(t, daemonControlURL(), "https://"+movedAddr)
	d.waitWatching(t)
	vpn := testnet.NewFakeVPN(t)
	owned := wantR1(lan, movedNet)
	waitTable(t, changeWithin, "owned R1 for "+movedNet+", R2, and the operator R1", hasAll(owned, wantR2(vpn), operator))
	for _, e := range d.events() {
		if e.changes(controlNet) {
			t.Errorf("the daemon changed the operator route %s: %+v", controlNet, e.Actions)
		}
	}

	vpn.Disconnect(t)
	waitTable(t, changeWithin, "no owned R1 for "+movedNet, func(table []underlay.Route) bool {
		return !hasDst(table, movedNet, lan.Iface)
	})
	if !hasAll(operator)(fetch(t)) {
		t.Errorf("the operator R1 %+v is gone after the VPN went down", operator)
	}
}

func TestDaemonDeletesOwnedRoutesWhenVPNGoesDown(t *testing.T) {
	lan := guestScenario(t)
	editHosts(t).Map(t, daemonHost, controlAddr)
	// A scoped route that no control host needs, added by the operator.
	operator := wantR1(lan, movedNet)
	operator.Owned = false
	addScopedRoute(t, operator)
	d := startDaemon(t)
	d.waitWatching(t)
	vpn := testnet.NewFakeVPN(t)
	waitTable(t, changeWithin, "owned R1 and R2", hasAll(wantR1(lan, controlNet), wantR2(vpn)))

	vpn.Disconnect(t)
	waitTable(t, changeWithin, "no owned route", func(table []underlay.Route) bool {
		return len(ownedRoutes(table)) == 0
	})
	if !hasAll(operator)(fetch(t)) {
		t.Errorf("the operator route %+v is gone after the VPN went down", operator)
	}
}

func TestDaemonChangesNothingWhenTwoTunnelsHoldTheGlobalRoutes(t *testing.T) {
	guestScenario(t)
	editHosts(t).Map(t, daemonHost, controlAddr)
	low := newUTUN(t, "198.18.72.2", "198.18.72.1")
	high := newUTUN(t, "198.18.73.2", "198.18.73.1")
	// The routes go with their utun when the test ends.
	routeCmd(t, "-n", "add", "-net", "0.0.0.0/1", "198.18.72.1")
	routeCmd(t, "-n", "add", "-net", "128.0.0.0/1", "198.18.73.1")
	// Without a VPN, the daemon deletes this owned route.
	seeded := underlay.Route{Dst: netip.MustParsePrefix("198.18.73.10/32"), Iface: high.Name, Owned: true}
	routeCmd(t, "-n", "add", "-proto1", "-host", "198.18.73.10", "-interface", high.Name)

	d := startDaemon(t)
	d.waitEvent(t, 0, 10*time.Second, "degraded for an ambiguous VPN", func(e logEvent) bool {
		return e.Msg == "degraded" && strings.HasPrefix(e.VPN, "ambiguous")
	})
	if owned := ownedRoutes(fetch(t)); len(owned) != 1 || owned[0] != seeded {
		t.Errorf("owned routes = %+v, want only %+v", owned, seeded)
	}
	code, out := d.check(t)
	if code != 1 || !strings.Contains(out, low.Name) || !strings.Contains(out, high.Name) {
		t.Errorf("tsunderlay check exit %d, want 1 with %s and %s:\n%s", code, low.Name, high.Name, out)
	}
}

func TestDaemonIgnoresASplitTunnelBesideTheFullTunnel(t *testing.T) {
	lan := guestScenario(t)
	editHosts(t).Map(t, daemonHost, controlAddr)
	split := newUTUN(t, "198.18.80.2", "198.18.80.1")
	routeCmd(t, "-n", "add", "-net", "10.253.0.0/16", "198.18.80.1")
	d := startDaemon(t)
	d.waitWatching(t)
	vpn := testnet.NewFakeVPN(t)
	table := waitTable(t, changeWithin, "owned R1 and R2", hasAll(wantR1(lan, controlNet), wantR2(vpn)))
	for _, r := range ownedRoutes(table) {
		if r.Iface == split.Name {
			t.Errorf("owned route %+v on the split tunnel", r)
		}
	}
	requireR1Path(t, lan, controlAddr)
	requireR2Path(t, vpn)
}

// TestDaemonConvergesFromAHalfAppliedTable starts the daemon on the table
// that a crash between two actions leaves. The daemon has no hook to stop
// between two actions, and a kill at a random time does not hit one
// reliably, so the test makes that table by hand.
func TestDaemonConvergesFromAHalfAppliedTable(t *testing.T) {
	tests := []struct {
		name string
		// seed adds the routes of the crashed daemon.
		seed func(t *testing.T, lan underlay.Link, vpn *testnet.FakeVPN)
	}{
		{
			name: "R2 added, R1 missing, owned R1 of an old control address left",
			seed: func(t *testing.T, lan underlay.Link, vpn *testnet.FakeVPN) {
				t.Helper()
				routeCmd(t, "-n", "add", "-proto1", "-host", vpn.Gateway().String(), "-interface", vpn.Iface())
				addScopedRoute(t, wantR1(lan, movedNet))
			},
		},
		{
			name: "R1 added, R2 missing",
			seed: func(t *testing.T, lan underlay.Link, _ *testnet.FakeVPN) {
				t.Helper()
				addScopedRoute(t, wantR1(lan, controlNet))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lan := guestScenario(t)
			editHosts(t).Map(t, daemonHost, controlAddr)
			vpn := testnet.NewFakeVPN(t)
			tt.seed(t, lan, vpn)

			d := startDaemon(t)
			want := []underlay.Route{wantR1(lan, controlNet), wantR2(vpn)}
			table := waitTable(t, changeWithin, "only owned R1 and R2", func(table []underlay.Route) bool {
				return hasAll(want...)(table) && len(ownedRoutes(table)) == len(want)
			})
			t.Logf("owned routes: %+v", ownedRoutes(table))
			if code, out := d.check(t); code != 0 {
				t.Errorf("tsunderlay check exit %d, want 0:\n%s", code, out)
			}
		})
	}
}

// TestDaemonFollowsTheFirstTunnelWhenASecondFullTunnelVPNComesUp runs the
// OpenVPN route commands of a second VPN B while the VPN A holds the global
// routes. The kernel refuses both adds with "File exists" and keeps the
// routes of A, so the global routes do not split and the daemon keeps R2 on
// A. When A goes down, no tunnel holds the global routes, because B never
// got them, so the daemon deletes its routes.
func TestDaemonFollowsTheFirstTunnelWhenASecondFullTunnelVPNComesUp(t *testing.T) {
	lan, _, d, a := upScenario(t)
	b := testnet.NewSecondFakeVPN(t)
	for _, l := range b.RouteLog() {
		t.Logf("%s", l)
		if !strings.Contains(l, "File exists") {
			t.Errorf("B route command: %q; want the kernel to refuse it with \"File exists\"", l)
		}
	}
	requireDaemonStaysOn(t, lan, d, a, b)

	a.Disconnect(t)
	table := waitTable(t, changeWithin, "no owned route", func(table []underlay.Route) bool {
		return len(ownedRoutes(table)) == 0
	})
	for _, r := range table {
		if r.Dst.Bits() == 1 {
			t.Errorf("route %+v after A went down; want no /1 route", r)
		}
	}
	code, out := d.check(t)
	t.Logf("check exit %d:\n%s", code, out)
	if code != 0 || !strings.Contains(out, "vpn:     none") {
		t.Errorf("tsunderlay check exit %d, want 0 with \"vpn:     none\":\n%s", code, out)
	}
}

// requireDaemonStaysOn checks the state after B came up: the global routes
// stay on a, nothing in the table belongs to b, and the daemon keeps R1 and
// R2 for a.
func requireDaemonStaysOn(t *testing.T, lan underlay.Link, d *daemonProc, a, b *testnet.FakeVPN) {
	t.Helper()
	aRoutes := []underlay.Route{
		{Dst: netip.MustParsePrefix("0.0.0.0/1"), Iface: a.Iface(), Via: a.Gateway()},
		{Dst: netip.MustParsePrefix("128.0.0.0/1"), Iface: a.Iface(), Via: a.Gateway()},
	}
	d.poke(t)
	table := fetch(t)
	for _, r := range table {
		if r.Dst.Bits() == 1 {
			t.Logf("rib.Fetch %+v", r)
		}
	}
	if !hasAll(aRoutes...)(table) {
		t.Errorf("the global routes of %s are gone: want %+v", a.Iface(), aRoutes)
	}
	for _, r := range table {
		if r.Dst.Bits() == 1 && r.Iface == b.Iface() {
			t.Errorf("route %+v on %s: B should hold no /1 route", r, b.Iface())
		}
	}
	want := []underlay.Route{wantR1(lan, controlNet), wantR2(a)}
	if owned := ownedRoutes(table); !hasAll(want...)(owned) || len(owned) != len(want) {
		t.Errorf("owned routes = %+v, want only %+v", owned, want)
	}
	requireR2Path(t, a)
	for _, e := range d.events() {
		if strings.HasPrefix(e.VPN, "ambiguous") {
			t.Errorf("the daemon saw an ambiguous VPN: %+v", e)
		}
	}
	code, out := d.check(t)
	t.Logf("check exit %d:\n%s", code, out)
	if want := "vpn:     " + a.Iface() + " via " + a.Gateway().String(); code != 0 || !strings.Contains(out, want) {
		t.Errorf("tsunderlay check exit %d, want 0 with %q:\n%s", code, want, out)
	}
}
