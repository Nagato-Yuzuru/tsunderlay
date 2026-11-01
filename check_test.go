//go:build darwin

package main

import (
	"errors"
	"io/fs"
	"net/netip"
	"strings"
	"testing"

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
