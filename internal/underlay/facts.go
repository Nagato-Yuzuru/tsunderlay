package underlay

import (
	"net/netip"
	"strings"
)

// Facts is what Plan needs to know about the current network state.
type Facts struct {
	// LAN is the global default route on a physical interface. It is nil
	// when there is no such route, for example when Wi-Fi is off, or when a
	// VPN replaced the global default.
	LAN     *Link
	VPN     VPN
	Control Control
	Table   []Route
}

// Control is the answer for the control hosts.
type Control struct {
	// Prefixes holds the /24s of the hosts that DNS resolved.
	Prefixes []netip.Prefix
	// Complete is true when every host gave an answer. When a host gave no
	// answer, its routes are unknown, so Plan keeps the owned control routes.
	Complete bool
}

// VPN is the full-tunnel state that the global routes show.
//
//sumtype:decl
type VPN interface{ isVPN() }

// NoVPN means that the global 0/1 and 128.0/1 are not both present.
type NoVPN struct{}

// SplitDefault means that one tunnel holds the global 0/1 and 128.0/1 with
// the same next hop. Unbound sockets follow it, and en0-bound dials fail.
type SplitDefault struct{ Tunnel Link }

// Ambiguous means that the global 0/1 and 128.0/1 go to different
// interfaces or next hops. tsunderlay does not choose between them.
type Ambiguous struct{ Low, High Route }

func (NoVPN) isVPN()        {}
func (SplitDefault) isVPN() {}
func (Ambiguous) isVPN()    {}

// DeriveFacts reads the LAN and VPN state from the routing table, and adds
// the control answer.
func DeriveFacts(table []Route, control Control) Facts {
	var lan *Link
	var low, high *Route
	for i, r := range table {
		if r.Scoped {
			continue
		}
		// Table holds IPv4 prefixes only, so a /1 is either 0/1 or 128.0/1.
		switch {
		case r.Dst.Bits() == 0:
			if !isTunnel(r.Iface) && r.Via.IsValid() {
				lan = &Link{Iface: r.Iface, Gateway: r.Via}
			}
		case r.Dst.Bits() == 1 && r.Dst.Addr().As4()[0] == 0:
			low = &table[i]
		case r.Dst.Bits() == 1:
			high = &table[i]
		}
	}
	return Facts{LAN: lan, VPN: vpnState(low, high), Control: control, Table: table}
}

func vpnState(low, high *Route) VPN {
	switch {
	case low == nil || high == nil:
		return NoVPN{}
	case low.Iface == high.Iface && low.Via == high.Via:
		return SplitDefault{Tunnel: Link{Iface: low.Iface, Gateway: low.Via}}
	default:
		return Ambiguous{Low: *low, High: *high}
	}
}

// isTunnel reports whether the interface is a VPN tunnel and not a physical
// link.
func isTunnel(iface string) bool {
	for _, p := range []string{"utun", "ipsec", "ppp"} {
		if strings.HasPrefix(iface, p) {
			return true
		}
	}
	return false
}
