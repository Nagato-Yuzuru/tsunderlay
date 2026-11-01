//go:build e2e && e2e_guest && darwin

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"tailscale.com/net/netns"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/rib"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/testenv"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/testnet"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// The daemon tests run the tsunderlay binary as a child process, change the
// default gateway and /etc/hosts of the guest, and add global routes. So
// they run only in the tart guest of `mise run vm:e2e`.

const (
	// envDaemon names the tsunderlay binary. `mise run vm:e2e` sets it.
	envDaemon = "TSUNDERLAY_E2E_DAEMON"
	// daemonHost is the control host of the daemon. The .test domain never
	// resolves in DNS (RFC 6761), so only /etc/hosts gives it an address.
	daemonHost = "control.tsunderlay.test"
	// movedAddr is a second control address in TEST-NET-2 (RFC 5737).
	movedAddr = "198.51.100.10"
	// tunnelAddr is behind the full tunnel, in TEST-NET-3 (RFC 5737).
	tunnelAddr = "203.0.113.10"
	tcpdump    = "/usr/sbin/tcpdump"
)

func daemonControlURL() string { return "https://" + daemonHost }

// probeAddrs are the destinations of the probe routes of poke.
func probeAddrs() []string { return []string{"198.18.99.1", "198.18.99.2"} }

// guestScenario checks that no earlier test left a global /1 route or an
// owned route, and deletes the owned routes when the test ends. It returns
// the LAN link: the global default route of the guest.
func guestScenario(t *testing.T) underlay.Link {
	t.Helper()
	requireE2E(t)
	testenv.RequireCommand(t, tcpdump)
	table := fetch(t)
	for _, r := range table {
		if r.Owned || (!r.Scoped && r.Dst.Bits() == 1) {
			t.Fatalf("an earlier test left the route %+v; restart the guest", r)
		}
	}
	t.Cleanup(func() { deleteOwnedRoutes(t) })
	return lanLink(t, table)
}

// lanLink returns the interface and the gateway of the global default route.
func lanLink(t *testing.T, table []underlay.Route) underlay.Link {
	t.Helper()
	for _, r := range table {
		if r.Dst.Bits() == 0 && !r.Scoped && r.Via.IsValid() && !strings.HasPrefix(r.Iface, "utun") {
			return underlay.Link{Iface: r.Iface, Gateway: r.Via}
		}
	}
	t.Fatalf("the guest has no global default route via a gateway")
	return underlay.Link{}
}

func fetch(t *testing.T) []underlay.Route {
	t.Helper()
	table, err := rib.Fetch()
	if err != nil {
		t.Fatalf("rib.Fetch: %v", err)
	}
	return table
}

// deleteOwnedRoutes deletes each route with RTF_PROTO1. In the guest, only
// tsunderlay and the tests add such routes.
func deleteOwnedRoutes(t *testing.T) {
	t.Helper()
	table, err := rib.Fetch()
	if err != nil {
		t.Errorf("rib.Fetch: %v", err)
		return
	}
	for _, r := range table {
		if !r.Owned {
			continue
		}
		// t.Context is already canceled when cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := routecmd.Run(ctx, underlay.Delete{Route: r}); err != nil {
			t.Errorf("delete owned route %+v: %v", r, err)
		}
		cancel()
	}
}

// routeCleanup runs /sbin/route with args when the test ends. A delete of a
// route that is already gone is not an error.
func routeCleanup(t *testing.T, args ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, routecmd.Path, args...).CombinedOutput()
		if bytes.Contains(out, []byte("not in table")) {
			return
		}
		if err != nil || bytes.Contains(out, []byte("writing to routing socket")) {
			t.Errorf("route %s: %v: %s", strings.Join(args, " "), err, out)
		}
	})
}

// addScopedRoute adds the scoped route r by hand, with RTF_PROTO1 when
// r.Owned, and deletes it when the test ends. The arguments do not come
// from routecmd, so the setup does not depend on the code under test.
func addScopedRoute(t *testing.T, r underlay.Route) {
	t.Helper()
	args := []string{"-n", "add"}
	if r.Owned {
		args = append(args, "-proto1")
	}
	args = append(args, "-ifscope", r.Iface, "-net", r.Dst.String(), r.Via.String())
	routeCmd(t, args...)
	routeCleanup(t, "-n", "delete", "-ifscope", r.Iface, "-net", r.Dst.String())
}

