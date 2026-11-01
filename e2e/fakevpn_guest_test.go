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
	// controlAddr is in TEST-NET-1 (RFC 5737). Nothing answers there.
	controlAddr = "192.0.2.10"
	hostsPath   = "/etc/hosts"
)

// hostsFile edits /etc/hosts. On darwin, Go resolves through the system
// resolver also in a CGO_ENABLED=0 binary. An "ip4" lookup of a mapped name
// gets the mapped address from /etc/hosts and does not use DNS. An "ip"
// lookup also asks DNS for AAAA records, so it waits on the DNS server of
// the guest.
type hostsFile struct{ orig []byte }

// editHosts saves /etc/hosts and restores it when the test ends.
func editHosts(t *testing.T) *hostsFile {
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
	return &hostsFile{orig: orig}
}

// Map sets the original file plus one line that maps host to addr. It
// replaces the line of an earlier Map.
func (h *hostsFile) Map(t *testing.T, host, addr string) {
	t.Helper()
	h.write(t, append(append([]byte{}, h.orig...), []byte("\n"+addr+" "+host+"\n")...))
}

// Unmap sets the original file.
func (h *hostsFile) Unmap(t *testing.T) {
	t.Helper()
	h.write(t, h.orig)
}

func (h *hostsFile) write(t *testing.T, b []byte) {
	t.Helper()
	if err := os.WriteFile(hostsPath, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", hostsPath, err)
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
// failure did not occur. The test dials an IP literal: the bind does not
// depend on the name, and a name lookup would depend on the DNS server of
// the guest.
func TestFakeVPNBreaksTailscaleDialers(t *testing.T) {
	requireE2E(t)
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
			conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(controlAddr, "443"))
			if err == nil {
				_ = conn.Close() //nolint:errcheck // the test fails below
				t.Fatalf("dial %s succeeded; want ENETUNREACH", controlAddr)
			}
			if !errors.Is(err, syscall.ENETUNREACH) {
				t.Fatalf("dial %s: %v; want ENETUNREACH", controlAddr, err)
			}
			t.Logf("dial %s: %v", controlAddr, err)
		})
	}
}
