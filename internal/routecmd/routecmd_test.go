package routecmd

import (
	"errors"
	"net/netip"
	"os/exec"
	"slices"
	"testing"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/underlay"
)

var (
	r1 = underlay.Route{
		Dst: netip.MustParsePrefix("192.200.0.0/24"), Iface: "en0", Scoped: true,
		Via: netip.MustParseAddr("192.168.1.1"), Owned: true,
	}
	r2 = underlay.Route{Dst: netip.MustParsePrefix("198.18.64.1/32"), Iface: "utun5", Owned: true}
)

func TestArgs(t *testing.T) {
	tests := []struct {
		name   string
		action underlay.Action
		want   []string
	}{
		{
			name:   "add scoped network route via a gateway",
			action: underlay.Add{Route: r1},
			want:   []string{"-n", "add", "-proto1", "-ifscope", "en0", "-net", "192.200.0.0/24", "192.168.1.1"},
		},
		{
			name:   "add host route to a link",
			action: underlay.Add{Route: r2},
			want:   []string{"-n", "add", "-proto1", "-host", "198.18.64.1", "-interface", "utun5"},
		},
		{
			name:   "delete keys on destination and scope only",
			action: underlay.Delete{Route: r1},
			want:   []string{"-n", "delete", "-ifscope", "en0", "-net", "192.200.0.0/24"},
		},
		{
			name:   "delete global host route",
			action: underlay.Delete{Route: r2},
			want:   []string{"-n", "delete", "-host", "198.18.64.1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Args(tt.action); !slices.Equal(got, tt.want) {
				t.Errorf("Args = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResult(t *testing.T) {
	exitErr := &exec.ExitError{}
	tests := []struct {
		name    string
		action  underlay.Action
		out     string
		err     error
		wantErr bool
	}{
		{name: "add succeeds", action: underlay.Add{Route: r2}, out: "add host 198.18.64.1: gateway utun5\n"},
		{name: "add of a route that exists is success", action: underlay.Add{Route: r2}, out: "route: writing to routing socket: File exists\nadd host 198.18.64.1: gateway utun5: File exists\n"},
		{name: "delete of a route that is gone is success", action: underlay.Delete{Route: r2}, out: "route: writing to routing socket: not in table\ndelete host 198.18.64.1: not in table\n"},
		// route exits 0 for every kernel rejection. The cases above and below
		// use the output of macOS 27.
		{name: "socket error with exit 0 is a failure", action: underlay.Add{Route: r1}, out: "route: writing to routing socket: Network is unreachable\n", wantErr: true},
		{name: "non-zero exit is a failure", action: underlay.Add{Route: r1}, out: "route: must be root to alter routing table\n", err: exitErr, wantErr: true},
		{name: "delete does not accept File exists", action: underlay.Delete{Route: r1}, out: "route: writing to routing socket: File exists\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := result(tt.action, []byte(tt.out), tt.err, "args")
			if got := err != nil; got != tt.wantErr {
				t.Fatalf("result error = %v, want error %t", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrRoute) {
				t.Errorf("result error = %v, want ErrRoute", err)
			}
		})
	}
}
