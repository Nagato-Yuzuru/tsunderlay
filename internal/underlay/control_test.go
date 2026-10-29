package underlay

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestControlPrefixes(t *testing.T) {
	addrs := func(ss ...string) []netip.Addr {
		var as []netip.Addr
		for _, s := range ss {
			as = append(as, netip.MustParseAddr(s))
		}
		return as
	}
	tests := []struct {
		name  string
		addrs []netip.Addr
		want  []netip.Prefix
	}{
		{name: "no answer gives no prefix", addrs: nil, want: nil},
		{name: "one address gives its /24", addrs: addrs("192.200.0.104"), want: []netip.Prefix{ctl}},
		{
			name:  "rotated addresses in one /24 give one prefix",
			addrs: addrs("192.200.0.104", "192.200.0.7", "192.200.0.250"),
			want:  []netip.Prefix{ctl},
		},
		{
			name:  "two /24s are sorted",
			addrs: addrs("192.201.0.1", "192.200.0.1"),
			want:  []netip.Prefix{ctl, ctl2},
		},
		{
			name:  "IPv6 is skipped and IPv4-mapped is IPv4",
			addrs: addrs("2606:b740:49::104", "::ffff:192.200.0.9"),
			want:  []netip.Prefix{ctl},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, ControlPrefixes(tt.addrs), equateIP); diff != "" {
				t.Errorf("ControlPrefixes (-want +got):\n%s", diff)
			}
		})
	}
}
