//go:build darwin

package main

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

func TestStatus(t *testing.T) {
	r := underlay.Route{Dst: netip.MustParsePrefix("198.18.64.1/32"), Iface: "utun5", Owned: true}
	tests := []struct {
		name    string
		vpn     underlay.VPN
		dns     error
		plan    []underlay.Action
		blocked []underlay.Blocked
		want    int
	}{
		{name: "healthy", vpn: underlay.SplitDefault{}, want: 0},
		{name: "no VPN and nothing to do", vpn: underlay.NoVPN{}, want: 0},
		{name: "a change is necessary", vpn: underlay.SplitDefault{}, plan: []underlay.Action{underlay.Add{Route: r}}, want: 1},
		{name: "ambiguous has no plan but is not healthy", vpn: underlay.Ambiguous{}, want: 1},
		{name: "DNS failure has no plan but is not healthy", vpn: underlay.SplitDefault{}, dns: errors.New("no such host"), want: 1},
		{name: "a blocked route has no plan but is not healthy", vpn: underlay.SplitDefault{}, blocked: []underlay.Blocked{{Want: r, By: r}}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status(underlay.Facts{VPN: tt.vpn}, tt.dns, tt.plan, tt.blocked); got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}
