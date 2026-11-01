//go:build darwin

package main

import (
	"errors"
	"io/fs"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/Nagato-Yuzuru/tsunderlay/internal/install"
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

// installFS is the file system of installNote tests. Only Lstat and
// ReadFile are set; any other call is a bug of the test and panics on the
// nil embedded interface.
type installFS struct {
	install.FS
	dirs   map[string]install.FileStat
	binary []byte
	// readErr is the error of ReadFile.
	readErr error
}

func (f installFS) Lstat(name string) (install.FileStat, error) {
	if st, ok := f.dirs[name]; ok {
		return st, nil
	}
	return install.FileStat{}, fs.ErrNotExist
}

func (f installFS) ReadFile(name string) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.binary == nil {
		return nil, fs.ErrNotExist
	}
	return f.binary, nil
}

func TestInstallNote(t *testing.T) {
	safe := map[string]install.FileStat{}
	for _, d := range []string{"/", "/Library", install.HelperDir} {
		safe[d] = install.FileStat{Mode: fs.ModeDir | 0o755}
	}
	unsafe := map[string]install.FileStat{}
	for d, st := range safe {
		unsafe[d] = st
	}
	unsafe[install.HelperDir] = install.FileStat{UID: 501, Mode: fs.ModeDir | 0o755}
	tests := []struct {
		name      string
		fs        installFS
		wantNote  string
		wantStale bool
	}{
		{name: "same content", fs: installFS{dirs: safe, binary: []byte("v1")}},
		{
			name:      "other content",
			fs:        installFS{dirs: safe, binary: []byte("v0")},
			wantNote:  "the installed daemon differs from this binary: run sudo tsunderlay install",
			wantStale: true,
		},
		{name: "not installed", fs: installFS{dirs: safe}, wantNote: "not installed"},
		{
			name:      "unsafe directory",
			fs:        installFS{dirs: unsafe, binary: []byte("v1")},
			wantNote:  "cannot check the installed daemon: ",
			wantStale: true,
		},
		{
			name:      "unreadable copy",
			fs:        installFS{dirs: safe, readErr: fs.ErrPermission},
			wantNote:  "cannot check the installed daemon: ",
			wantStale: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			note, stale := installNote(install.Installer{FS: tt.fs}, []byte("v1"))

			if !strings.HasPrefix(note, tt.wantNote) || (tt.wantNote == "" && note != "") {
				t.Errorf("note = %q, want prefix %q", note, tt.wantNote)
			}
			if stale != tt.wantStale {
				t.Errorf("stale = %v, want %v", stale, tt.wantStale)
			}
		})
	}
}

func TestReport(t *testing.T) {
	addr := netip.MustParseAddr
	pfx := netip.MustParsePrefix
	lanDefault := underlay.Route{Dst: pfx("0.0.0.0/0"), Iface: "en0", Via: addr("192.168.2.1")}
	retire := `retire:  R1 (scoped control routes, for the Rex node): Rex: embedded node starts bound to the interface
         R2 (host route to the VPN next hop, for the system app): https://github.com/tailscale/tailscale/issues/21198
         env file line TS_BIND_TO_INTERFACE_BY_ROUTE=true (for the system app): https://github.com/tailscale/tailscale/issues/21198
`
	tests := []struct {
		name    string
		table   []underlay.Route
		control underlay.Control
		want    string
	}{
		{
			name: "tunnels, warnings, and the retirement parts",
			table: []underlay.Route{
				lanDefault,
				{Dst: pfx("0.0.0.0/0"), Iface: "utun3", Scoped: true},
				{Dst: pfx("0.0.0.0/1"), Iface: "utun5", Via: addr("198.18.64.1")},
				{Dst: pfx("128.0.0.0/1"), Iface: "utun5", Via: addr("198.18.64.1")},
				{Dst: pfx("10.253.0.0/16"), Iface: "utun6"},
				{Dst: pfx("100.64.0.0/10"), Iface: "utun6"},
			},
			control: underlay.Control{Prefixes: []netip.Prefix{pfx("192.200.0.0/24"), pfx("203.0.113.0/24")}, Complete: true},
			want: `lan:     en0 via 192.168.2.1
vpn:     utun5 via 198.18.64.1
tunnels: utun3: interface-scoped default; only sockets bound to it use it
         utun5: split default (0/1 and 128.0/1); tsunderlay follows it
         utun6: split tunnel: 10.253.0.0/16, 100.64.0.0/10
control: controlplane.tailscale.com -> 192.200.0.0/24, 203.0.113.0/24
warning: control prefix 203.0.113.0/24 is not in the list of https://tailscale.com/kb/1082/firewall-ports
warning: 100.64.0.0/10 on utun6 overlaps the Tailscale CGNAT range 100.64.0.0/10; tsunderlay does not fix this
plan:
  /sbin/route -n add -proto1 -host 198.18.64.1 -interface utun5
  /sbin/route -n add -proto1 -ifscope en0 -net 192.200.0.0/24 192.168.2.1
  /sbin/route -n add -proto1 -ifscope en0 -net 203.0.113.0/24 192.168.2.1
` + retire,
		},
		{
			name:    "no tunnel",
			table:   []underlay.Route{lanDefault},
			control: underlay.Control{Prefixes: []netip.Prefix{pfx("192.200.0.0/24")}, Complete: true},
			want: `lan:     en0 via 192.168.2.1
vpn:     none (no global 0/1 and 128.0/1)
tunnels: none
control: controlplane.tailscale.com -> 192.200.0.0/24
plan:    none
` + retire,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := underlay.DeriveFacts(tt.table, tt.control)
			got := report(f, []string{defaultControlHost}, nil, underlay.Plan(f), underlay.Blocks(f))
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("report (-want +got):\n%s", diff)
			}
		})
	}
}
