//go:build darwin

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/install"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/rib"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/routecmd"
	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// check prints the facts and the plan that the daemon would use. It returns
// exitCode(1) when the state is not healthy.
func check(ctx context.Context, w io.Writer, hosts []string) error {
	table, err := rib.Fetch()
	if err != nil {
		return err
	}
	control, dnsErr := resolveControl(ctx, net.DefaultResolver.LookupNetIP, hosts)
	f := underlay.DeriveFacts(table, control)
	plan := underlay.Plan(f)

	if _, err := io.WriteString(w, report(f, hosts, dnsErr, plan, underlay.Blocks(f))); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}
	code := status(f, dnsErr, plan, underlay.Blocks(f))

	exe, err := executable()
	if err != nil {
		return err
	}
	want, err := os.ReadFile(exe)
	if err != nil {
		return fmt.Errorf("reading this binary: %w", err)
	}
	note, stale := installNote(install.Installer{FS: install.OS{}}, want)
	if note != "" {
		if _, err := io.WriteString(w, note+"\n"); err != nil {
			return fmt.Errorf("writing the report: %w", err)
		}
	}
	if code != 0 || stale {
		return exitCode(1)
	}
	return nil
}

// installNote compares the installed daemon with want. It returns the line
// to print, and whether the install is stale, which is a failure of check.
// No install is not a failure: the operator runs check before the first
// install. A copy that matches needs no line.
func installNote(in install.Installer, want []byte) (note string, stale bool) {
	m, err := in.Installed(want)
	if err != nil {
		return fmt.Sprintf("cannot check the installed daemon: %v", err), true
	}
	switch m {
	case install.NotInstalled:
		return "not installed", false
	case install.Same:
		return "", false
	case install.Different:
		return "the installed daemon differs from this binary: run sudo tsunderlay install", true
	case install.MatchUnspecified:
		panic("check: Installed returned MatchUnspecified")
	default:
		panic(fmt.Sprintf("check: unknown install match %d", m))
	}
}

func report(f underlay.Facts, hosts []string, dnsErr error, plan []underlay.Action, blocked []underlay.Blocked) string {
	lines := []string{
		"lan:     " + lan(f.LAN),
	}
	if underlay.ScopedLANDefault(f) {
		lines = append(lines, fmt.Sprintf("note:    %[1]s has an interface-scoped default; sockets bound to %[1]s bypass the VPN. "+
			"Delete it if you added it by hand: sudo route -n delete -ifscope %[1]s default", f.LAN.Iface))
	}
	lines = append(lines, "vpn:     "+vpn(f.VPN))
	lines = append(lines, block("tunnels:", tunnelLines(underlay.Tunnels(f)))...)
	lines = append(lines, fmt.Sprintf("control: %s -> %s", strings.Join(hosts, ", "), joinPrefixes(f.Control.Prefixes)))
	if dnsErr != nil {
		lines = append(lines, fmt.Sprintf("         no answer: %v; owned control routes stay", strings.ReplaceAll(dnsErr.Error(), "\n", "; ")))
	}
	for _, p := range undocumentedControl(hosts, f.Control.Prefixes) {
		lines = append(lines, fmt.Sprintf("warning: control prefix %s is not in the list of %s", p, underlay.FirewallDocURL))
	}
	for _, o := range underlay.Overlaps(f) {
		lines = append(lines, "warning: "+overlapLine(o))
	}
	for _, b := range blockedLines(blocked) {
		lines = append(lines, "blocked: "+b)
	}
	if len(plan) == 0 {
		lines = append(lines, "plan:    none")
	} else {
		lines = append(lines, "plan:")
		for _, a := range plan {
			lines = append(lines, "  "+routecmd.Line(a))
		}
	}
	lines = append(lines, block("retire:", partLines(underlay.Parts()))...)
	return strings.Join(lines, "\n") + "\n"
}

// block puts label before the first line and indents the others under it.
// It gives "label none" for no lines.
func block(label string, lines []string) []string {
	if len(lines) == 0 {
		lines = []string{"none"}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		prefix := strings.Repeat(" ", len("control: "))
		if i == 0 {
			prefix = fmt.Sprintf("%-9s", label)
		}
		out[i] = prefix + l
	}
	return out
}

func tunnelLines(ts []underlay.Tunnel) []string {
	lines := make([]string, len(ts))
	for i, t := range ts {
		lines[i] = tunnelLine(t)
	}
	return lines
}

// tunnelLine prints the facts of a tunnel, for example
// "utun3 (Tailscale): scoped default; routes 100.64.0.0/10".
func tunnelLine(t underlay.Tunnel) string {
	name := t.Iface
	if t.Tailscale {
		name += " (Tailscale)"
	}
	var facts []string
	if t.GlobalDefault {
		facts = append(facts, "global default")
	}
	if t.ScopedDefault {
		facts = append(facts, "scoped default")
	}
	switch {
	case t.Low && t.High:
		facts = append(facts, "global 0/1 and 128.0/1")
	case t.Low:
		facts = append(facts, "global 0/1 only")
	case t.High:
		facts = append(facts, "global 128.0/1 only")
	}
	if len(t.Prefixes) > 0 {
		facts = append(facts, "routes "+listPrefixes(t.Prefixes))
	}
	if len(facts) == 0 {
		facts = append(facts, "no global route")
	}
	if t.Followed {
		facts = append(facts, "tsunderlay follows it")
	}
	return name + ": " + strings.Join(facts, "; ")
}

// maxListed bounds the prefixes in one line: a VPN can push thousands.
const maxListed = 5

// listPrefixes joins at most maxListed prefixes, and counts the others.
func listPrefixes(ps []netip.Prefix) string {
	s := joinPrefixes(ps[:min(len(ps), maxListed)])
	if n := len(ps) - maxListed; n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s
}

func overlapLine(o underlay.Overlap) string {
	what := "control prefix " + o.With.String()
	if o.With == underlay.CGNAT() {
		what = "the Tailscale CGNAT range " + o.With.String()
	}
	return fmt.Sprintf("%s routes %s into %s; tsunderlay does not fix this", o.Iface, listPrefixes(o.Routes), what)
}

func partLines(ps []underlay.Part) []string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.Name + ": " + p.Upstream
	}
	return lines
}

// status is the exit code of check. Three states have no plan, but are not
// healthy: an ambiguous VPN state (Tailscale may use either tunnel), a DNS
// failure (check cannot confirm the control routes), and a blocked route.
func status(f underlay.Facts, dnsErr error, plan []underlay.Action, blocked []underlay.Blocked) int {
	if _, ok := f.VPN.(underlay.Ambiguous); ok || dnsErr != nil || len(plan) > 0 || len(blocked) > 0 {
		return 1
	}
	return 0
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
	if len(ps) == 0 {
		return "none"
	}
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}

// blockedLines describes each blocked route with the command that removes
// the operator route.
func blockedLines(blocked []underlay.Blocked) []string {
	lines := make([]string, len(blocked))
	for i, b := range blocked {
		lines[i] = fmt.Sprintf("%s is held by a route that tsunderlay does not own (%s); delete it: %s",
			b.Want.Dst, link(underlay.Link{Iface: b.By.Iface, Gateway: b.By.Via}), routecmd.Line(underlay.Delete{Route: b.By}))
	}
	return lines
}
