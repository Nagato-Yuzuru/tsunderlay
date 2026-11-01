//go:build darwin && e2e_guest

package testnet

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
)

// vpnAddrs are the addresses of a fake VPN, in 198.18.0.0/15 (RFC 2544).
type vpnAddrs struct{ local, gateway, net, netmask string }

func firstVPN() vpnAddrs {
	return vpnAddrs{local: "198.18.64.2", gateway: "198.18.64.1", net: "198.18.64.0/20", netmask: "255.255.240.0"}
}

func secondVPN() vpnAddrs {
	return vpnAddrs{local: "198.18.96.2", gateway: "198.18.96.1", net: "198.18.96.0/20", netmask: "255.255.240.0"}
}

// fullTunnel holds the two halves of the IPv4 address space. Together they
// are more specific than the default route, so they take all traffic
// without a change to the default route.
func fullTunnel() []string { return []string{"0.0.0.0/1", "128.0.0.0/1"} }

// FakeVPN is a full-tunnel VPN that has the routes of an OpenVPN client on
// macOS: a utun, a route for the VPN network, and 0/1 and 128/1 through a
// gateway in that network. Nothing reads from the utun, so the packets that
// it takes are dropped.
//
// The global routes take all IPv4 traffic of the host. The e2e_guest build
// tag keeps this type out of every build that can run on a real host or CI;
// only the tart guest of `mise run vm:e2e` builds it.
type FakeVPN struct {
	utun  *UTUN
	addrs vpnAddrs
	// second is true for a VPN that comes up beside another one. The kernel
	// may reject its global routes, so connect records the output of each
	// route command and does not fail. Disconnect then leaves the global
	// routes to the kernel: a delete by destination could remove the
	// routes of the first VPN.
	second   bool
	routeLog []string
}

// NewFakeVPN connects the fake VPN and registers its removal with t.Cleanup.
func NewFakeVPN(t testing.TB) *FakeVPN {
	t.Helper()
	v := &FakeVPN{addrs: firstVPN()}
	v.connect(t)
	t.Cleanup(func() { v.Disconnect(t) })
	return v
}

// NewSecondFakeVPN connects a fake VPN with other addresses and a second
// utun, for a test that has a first one up. It does not fail when the
// kernel rejects its global routes; RouteLog holds what the kernel said.
func NewSecondFakeVPN(t testing.TB) *FakeVPN {
	t.Helper()
	v := &FakeVPN{addrs: secondVPN(), second: true}
	v.connect(t)
	t.Cleanup(func() { v.Disconnect(t) })
	return v
}

// RouteLog returns each global route command of a second VPN with its
// output, in the order that they ran.
func (v *FakeVPN) RouteLog() []string { return v.routeLog }

// Iface returns the name of the current utun.
func (v *FakeVPN) Iface() string { return v.utun.Name }

// Gateway returns the next hop of the global routes.
func (v *FakeVPN) Gateway() netip.Addr { return netip.MustParseAddr(v.addrs.gateway) }

// WaitPacket reads the packets that leave through the current utun until
// match accepts one. It returns false when no packet matches before timeout.
func (v *FakeVPN) WaitPacket(t testing.TB, timeout time.Duration, match func(packet []byte) bool) bool {
	t.Helper()
	return v.utun.WaitPacket(t, timeout, match)
}

// Reconnect closes the utun and connects again on a new one, as OpenVPN does
// after the tunnel drops.
func (v *FakeVPN) Reconnect(t testing.TB) {
	t.Helper()
	v.Disconnect(t)
	v.connect(t)
}

// connect runs the commands that OpenVPN runs on macOS.
func (v *FakeVPN) connect(t testing.TB) {
	t.Helper()
	v.utun = NewUTUN(t)
	a := v.addrs
	Ifconfig(t, v.utun.Name, a.local, a.local, "netmask", a.netmask, "up")
	route(t, "-n", "add", "-net", a.net, a.local)
	for _, dst := range fullTunnel() {
		if v.second {
			v.routeLog = append(v.routeLog, routeRecord(t, "-n", "add", "-net", dst, a.gateway))
			continue
		}
		route(t, "-n", "add", "-net", dst, a.gateway)
	}
}

// Disconnect removes the routes and the utun, as OpenVPN does when it stops.
// The kernel removes the routes of the utun with it, but an explicit delete
// does not depend on that. It does nothing when the VPN is already down.
func (v *FakeVPN) Disconnect(t testing.TB) {
	t.Helper()
	if v.utun == nil {
		return
	}
	if !v.second {
		for _, dst := range fullTunnel() {
			routeDelete(t, dst)
		}
	}
	routeDelete(t, v.addrs.net)
	// After a Reconnect, the cleanup of NewUTUN for the new utun can run
	// first. Close then does nothing.
	v.utun.Close(t)
	v.utun = nil
}

// route runs /sbin/route with args and fails the test on error. route exits
// 0 when the kernel rejects a change, so the output also decides.
func route(t testing.TB, args ...string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), routecmd.Path, args...).CombinedOutput()
	if err != nil || bytes.Contains(out, []byte("writing to routing socket")) {
		t.Fatalf("route %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// routeRecord runs /sbin/route with args and returns the command line with
// its output. It fails the test only when route does not run.
func routeRecord(t testing.TB, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), routecmd.Path, args...).CombinedOutput()
	if _, ok := errors.AsType[*exec.ExitError](err); err != nil && !ok {
		t.Fatalf("route %s: %v", strings.Join(args, " "), err)
	}
	return "route " + strings.Join(args, " ") + ": " + strings.TrimSpace(string(out))
}

// routeDelete deletes the network route dst. A route that is not in the
// table is not an error.
func routeDelete(t testing.TB, dst string) {
	t.Helper()
	// t.Context is already canceled when cleanups run.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, routecmd.Path, "-n", "delete", "-net", dst).CombinedOutput()
	if bytes.Contains(out, []byte("not in table")) {
		return
	}
	if err != nil || bytes.Contains(out, []byte("writing to routing socket")) {
		t.Errorf("route -n delete -net %s: %v: %s", dst, err, out)
	}
}
