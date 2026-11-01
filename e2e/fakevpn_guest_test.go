//go:build e2e && e2e_guest && darwin

package e2e

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"tailscale.com/net/netmon"
	"tailscale.com/net/netns"
	"tailscale.com/util/eventbus"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/testnet"
)

// The tests in this file add global routes through testnet.FakeVPN, so they
// run only in the tart guest of `mise run vm:e2e`.

const (
	controlHost = "controlplane.tailscale.com"
	// controlAddr is in TEST-NET-1 (RFC 5737). Nothing answers there.
	controlAddr = "192.0.2.10"
	hostsPath   = "/etc/hosts"
)

// mapControlHost maps controlHost to controlAddr in /etc/hosts and restores
// the file when the test ends. The pure Go resolver of a CGO_ENABLED=0
// binary reads /etc/hosts, so a dial by name gets controlAddr.
func mapControlHost(t *testing.T) {
	t.Helper()
	orig, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("read %s: %v", hostsPath, err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(hostsPath, orig, 0o644); err != nil {
			t.Errorf("restore %s: %v", hostsPath, err)
		}
	})
	mapped := append(append([]byte{}, orig...), []byte("\n"+controlAddr+" "+controlHost+"\n")...)
	if err := os.WriteFile(hostsPath, mapped, 0o644); err != nil {
		t.Fatalf("write %s: %v", hostsPath, err)
	}
	addrs, err := net.DefaultResolver.LookupHost(t.Context(), controlHost)
	if err != nil || len(addrs) != 1 || addrs[0] != controlAddr {
		t.Fatalf("LookupHost(%s) = %v, %v; want [%s]", controlHost, addrs, err, controlAddr)
	}
}

// newNetMon returns a started network monitor, as tailscaled has.
// netns.NewDialer panics on a nil monitor.
func newNetMon(t *testing.T) *netmon.Monitor {
	t.Helper()
	bus := eventbus.New()
	t.Cleanup(bus.Close)
	netMon, err := netmon.New(bus, t.Logf)
	if err != nil {
		t.Fatalf("netmon.New: %v", err)
	}
	t.Cleanup(func() {
		if err := netMon.Close(); err != nil {
			t.Errorf("close netmon: %v", err)
		}
	})
	netMon.Start()
	return netMon
}

// TestFakeVPNBreaksTailscaleDialers reproduces the bug that tsunderlay
// fixes. Under a full-tunnel VPN and without tsunderlay, both dialers of
// tailscale.com/net/netns bind the socket to the interface of the default
// route. That interface has no route to the control plane, so the dial
// fails with ENETUNREACH:
//
//   - The default dialer binds to the default route interface directly.
//   - The bind-by-route dialer (the system app) finds the route through the
//     VPN gateway, gets no link address for the gateway, and falls back to
//     the default route interface.
//
// A dial that times out means that the packet left through the VPN, so the
// failure did not occur.
func TestFakeVPNBreaksTailscaleDialers(t *testing.T) {
	requireE2E(t)
	mapControlHost(t)
	vpn := testnet.NewFakeVPN(t)
	t.Logf("fake VPN on %s", vpn.Iface())
	netMon := newNetMon(t)

	tests := []struct {
		name    string
		byRoute bool
	}{
		{name: "default dialer", byRoute: false},
		{name: "bind-by-route dialer", byRoute: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The setting is global to the process, so the subtests do not
			// run in parallel.
			netns.SetBindToInterfaceByRoute(t.Logf, tt.byRoute)
			t.Cleanup(func() { netns.SetBindToInterfaceByRoute(t.Logf, false) })

			d := netns.NewDialer(t.Logf, netMon)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(controlHost, "443"))
			if err == nil {
				_ = conn.Close() //nolint:errcheck // the test fails below
				t.Fatalf("dial %s succeeded; want ENETUNREACH", controlHost)
			}
			if !errors.Is(err, syscall.ENETUNREACH) {
				t.Fatalf("dial %s: %v; want ENETUNREACH", controlHost, err)
			}
			t.Logf("dial %s: %v", controlHost, err)
		})
	}
}
