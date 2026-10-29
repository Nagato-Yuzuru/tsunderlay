//go:build darwin

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/rib"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

const dnsTimeout = 5 * time.Second

// check prints the facts and the plan that the daemon would use.
func check(ctx context.Context, w io.Writer) (int, error) {
	table, err := rib.Fetch()
	if err != nil {
		return 2, err
	}
	control, dnsErr := resolveControl(ctx)
	f := underlay.DeriveFacts(table, control)
	plan := underlay.Plan(f)

	if _, err := io.WriteString(w, report(f, dnsErr, plan)); err != nil {
		return 2, fmt.Errorf("writing the report: %w", err)
	}
	return status(f, plan), nil
}

func report(f underlay.Facts, dnsErr error, plan []underlay.Action) string {
	lines := []string{
		"lan:     " + lan(f.LAN),
		"vpn:     " + vpn(f.VPN),
	}
	if dnsErr != nil {
		lines = append(lines, fmt.Sprintf("control: %s: no answer (%v); owned control routes stay", underlay.ControlHost, dnsErr))
	} else {
		lines = append(lines, fmt.Sprintf("control: %s -> %s", underlay.ControlHost, joinPrefixes(f.Control)))
	}
	if len(plan) == 0 {
		lines = append(lines, "plan:    none")
	} else {
		lines = append(lines, "plan:")
		for _, a := range plan {
			lines = append(lines, "  "+routecmd.Path+" "+strings.Join(routecmd.Args(a), " "))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// status is the exit code of check. An ambiguous VPN state has no plan, but
// it is not healthy: Tailscale may use either tunnel.
func status(f underlay.Facts, plan []underlay.Action) int {
	if _, ok := f.VPN.(underlay.Ambiguous); ok || len(plan) > 0 {
		return 1
	}
	return 0
}

// resolveControl returns the /24s of the control plane. It returns no
// prefixes when DNS gives no answer, and Plan then keeps the owned routes.
func resolveControl(ctx context.Context) ([]netip.Prefix, error) {
	ctx, cancel := context.WithTimeout(ctx, dnsTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", underlay.ControlHost)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", underlay.ControlHost, err)
	}
	return underlay.ControlPrefixes(addrs), nil
}

func lan(l *underlay.Link) string {
	if l == nil {
		return "none (no global default on a physical interface)"
	}
	return link(*l)
}

func vpn(v underlay.VPN) string {
	switch v := v.(type) {
	case underlay.NoVPN:
		return "none (no global 0/1 and 128.0/1)"
	case underlay.SplitDefault:
		return link(v.Tunnel)
	case underlay.Ambiguous:
		return fmt.Sprintf("ambiguous: 0/1 %s, 128.0/1 %s; no change", hop(v.Low), hop(v.High))
	default:
		panic(fmt.Sprintf("check: unknown VPN state %T", v))
	}
}

func link(l underlay.Link) string {
	if !l.Gateway.IsValid() {
		return l.Iface + " (link)"
	}
	return l.Iface + " via " + l.Gateway.String()
}

func hop(r underlay.Route) string {
	return "on " + link(underlay.Link{Iface: r.Iface, Gateway: r.Via})
}

func joinPrefixes(ps []netip.Prefix) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}
