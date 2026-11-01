//go:build e2e && darwin

package e2e

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/rib"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// TestReconnectChangesInterface proves two facts of the kernel that the loop
// depends on. When an interface goes away, its routes go with it. When it
// comes back, it has a new index, and a route on it reads back again.
func TestReconnectChangesInterface(t *testing.T) {
	requireE2E(t)
	dst := netip.MustParsePrefix("198.18.40.10/32")
	addArgs := func(iface string) []string {
		return []string{"-n", "add", "-proto1", "-host", dst.Addr().String(), "-interface", iface}
	}

	old := newUTUN(t, "198.18.40.1", "198.18.40.2")
	oldIfc, err := net.InterfaceByName(old.Name)
	if err != nil {
		t.Fatalf("look up %s: %v", old.Name, err)
	}
	oldRoute := underlay.Route{Dst: dst, Iface: old.Name, Owned: true}
	routeCmd(t, addArgs(old.Name)...)
	if _, ok := lookup(t, oldRoute); !ok {
		t.Fatalf("rib.Fetch has no route %s on %s before the close", dst, old.Name)
	}

	old.Close(t)
	if got, ok := lookup(t, oldRoute); ok {
		t.Fatalf("rib.Fetch still has %+v after %s closed", got, old.Name)
	}

	fresh := newUTUN(t, "198.18.40.1", "198.18.40.2")
	freshIfc, err := net.InterfaceByName(fresh.Name)
	if err != nil {
		t.Fatalf("look up %s: %v", fresh.Name, err)
	}
	if freshIfc.Index == oldIfc.Index {
		t.Fatalf("%s has the index %d of the closed %s", fresh.Name, freshIfc.Index, old.Name)
	}
	routeCmd(t, addArgs(fresh.Name)...)
	want := underlay.Route{Dst: dst, Iface: fresh.Name, Owned: true}
	got, ok := lookup(t, want)
	if !ok {
		t.Fatalf("rib.Fetch has no route %s on %s after the reconnect", dst, fresh.Name)
	}
	if diff := cmp.Diff(want, got, equateIP); diff != "" {
		t.Errorf("route mismatch (-want +got):\n%s", diff)
	}
}

// TestWatchWakesOnChangesOnly proves that rib.Watch wakes for a route change
// and stays quiet for lookups. macOS sends the lookups of all processes to
// every routing socket, so a wake for each would make the daemon loop spin.
func TestWatchWakesOnChangesOnly(t *testing.T) {
	requireE2E(t)
	u := newUTUN(t, "198.18.30.1", "198.18.30.2")

	wake := make(chan struct{}, 1)
	watchErr := make(chan error, 1)
	go func() { watchErr <- rib.Watch(t.Context(), wake) }()
	t.Cleanup(func() {
		// t.Context ends before the cleanups run, so Watch is returning.
		if err := <-watchErr; err != nil {
			t.Errorf("rib.Watch: %v", err)
		}
	})

	// Watch gives no signal that its socket is open. Change a probe route
	// until the first wake shows that it listens.
	probe := "198.18.30.20"
	deadline := time.After(5 * time.Second)
	for live := false; !live; {
		routeCmd(t, "-n", "add", "-host", probe, "-interface", u.Name)
		select {
		case <-wake:
			live = true
		case <-time.After(100 * time.Millisecond):
		case <-deadline:
			t.Fatal("no wake after repeated route changes: the watcher is not listening")
		case err := <-watchErr:
			t.Fatalf("rib.Watch ended early: %v", err)
		}
		routeCmd(t, "-n", "delete", "-host", probe, "-interface", u.Name)
	}
	settle(wake)

	routeCmd(t, "-n", "add", "-proto1", "-host", "198.18.30.10", "-interface", u.Name)
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("no wake within 1s after a route add")
	}
	settle(wake)

	// A lookup sends RTM_GET to every routing socket.
	for range 3 {
		routeCmd(t, "-n", "get", "198.18.30.10")
	}
	select {
	case <-wake:
		t.Fatal("wake after route lookups; the watcher must ignore them")
	case <-time.After(time.Second):
	}
}

// settle drains wakes until none arrives for 200 ms, because one route
// change can send several messages.
func settle(wake <-chan struct{}) {
	for {
		select {
		case <-wake:
		case <-time.After(200 * time.Millisecond):
			return
		}
	}
}
