//go:build darwin

package main

import (
	"net/netip"
	"testing"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

func TestStatus(t *testing.T) {
	r := underlay.Route{Dst: netip.MustParsePrefix("198.18.64.1/32"), Iface: "utun5", Owned: true}
	tests := []struct {
		name string
		vpn  underlay.VPN
		plan []underlay.Action
		want int
	}{
		{name: "healthy", vpn: underlay.SplitDefault{}, want: 0},
		{name: "no VPN and nothing to do", vpn: underlay.NoVPN{}, want: 0},
		{name: "a change is necessary", vpn: underlay.SplitDefault{}, plan: []underlay.Action{underlay.Add{Route: r}}, want: 1},
		{name: "ambiguous has no plan but is not healthy", vpn: underlay.Ambiguous{}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status(underlay.Facts{VPN: tt.vpn}, tt.plan); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}
