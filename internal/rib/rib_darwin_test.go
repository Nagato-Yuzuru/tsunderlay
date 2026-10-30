package rib

import (
	"errors"
	"net/netip"
	"os"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/net/route"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

// The messages copy the shapes of the routing table of the development Mac.
// They are hand-written for the same reason as the fixtures of package
// underlay.
const (
	en0   = 14
	utun5 = 38
)

func v4(s string) *route.Inet4Addr {
	return &route.Inet4Addr{IP: netip.MustParseAddr(s).As4()}
}

func msg(flags, index int, dst, gw, mask route.Addr) *route.RouteMessage {
	addrs := make([]route.Addr, syscall.RTAX_MAX)
	addrs[syscall.RTAX_DST] = dst
	addrs[syscall.RTAX_GATEWAY] = gw
	addrs[syscall.RTAX_NETMASK] = mask
	return &route.RouteMessage{Flags: flags, Index: index, Addrs: addrs}
}

func TestParse(t *testing.T) {
	const up = syscall.RTF_UP
	names := map[int]string{en0: "en0", utun5: "utun5"}
	tests := []struct {
		name string
		msg  route.Message
		want []underlay.Route
	}{
		{
			name: "network route with an IP next hop",
			msg:  msg(up|syscall.RTF_GATEWAY, utun5, v4("0.0.0.0"), v4("198.18.64.1"), v4("128.0.0.0")),
			want: []underlay.Route{{Dst: netip.MustParsePrefix("0.0.0.0/1"), Iface: "utun5", Via: netip.MustParseAddr("198.18.64.1")}},
		},
		{
			name: "default route without a netmask is /0",
			msg:  msg(up|syscall.RTF_GATEWAY, en0, v4("0.0.0.0"), v4("192.168.1.1"), nil),
			want: []underlay.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0", Via: netip.MustParseAddr("192.168.1.1")}},
		},
		{
			name: "host route to a link has no next hop",
			msg:  msg(up|syscall.RTF_HOST, utun5, v4("198.18.64.1"), &route.LinkAddr{Index: utun5, Name: "utun5"}, nil),
			want: []underlay.Route{{Dst: netip.MustParsePrefix("198.18.64.1/32"), Iface: "utun5"}},
		},
		{
			name: "gateway slot without RTF_GATEWAY is not a next hop",
			msg:  msg(up, utun5, v4("198.18.64.0"), v4("198.18.64.173"), v4("255.255.240.0")),
			want: []underlay.Route{{Dst: netip.MustParsePrefix("198.18.64.0/20"), Iface: "utun5"}},
		},
		{
			name: "RTF_IFSCOPE is scoped and RTF_PROTO1 is owned",
			msg:  msg(up|syscall.RTF_GATEWAY|syscall.RTF_IFSCOPE|syscall.RTF_PROTO1, en0, v4("192.200.0.0"), v4("192.168.1.1"), v4("255.255.255.0")),
			want: []underlay.Route{{Dst: netip.MustParsePrefix("192.200.0.0/24"), Iface: "en0", Scoped: true, Via: netip.MustParseAddr("192.168.1.1"), Owned: true}},
		},
		{
			name: "skips a route on an interface that went away",
			msg:  msg(up|syscall.RTF_GATEWAY, 34, v4("0.0.0.0"), v4("198.18.64.1"), v4("128.0.0.0")),
			want: nil,
		},
		{
			name: "skips a netmask that is not contiguous",
			msg:  msg(up, en0, v4("10.0.0.0"), nil, v4("255.0.255.0")),
			want: nil,
		},
		{
			name: "skips a destination that is not IPv4",
			msg:  msg(up, en0, &route.Inet6Addr{}, nil, nil),
			want: nil,
		},
		{
			name: "skips messages that are not routes",
			msg:  &route.InterfaceMessage{Index: en0},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Parse([]route.Message{tt.msg}, names)
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})); diff != "" {
				t.Errorf("Parse (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFetchRetry(t *testing.T) {
	grown := os.NewSyscallError("sysctl", syscall.ENOMEM)
	other := os.NewSyscallError("sysctl", syscall.EPERM)
	tests := []struct {
		name      string
		errs      []error // the result of each read; nil means success
		wantErr   error
		wantReads int
	}{
		{name: "success at once", errs: []error{nil}, wantReads: 1},
		{name: "a grown table is read again", errs: []error{grown, grown, nil}, wantReads: 3},
		{name: "another error is not retried", errs: []error{other}, wantErr: syscall.EPERM, wantReads: 1},
		{name: "a table that keeps growing fails after the last attempt", errs: []error{grown, grown, grown, grown, grown}, wantErr: syscall.ENOMEM, wantReads: fetchAttempts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reads := 0
			_, err := fetchRetry(func() ([]byte, error) {
				err := tt.errs[reads]
				reads++
				return nil, err
			})
			if reads != tt.wantReads {
				t.Errorf("reads = %d, want %d", reads, tt.wantReads)
			}
			if tt.wantErr == nil && err != nil || tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