// wantR1 is the owned route that keeps the control network dst reachable
// for a socket bound to the LAN interface.
func wantR1(lan underlay.Link, dst string) underlay.Route {
	return underlay.Route{
		Dst: netip.MustParsePrefix(dst), Iface: lan.Iface, Scoped: true, Via: lan.Gateway, Owned: true,
	}
}

// wantR2 is the owned host route to the VPN gateway on the link of the
// tunnel.
func wantR2(vpn *testnet.FakeVPN) underlay.Route {
	gw := vpn.Gateway()
	return underlay.Route{Dst: netip.PrefixFrom(gw, 32), Iface: vpn.Iface(), Owned: true}
}

// hasAll reports whether table holds each route of want, with the same
// next hop and ownership.
func hasAll(want ...underlay.Route) func([]underlay.Route) bool {
	return func(table []underlay.Route) bool {
		for _, w := range want {
			if !slices.Contains(table, w) {
				return false
			}
		}
		return true
	}
}

// hasDst reports whether table holds a route to dst on iface, in any form.
func hasDst(table []underlay.Route, dst, iface string) bool {
	p := netip.MustParsePrefix(dst)
	return slices.ContainsFunc(table, func(r underlay.Route) bool { return r.Dst == p && r.Iface == iface })
}

func ownedRoutes(table []underlay.Route) []underlay.Route {
	var owned []underlay.Route
	for _, r := range table {
		if r.Owned {
			owned = append(owned, r)
		}
	}
	return owned
}

// waitTable reads the routing table until cond accepts it, and fails the
// test when timeout ends first. The kernel gives no signal for a route that
// another process adds, so waitTable polls.
func waitTable(t *testing.T, timeout time.Duration, what string, cond func([]underlay.Route) bool) []underlay.Route {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		table := fetch(t)
		if cond(table) {
			return table
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %v: want %s; owned routes: %+v", timeout, what, ownedRoutes(table))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// logEvent holds the fields of a daemon log line that the tests read.
type logEvent struct {
	Msg      string      `json:"msg"`
	Trigger  string      `json:"tsunderlay.trigger"`
	VPN      string      `json:"tsunderlay.vpn"`
	DNSError string      `json:"tsunderlay.control.dns_error"`
	Actions  []logAction `json:"tsunderlay.actions"`
}

type logAction struct {
	Command string `json:"command"`
}

// changes reports whether the event applied a route command that names dst.
func (e logEvent) changes(dst string) bool {
	return slices.ContainsFunc(e.Actions, func(a logAction) bool {
		return slices.Contains(strings.Fields(a.Command), dst)
	})
}

// logSink collects the log lines of the daemon.
type logSink struct {
	mu      sync.Mutex
	partial []byte
	lines   []string
	// notify has a value when a line arrived since the last receive.
	notify chan struct{}
}

func newLogSink() *logSink { return &logSink{notify: make(chan struct{}, 1)} }

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		line, rest, ok := bytes.Cut(s.partial, []byte("\n"))
		if !ok {
			break
		}
		s.lines = append(s.lines, string(line))
		s.partial = rest
	}
	select {
	case s.notify <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (s *logSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

// daemonProc is a `tsunderlay run` child process.
type daemonProc struct {
	bin    string
	cmd    *exec.Cmd
	log    *logSink
	done   chan error
	probes int
}

// startDaemon starts `tsunderlay run` with the control URL of daemonHost,
// or with urls when they are given. A cleanup stops it with SIGTERM and
// prints its log when the test failed.
func startDaemon(t *testing.T, urls ...string) *daemonProc {
	t.Helper()
	bin := testenv.RequireEnv(t, envDaemon)
	testenv.RequireCommand(t, bin)
	if len(urls) == 0 {
		urls = []string{daemonControlURL()}
	}
	args := []string{"run"}
	for _, u := range urls {
		args = append(args, "--control-url", u)
	}
	sink := newLogSink()
	// The daemon must outlive t.Context: the cleanup stops it with a signal.
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), bin, args...)
	cmd.Stdout, cmd.Stderr = sink, sink
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	d := &daemonProc{bin: bin, cmd: cmd, log: sink, done: make(chan error, 1)}
	go func() { d.done <- cmd.Wait() }()
	t.Cleanup(func() {
		d.stop(t)
		if t.Failed() {
			for _, l := range sink.snapshot() {
				t.Logf("daemon: %s", l)
			}
		}
	})
	return d
}

// stop sends SIGTERM and waits for a clean exit.
func (d *daemonProc) stop(t *testing.T) {
	t.Helper()
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("signal the daemon: %v", err)
	}
	select {
	case err := <-d.done:
		if err != nil {
			t.Errorf("daemon exit: %v; want status 0", err)
		}
	case <-time.After(10 * time.Second):
		_ = d.cmd.Process.Kill() //nolint:errcheck // the error below reports the hang; Wait reports the end
		t.Errorf("the daemon did not exit within 10 s after SIGTERM; after a kill: %v", <-d.done)
	}
}

