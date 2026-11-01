//go:build darwin && e2e_guest

package testnet

import (
	"bytes"
	"context"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
)

// The addresses of the fake VPN, in 198.18.0.0/15 (RFC 2544).
const (
	vpnLocal   = "198.18.64.2"
	vpnGateway = "198.18.64.1"
	vpnNet     = "198.18.64.0/20"
	vpnNetmask = "255.255.240.0"
)

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
	utun *UTUN
}

// NewFakeVPN connects the fake VPN and registers its removal with t.Cleanup.
func NewFakeVPN(t testing.TB) *FakeVPN {
	t.Helper()
	v := &FakeVPN{}
	v.connect(t)
	t.Cleanup(func() { v.Disconnect(t) })
	return v
}

// Iface returns the name of the current utun.
func (v *FakeVPN) Iface() string { return v.utun.Name }

// Gateway returns the next hop of the global routes.
func (v *FakeVPN) Gateway() netip.Addr { return netip.MustParseAddr(vpnGateway) }

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
	Ifconfig(t, v.utun.Name, vpnLocal, vpnLocal, "netmask", vpnNetmask, "up")
	route(t, "-n", "add", "-net", vpnNet, vpnLocal)
	for _, dst := range fullTunnel() {
		route(t, "-n", "add", "-net", dst, vpnGateway)
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
	for _, dst := range fullTunnel() {
		routeDelete(t, dst)
	}
	routeDelete(t, vpnNet)
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
