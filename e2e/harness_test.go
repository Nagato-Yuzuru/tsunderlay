//go:build e2e && darwin

// Package e2e holds the end-to-end tests. They add routes and network
// interfaces, so they run only on a disposable host as root. Build them with
// `mise run e2e:build`.
//
// The tests change only routes in 198.18.0.0/15 on interfaces that they
// create. 198.18.0.0/15 is reserved for benchmark tests (RFC 2544), so it
// does not collide with real networks on the host. The tests never change a
// default route or another global route that the host uses. The kernel
// removes the routes of an interface when the interface goes away, so the
// interface cleanups also remove the routes of the tests.
package e2e

import (
	"bytes"
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/rib"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/testenv"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/testnet"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

func requireE2E(t *testing.T) {
	t.Helper()
	testenv.RequireDisposableHost(t)
	testenv.RequireRoot(t)
	testenv.RequireCommand(t, "ifconfig")
	testenv.RequireCommand(t, routecmd.Path)
}

// newUTUN returns a utun with the point-to-point address pair local, peer.
func newUTUN(t *testing.T, local, peer string) *testnet.UTUN {
	t.Helper()
	u := testnet.NewUTUN(t)
	u.SetAddr(t, netip.MustParseAddr(local), netip.MustParseAddr(peer))
	return u
}

// newFeth returns a feth pair. Side A has the address addr (CIDR form), so
// that a gateway in that network is on the link of A.
func newFeth(t *testing.T, addr string) testnet.FethPair {
	t.Helper()
	p := testnet.NewFethPair(t)
	testnet.Ifconfig(t, p.A, "inet", addr)
	return p
}

// routeCmd runs /sbin/route with args and fails the test on error. route
// exits 0 when the kernel rejects a change, so the output also decides.
func routeCmd(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), routecmd.Path, args...).CombinedOutput()
	if err != nil || bytes.Contains(out, []byte("writing to routing socket")) {
		t.Fatalf("route %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// lookup returns the route of the kernel with the key of want: the same
// destination, scope, and interface.
func lookup(t *testing.T, want underlay.Route) (underlay.Route, bool) {
	t.Helper()
	routes, err := rib.Fetch()
	if err != nil {
		t.Fatalf("rib.Fetch: %v", err)
	}
	for _, r := range routes {
		if r.Dst == want.Dst && r.Scoped == want.Scoped && r.Iface == want.Iface {
			return r, true
		}
	}
	return underlay.Route{}, false
}