// events returns the log events that the daemon emitted.
func (d *daemonProc) events() []logEvent {
	var es []logEvent
	for _, l := range d.log.snapshot() {
		var e logEvent
		if json.Unmarshal([]byte(l), &e) == nil {
			es = append(es, e)
		}
	}
	return es
}

// waitEvent returns the first event from the index from on that match
// accepts. It fails the test when no such event arrives before timeout.
func (d *daemonProc) waitEvent(t *testing.T, from int, timeout time.Duration, what string, match func(logEvent) bool) logEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if es := d.events(); len(es) > from {
			if i := slices.IndexFunc(es[from:], match); i >= 0 {
				return es[from+i]
			}
		}
		select {
		case <-d.log.notify:
		case <-deadline:
			t.Fatalf("after %v: no daemon event %s", timeout, what)
		}
	}
}

// poke adds an owned probe route and waits until the daemon deletes it, so
// a reconcile ran after the call. The daemon backs off from a plan that
// repeats the plan before it, so the probes alternate between two
// addresses. It returns the probe address.
func (d *daemonProc) poke(t *testing.T) string {
	t.Helper()
	probe := probeAddrs()[d.probes%len(probeAddrs())]
	d.probes++
	routeCmd(t, "-n", "add", "-proto1", "-host", probe, "-interface", "lo0")
	waitTable(t, 5*time.Second, "the daemon deletes the owned probe route "+probe, func(table []underlay.Route) bool {
		return !hasDst(table, probe+"/32", "lo0")
	})
	return probe
}

// waitWatching returns when a route change woke the daemon. The daemon
// reconciles once at start without a wake, so only a reconcile with the
// trigger route-event proves that its route watcher listens.
func (d *daemonProc) waitWatching(t *testing.T) {
	t.Helper()
	for range 5 {
		from := len(d.events())
		probe := d.poke(t)
		e := d.waitEvent(t, from, time.Second, "that deletes "+probe, func(e logEvent) bool {
			return e.Msg == "routes changed" && e.changes(probe)
		})
		if e.Trigger == "route-event" {
			return
		}
	}
	t.Fatalf("no reconcile with the trigger route-event after 5 probe routes")
}

// pokeUntil pokes the daemon until done reports true. A change that sends
// no route message, such as an edit of /etc/hosts, needs a wake. The Go
// resolver caches /etc/hosts for 5 s, so more than one wake can be needed.
func (d *daemonProc) pokeUntil(t *testing.T, timeout time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("after %v: want %s; owned routes: %+v", timeout, what, ownedRoutes(fetch(t)))
		}
		d.poke(t)
	}
}

// check runs `tsunderlay check` with the control URL of daemonHost and
// returns its exit status and output.
func (d *daemonProc) check(t *testing.T) (int, string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), d.bin, "check", "--control-url", daemonControlURL()).CombinedOutput()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatalf("tsunderlay check: %v: %s", err, out)
	}
	return 0, string(out)
}

// dial dials addr:443 with a netns dialer of Tailscale for at most timeout.
// byRoute selects the dialer of the system app
// (TS_BIND_TO_INTERFACE_BY_ROUTE); otherwise it is the default dialer, which
// binds to the LAN interface like the Rex node. It returns the dial error.
func dial(t *testing.T, byRoute bool, addr string, timeout time.Duration) <-chan error {
	t.Helper()
	// The setting is global to the process, so no other dial runs at the
	// same time.
	netns.SetBindToInterfaceByRoute(t.Logf, byRoute)
	d := netns.NewDialer(t.Logf, newNetMon(t))
	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), timeout)
		defer cancel()
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(addr, "443"))
		if err == nil {
			_ = conn.Close() //nolint:errcheck // the dial result is the only output
		}
		netns.SetBindToInterfaceByRoute(t.Logf, false)
		errc <- err
	}()
	return errc
}

// requireR1Path proves R1 for the control address addr: the default netns
// dialer gets no ENETUNREACH, and its SYN leaves through the LAN interface.
// Nothing answers at addr, so a timeout is a pass.
func requireR1Path(t *testing.T, lan underlay.Link, addr string) {
	t.Helper()
	captured := captureSYN(t, lan.Iface, addr)
	err := <-dial(t, false, addr, time.Second)
	if errors.Is(err, syscall.ENETUNREACH) {
		t.Fatalf("R1 oracle: dial %s from %s: %v", addr, lan.Iface, err)
	}
	if !captured() {
		t.Fatalf("R1 oracle: no SYN to %s on %s; dial: %v", addr, lan.Iface, err)
	}
}

// captureSYN starts tcpdump on iface for a SYN to dst and waits until it
// listens. The returned function reports whether it saw one within 3 s.
func captureSYN(t *testing.T, iface, dst string) func() bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Second)
	cmd := exec.CommandContext(ctx, tcpdump, "-i", iface, "-n", "-c", "1",
		"tcp[tcpflags] & tcp-syn != 0 and dst host "+dst)
	out := newLogSink()
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start tcpdump: %v", err)
	}
	result := make(chan error, 1)
	go func() { result <- cmd.Wait() }()
	for !slices.ContainsFunc(out.snapshot(), func(l string) bool { return strings.Contains(l, "listening on") }) {
		select {
		case <-out.notify:
		case err := <-result:
			cancel()
			t.Fatalf("tcpdump ended before it listened: %v: %s", err, strings.Join(out.snapshot(), "\n"))
		}
	}
	return func() bool {
		defer cancel()
		return <-result == nil
	}
}

// requireR2Path proves R2: the bind-by-route dialer of the system app gets
// no ENETUNREACH, and its SYN to an address behind the tunnel arrives at
// the utun of vpn.
func requireR2Path(t *testing.T, vpn *testnet.FakeVPN) {
	t.Helper()
	dst := netip.MustParseAddr(tunnelAddr)
	errc := dial(t, true, tunnelAddr, 2*time.Second)
	seen := vpn.WaitPacket(t, 2*time.Second, func(p []byte) bool { return isSYNTo(p, dst) })
	err := <-errc
	if errors.Is(err, syscall.ENETUNREACH) {
		t.Fatalf("R2 oracle: bind-by-route dial %s: %v", tunnelAddr, err)
	}
	if !seen {
		t.Fatalf("R2 oracle: no SYN to %s arrived at %s; dial: %v", tunnelAddr, vpn.Iface(), err)
	}
}

// isSYNTo reports whether p is an IPv4 TCP SYN to dst.
func isSYNTo(p []byte, dst netip.Addr) bool {
	const protoTCP, flagSYN, flagACK = 6, 0x02, 0x10
	if len(p) < 20 || p[0]>>4 != 4 || p[9] != protoTCP {
		return false
	}
	ihl := int(p[0]&0x0f) * 4
	if len(p) < ihl+14 || netip.AddrFrom4([4]byte(p[16:20])) != dst {
		return false
	}
	flags := p[ihl+13]
	return flags&flagSYN != 0 && flags&flagACK == 0
}
